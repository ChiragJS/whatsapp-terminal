package whatsapp

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/chirag/whatsapp-terminal/internal/config"
	"github.com/chirag/whatsapp-terminal/internal/domain"
	appstore "github.com/chirag/whatsapp-terminal/internal/store"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	waevents "go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

type Adapter struct {
	cfg config.Config

	repo   *appstore.Store
	logger *slog.Logger
	events chan domain.Event

	mu        sync.RWMutex
	baseCtx   context.Context
	client    *whatsmeow.Client
	sessionDB *sql.DB

	bootstrapMu             sync.Mutex
	initialHistoryRequested bool

	contactSyncMu      sync.Mutex
	contactSyncRunning bool
}

func NewAdapter(cfg config.Config, repo *appstore.Store, logger *slog.Logger) *Adapter {
	return &Adapter{
		cfg:    cfg,
		repo:   repo,
		logger: logger,
		events: make(chan domain.Event, 128),
	}
}

func (a *Adapter) Events() <-chan domain.Event {
	return a.events
}

func (a *Adapter) Start(ctx context.Context) error {
	sessionPath := filepath.Join(a.cfg.DataDir, fmt.Sprintf("%s-session.db", a.cfg.SessionName))
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", sessionPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("open session db: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	waLogger := newBridgeLogger(a.logger.With("component", "whatsmeow"))
	container := sqlstore.NewWithDB(db, "sqlite", waLogger.Sub("Database"))
	if err := container.Upgrade(ctx); err != nil {
		_ = db.Close()
		return fmt.Errorf("upgrade session db: %w", err)
	}

	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		_ = db.Close()
		return fmt.Errorf("load device session: %w", err)
	}

	client := whatsmeow.NewClient(device, waLogger.Sub("Client"))
	client.EnableAutoReconnect = true
	client.AddEventHandler(a.handleEvent)

	a.mu.Lock()
	a.baseCtx = ctx
	a.client = client
	a.sessionDB = db
	a.mu.Unlock()

	go a.connect(client)
	return nil
}

func (a *Adapter) Stop() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.client != nil {
		a.client.Disconnect()
	}
	if a.sessionDB != nil {
		err := a.sessionDB.Close()
		a.sessionDB = nil
		a.client = nil
		return err
	}
	a.client = nil
	return nil
}

func (a *Adapter) SendText(ctx context.Context, chatJID, text string) error {
	client := a.clientRef()
	if client == nil {
		return errors.New("client is not ready")
	}
	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("parse chat JID: %w", err)
	}
	canonicalChat, err := a.normalizeDirectChatJID(ctx, jid)
	if err != nil {
		return err
	}
	resp, err := client.SendMessage(ctx, jid, &waE2E.Message{
		Conversation: proto.String(text),
	})
	if err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	stored := domain.Message{
		ID:         resp.ID,
		ChatJID:    canonicalChat.String(),
		SenderJID:  a.selfJID().String(),
		SenderName: "You",
		Text:       strings.TrimSpace(text),
		FromMe:     true,
		Receipt:    domain.ReceiptStateSent,
		IsGroup:    canonicalChat.Server == types.GroupServer,
	}
	return a.persistSentMessage(ctx, canonicalChat, stored, resp.Timestamp.UTC())
}

func (a *Adapter) SendImage(ctx context.Context, chatJID, path, caption string) error {
	return a.SendMedia(ctx, chatJID, path, caption)
}

func (a *Adapter) SendMedia(ctx context.Context, chatJID, path, caption string) error {
	kind, mimeType, err := detectOutgoingMedia(path)
	if err != nil {
		return err
	}
	return a.sendUploadedMedia(ctx, chatJID, path, caption, kind, mimeType, 0)
}

func (a *Adapter) SendVoiceNote(ctx context.Context, chatJID, path string, duration time.Duration) error {
	return a.sendUploadedMedia(ctx, chatJID, path, "", domain.MediaKindVoice, "audio/ogg; codecs=opus", duration)
}

func (a *Adapter) DownloadMedia(ctx context.Context, msg domain.Message, downloadDir string) (string, error) {
	client := a.clientRef()
	if client == nil {
		return "", errors.New("client is not ready")
	}
	if msg.MediaKind == domain.MediaKindNone || msg.MediaDirectPath == "" || len(msg.MediaKey) == 0 {
		return "", errors.New("message does not contain downloadable media")
	}
	if err := os.MkdirAll(downloadDir, 0o700); err != nil {
		return "", fmt.Errorf("create download dir: %w", err)
	}

	targetPath := filepath.Join(downloadDir, downloadFileName(msg))
	file, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("create media file: %w", err)
	}
	defer file.Close()

	mediaType := whatsmeow.MediaDocument
	switch msg.MediaKind {
	case domain.MediaKindImage:
		mediaType = whatsmeow.MediaImage
	case domain.MediaKindVideo:
		mediaType = whatsmeow.MediaVideo
	case domain.MediaKindAudio, domain.MediaKindVoice:
		mediaType = whatsmeow.MediaAudio
	}
	if err := client.DownloadMediaWithPathToFile(
		ctx,
		msg.MediaDirectPath,
		msg.MediaFileEncSHA256,
		msg.MediaFileSHA256,
		msg.MediaKey,
		int(msg.MediaFileLength),
		mediaType,
		mediaDownloadMIMEType(mediaType),
		file,
	); err != nil {
		_ = os.Remove(targetPath)
		return "", fmt.Errorf("download media: %w", err)
	}
	if err := a.repo.MarkMessageDownloaded(ctx, msg.ChatJID, msg.ID, targetPath); err != nil {
		return "", err
	}
	return targetPath, nil
}

func (a *Adapter) persistSentMessage(ctx context.Context, chatJID types.JID, msg domain.Message, sentAt time.Time) error {
	if sentAt.IsZero() {
		sentAt = time.Now().UTC()
	}
	msg.Timestamp = sentAt
	if err := a.repo.RecordMessage(ctx, msg, false); err != nil {
		return err
	}
	if err := a.repo.UpsertChat(ctx, domain.ChatSummary{
		JID:                chatJID.String(),
		Title:              a.resolveChatTitle(ctx, chatJID, ""),
		LastMessageID:      msg.ID,
		LastMessagePreview: msg.Text,
		LastSenderName:     msg.SenderName,
		LastMessageAt:      msg.Timestamp,
		IsGroup:            msg.IsGroup,
	}); err != nil {
		return err
	}
	a.emit(domain.Event{Type: domain.EventChatUpdate, ChatJID: chatJID.String()})
	return nil
}

func (a *Adapter) RequestHistory(ctx context.Context, chatJID string, count int) error {
	client := a.clientRef()
	if client == nil {
		return errors.New("client is not ready")
	}
	oldest, err := a.repo.OldestMessage(ctx, chatJID)
	if err != nil {
		return err
	}
	chat, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("parse chat JID: %w", err)
	}
	if oldest == nil {
		summary, err := a.repo.GetChat(ctx, chatJID)
		if err != nil {
			return err
		}
		if summary != nil && summary.LastMessageID != "" && !summary.LastMessageAt.IsZero() {
			info := &types.MessageInfo{
				MessageSource: types.MessageSource{
					Chat:     chat,
					IsFromMe: strings.EqualFold(summary.LastSenderName, "You"),
					IsGroup:  summary.IsGroup,
				},
				ID:        summary.LastMessageID,
				Timestamp: summary.LastMessageAt,
			}
			_, err = client.SendPeerMessage(ctx, client.BuildHistorySyncRequest(info, count))
			if err != nil {
				return fmt.Errorf("request chat history sync: %w", err)
			}
			a.emit(domain.Event{Type: domain.EventStatus, Status: "Requested chat history from the primary device"})
			return nil
		}
		if err := a.requestRecentHistorySync(ctx, 30, uint32(max(count, 12))); err != nil {
			return fmt.Errorf("request recent history sync: %w", err)
		}
		a.emit(domain.Event{Type: domain.EventStatus, Status: "Requested recent chats from the primary device"})
		return nil
	}
	sender, err := types.ParseJID(oldest.SenderJID)
	if err != nil {
		return fmt.Errorf("parse sender JID: %w", err)
	}
	info := &types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:     chat,
			Sender:   sender,
			IsFromMe: oldest.FromMe,
			IsGroup:  oldest.IsGroup,
		},
		ID:        oldest.ID,
		Timestamp: oldest.Timestamp,
	}
	_, err = client.SendPeerMessage(ctx, client.BuildHistorySyncRequest(info, count))
	if err != nil {
		return fmt.Errorf("request history sync: %w", err)
	}
	a.emit(domain.Event{Type: domain.EventStatus, Status: "Requested older history from the primary device"})
	return nil
}

func (a *Adapter) sendUploadedMedia(ctx context.Context, chatJID, path, caption string, kind domain.MediaKind, mimeType string, duration time.Duration) error {
	client := a.clientRef()
	if client == nil {
		return errors.New("client is not ready")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read media: %w", err)
	}
	if len(data) == 0 {
		return errors.New("media file is empty")
	}

	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("parse chat JID: %w", err)
	}
	canonicalChat, err := a.normalizeDirectChatJID(ctx, jid)
	if err != nil {
		return err
	}
	upload, err := client.Upload(ctx, data, mediaUploadType(kind))
	if err != nil {
		return fmt.Errorf("upload media: %w", err)
	}

	message, preview, storedKind, err := buildOutgoingMediaMessage(path, caption, kind, mimeType, duration, upload, data)
	if err != nil {
		return err
	}

	resp, err := client.SendMessage(ctx, jid, message)
	if err != nil {
		return fmt.Errorf("send media: %w", err)
	}
	stored := domain.Message{
		ID:                 resp.ID,
		ChatJID:            canonicalChat.String(),
		SenderJID:          a.selfJID().String(),
		SenderName:         "You",
		Text:               preview,
		FromMe:             true,
		Receipt:            domain.ReceiptStateSent,
		IsGroup:            canonicalChat.Server == types.GroupServer,
		MediaKind:          storedKind,
		MediaMIME:          mimeType,
		MediaFileName:      filepath.Base(path),
		MediaDirectPath:    upload.DirectPath,
		MediaFileLength:    upload.FileLength,
		MediaSeconds:       uint32(duration.Round(time.Second) / time.Second),
		MediaKey:           upload.MediaKey,
		MediaFileSHA256:    upload.FileSHA256,
		MediaFileEncSHA256: upload.FileEncSHA256,
	}
	return a.persistSentMessage(ctx, canonicalChat, stored, resp.Timestamp.UTC())
}

func (a *Adapter) connect(client *whatsmeow.Client) {
	ctx := a.appContext()

	if client.Store.ID == nil {
		qrChan, err := client.GetQRChannel(ctx)
		if err != nil {
			a.emit(domain.Event{Type: domain.EventError, Err: fmt.Errorf("open QR channel: %w", err)})
			return
		}
		go a.forwardQR(qrChan)
		a.emit(domain.Event{Type: domain.EventStatus, Status: "Pairing required. Scan the QR code from your phone."})
	} else {
		a.emit(domain.Event{Type: domain.EventStatus, Status: "Connecting to WhatsApp..."})
	}

	if err := client.Connect(); err != nil {
		a.emit(domain.Event{Type: domain.EventError, Err: fmt.Errorf("connect to WhatsApp: %w", err)})
	}
}

func (a *Adapter) forwardQR(items <-chan whatsmeow.QRChannelItem) {
	for item := range items {
		switch item.Event {
		case whatsmeow.QRChannelEventCode:
			a.emit(domain.Event{Type: domain.EventQRCode, QRCode: item.Code, Status: "Scan the QR code with WhatsApp on your phone"})
		case whatsmeow.QRChannelEventError:
			a.emit(domain.Event{Type: domain.EventError, Err: item.Error})
		default:
			a.emit(domain.Event{Type: domain.EventStatus, Status: item.Event})
		}
	}
}

func (a *Adapter) handleEvent(raw any) {
	switch evt := raw.(type) {
	case *waevents.Connected:
		ctx, cancel := a.contextWithTimeout(30 * time.Second)
		defer cancel()
		a.emit(domain.Event{Type: domain.EventStatus, Status: "connected"})
		if err := a.normalizeDirectChats(ctx); err != nil {
			a.emit(domain.Event{Type: domain.EventError, Err: err})
		}
		if err := a.syncContacts(ctx); err != nil {
			a.emit(domain.Event{Type: domain.EventError, Err: err})
		}
		a.emit(domain.Event{Type: domain.EventChatListUpdate})
		a.scheduleSessionContactSync()
		go a.bootstrapRecentHistory()
	case *waevents.Disconnected:
		a.emit(domain.Event{Type: domain.EventStatus, Status: "disconnected"})
	case *waevents.StreamReplaced:
		a.emit(domain.Event{Type: domain.EventError, Err: errors.New("session was replaced by another client")})
	case *waevents.LoggedOut:
		a.emit(domain.Event{Type: domain.EventError, Err: fmt.Errorf("logged out: %s", evt.Reason.String())})
	case *waevents.TemporaryBan:
		a.emit(domain.Event{Type: domain.EventError, Err: fmt.Errorf("temporary ban: %s", evt.String())})
	case *waevents.ConnectFailure:
		a.emit(domain.Event{Type: domain.EventError, Err: fmt.Errorf("connect failure: %s", evt.Reason.String())})
	case *waevents.PushName:
		ctx, cancel := a.contextWithTimeout(20 * time.Second)
		defer cancel()
		_ = a.repo.UpsertContact(ctx, domain.Contact{
			JID:      evt.JID.ToNonAD().String(),
			PushName: evt.NewPushName,
		})
		a.emit(domain.Event{Type: domain.EventChatListUpdate})
	case *waevents.Contact:
		ctx, cancel := a.contextWithTimeout(30 * time.Second)
		defer cancel()
		_ = a.syncContacts(ctx)
		a.emit(domain.Event{Type: domain.EventChatListUpdate})
	case *waevents.HistorySync:
		ctx, cancel := a.contextWithTimeout(2 * time.Minute)
		defer cancel()
		if err := a.handleHistorySync(ctx, evt.Data); err != nil {
			a.emit(domain.Event{Type: domain.EventError, Err: err})
		}
	case *waevents.Message:
		ctx, cancel := a.contextWithTimeout(30 * time.Second)
		defer cancel()
		if err := a.handleMessage(ctx, evt.UnwrapRaw()); err != nil {
			a.emit(domain.Event{Type: domain.EventError, Err: err})
		}
	case *waevents.Receipt:
		ctx, cancel := a.contextWithTimeout(20 * time.Second)
		defer cancel()
		if err := a.handleReceipt(ctx, evt); err != nil {
			a.emit(domain.Event{Type: domain.EventError, Err: err})
		}
	}
}

func (a *Adapter) handleHistorySync(ctx context.Context, sync *waHistorySync.HistorySync) error {
	for _, pushName := range sync.GetPushnames() {
		if pushName == nil || pushName.GetPushname() == "" || pushName.GetPushname() == "-" {
			continue
		}
		jid, err := types.ParseJID(pushName.GetID())
		if err != nil {
			continue
		}
		if err := a.repo.UpsertContact(ctx, domain.Contact{
			JID:      jid.ToNonAD().String(),
			PushName: strings.TrimSpace(pushName.GetPushname()),
		}); err != nil {
			return err
		}
	}

	for _, conversation := range sync.GetConversations() {
		chatJID, err := types.ParseJID(conversation.GetID())
		if err != nil || chatJID.IsEmpty() {
			continue
		}
		if ignoredChatJID(chatJID.ToNonAD().String()) {
			continue
		}

		canonicalChat, err := a.normalizeDirectChatJID(ctx, chatJID)
		if err != nil {
			return err
		}

		chat := domain.ChatSummary{
			JID:         canonicalChat.String(),
			Title:       a.resolveChatTitle(ctx, canonicalChat, firstNonEmpty(conversation.GetDisplayName(), conversation.GetName())),
			UnreadCount: int(conversation.GetUnreadCount()),
			IsGroup:     canonicalChat.Server == types.GroupServer,
		}

		var latest *domain.Message
		for _, item := range conversation.GetMessages() {
			msg, ok := a.historyMessageToDomain(ctx, canonicalChat, item)
			if !ok {
				continue
			}
			if err := a.repo.RecordMessage(ctx, msg, false); err != nil {
				return err
			}
			localCopy := msg
			if latest == nil || localCopy.Timestamp.After(latest.Timestamp) {
				latest = &localCopy
			}
		}

		if storedChat, err := a.repo.GetChat(ctx, chat.JID); err == nil && storedChat != nil {
			chat.LastMessageID = storedChat.LastMessageID
			chat.LastMessagePreview = storedChat.LastMessagePreview
			chat.LastSenderName = storedChat.LastSenderName
			chat.LastMessageAt = storedChat.LastMessageAt
		} else if latest != nil {
			chat.LastMessageID = latest.ID
			chat.LastMessagePreview = latest.Text
			chat.LastSenderName = latest.SenderName
			chat.LastMessageAt = latest.Timestamp
		}
		if chat.LastMessageAt.IsZero() && conversation.GetConversationTimestamp() > 0 {
			chat.LastMessageAt = unixTimeFromUint64(conversation.GetConversationTimestamp())
		}

		if err := a.repo.UpsertChat(ctx, chat); err != nil {
			return err
		}
	}

	a.scheduleSessionContactSync()
	a.emit(domain.Event{Type: domain.EventChatListUpdate, Status: "History sync updated"})
	return nil
}

func (a *Adapter) handleMessage(ctx context.Context, evt *waevents.Message) error {
	if evt == nil || evt.Message == nil {
		return nil
	}

	chatJID, err := a.normalizeDirectChatJID(ctx, evt.Info.Chat.ToNonAD())
	if err != nil {
		return err
	}
	if ignoredChatJID(chatJID.String()) {
		return nil
	}
	msg := domain.Message{
		ID:         evt.Info.ID,
		ChatJID:    chatJID.String(),
		SenderJID:  evt.Info.Sender.ToNonAD().String(),
		SenderName: a.resolveSenderName(ctx, evt.Info.Sender.ToNonAD(), evt.Info.PushName),
		Text:       extractText(evt.Message),
		Timestamp:  evt.Info.Timestamp.UTC(),
		FromMe:     evt.Info.IsFromMe,
		Receipt:    receiptForIncoming(evt.Info.IsFromMe),
		IsGroup:    evt.Info.IsGroup,
	}
	if msg.FromMe {
		msg.SenderJID = a.selfJID().String()
		msg.SenderName = "You"
	}
	applyMediaMetadata(&msg, evt.Message)

	if !evt.Info.IsGroup && evt.Info.PushName != "" {
		_ = a.repo.UpsertContact(ctx, domain.Contact{
			JID:      evt.Info.Sender.ToNonAD().String(),
			PushName: evt.Info.PushName,
		})
	}

	if err := a.repo.RecordMessage(ctx, msg, !msg.FromMe); err != nil {
		return err
	}
	unreadCount := 0
	if current, err := a.repo.GetChat(ctx, msg.ChatJID); err == nil && current != nil {
		unreadCount = current.UnreadCount
	}
	if err := a.repo.UpsertChat(ctx, domain.ChatSummary{
		JID:                msg.ChatJID,
		Title:              a.resolveChatTitle(ctx, chatJID, evt.Info.PushName),
		LastMessageID:      msg.ID,
		LastMessagePreview: msg.Text,
		LastSenderName:     msg.SenderName,
		LastMessageAt:      msg.Timestamp,
		UnreadCount:        unreadCount,
		IsGroup:            msg.IsGroup,
	}); err != nil {
		return err
	}

	a.emit(domain.Event{Type: domain.EventChatUpdate, ChatJID: msg.ChatJID, Notify: !msg.FromMe})
	return nil
}

func ignoredChatJID(jid string) bool {
	return jid == "status@broadcast"
}

func (a *Adapter) handleReceipt(ctx context.Context, evt *waevents.Receipt) error {
	state := domain.ReceiptStateUnknown
	switch evt.Type {
	case types.ReceiptTypeDelivered:
		state = domain.ReceiptStateDelivered
	case types.ReceiptTypeRead, types.ReceiptTypeReadSelf:
		state = domain.ReceiptStateRead
	case types.ReceiptTypeSender:
		state = domain.ReceiptStateSent
	}
	if state == domain.ReceiptStateUnknown {
		return nil
	}
	if err := a.repo.UpdateReceipts(ctx, evt.Chat.ToNonAD().String(), evt.MessageIDs, state); err != nil {
		return err
	}
	a.emit(domain.Event{Type: domain.EventChatUpdate, ChatJID: evt.Chat.ToNonAD().String()})
	return nil
}

func (a *Adapter) syncContacts(ctx context.Context) error {
	client := a.clientRef()
	if client != nil && client.Store != nil && client.Store.Contacts != nil {
		contacts, err := client.Store.Contacts.GetAllContacts(ctx)
		if err != nil {
			return fmt.Errorf("sync contacts from store: %w", err)
		}
		for jid, info := range contacts {
			if err := a.repo.UpsertContact(ctx, domain.Contact{
				JID:          jid.ToNonAD().String(),
				DisplayName:  firstNonEmpty(info.FullName, info.FirstName),
				PushName:     info.PushName,
				BusinessName: info.BusinessName,
			}); err != nil {
				return err
			}
		}
	}
	if err := a.syncRecentChatContacts(ctx); err != nil {
		if isSQLiteBusy(err) {
			return nil
		}
		return err
	}
	return nil
}

func (a *Adapter) scheduleSessionContactSync() {
	a.contactSyncMu.Lock()
	if a.contactSyncRunning {
		a.contactSyncMu.Unlock()
		return
	}
	a.contactSyncRunning = true
	a.contactSyncMu.Unlock()

	go func() {
		defer func() {
			a.contactSyncMu.Lock()
			a.contactSyncRunning = false
			a.contactSyncMu.Unlock()
		}()

		delays := []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second}
		for _, delay := range delays {
			timer := time.NewTimer(delay)
			select {
			case <-a.appContext().Done():
				timer.Stop()
				return
			case <-timer.C:
			}

			ctx, cancel := context.WithTimeout(a.appContext(), 20*time.Second)
			err := a.syncRecentChatContacts(ctx)
			cancel()
			if err == nil {
				a.emit(domain.Event{Type: domain.EventChatListUpdate})
				return
			}
			if !isSQLiteBusy(err) {
				if a.logger != nil {
					a.logger.Warn("session contact sync failed", "error", err)
				}
				return
			}
		}
	}()
}

func (a *Adapter) syncRecentChatContacts(ctx context.Context) error {
	targets := make(map[string]struct{}, 32)
	chats, err := a.repo.ListChats(ctx, "", 8)
	if err != nil {
		return fmt.Errorf("list recent chats for contact sync: %w", err)
	}
	for _, chat := range chats {
		targets[chat.JID] = struct{}{}
		messages, err := a.repo.ListMessages(ctx, chat.JID, 20)
		if err != nil {
			return fmt.Errorf("list messages for contact sync %s: %w", chat.JID, err)
		}
		for _, msg := range messages {
			if msg.SenderJID != "" {
				targets[msg.SenderJID] = struct{}{}
			}
		}
	}
	if len(targets) == 0 {
		return nil
	}
	return a.syncContactsFromSessionDB(ctx, targets)
}

func (a *Adapter) historyMessageToDomain(ctx context.Context, chatJID types.JID, item *waHistorySync.HistorySyncMsg) (domain.Message, bool) {
	if item == nil || item.GetMessage() == nil || item.GetMessage().GetMessage() == nil {
		return domain.Message{}, false
	}
	webMsg := item.GetMessage()
	client := a.clientRef()
	if client != nil {
		if parsed, err := client.ParseWebMessage(chatJID, webMsg); err == nil && parsed != nil {
			if !parsed.Info.IsFromMe && parsed.Info.PushName != "" {
				_ = a.repo.UpsertContact(ctx, domain.Contact{
					JID:      parsed.Info.Sender.ToNonAD().String(),
					PushName: parsed.Info.PushName,
				})
			}
			sender := parsed.Info.Sender.ToNonAD()
			msg := domain.Message{
				ID:         parsed.Info.ID,
				ChatJID:    parsed.Info.Chat.ToNonAD().String(),
				SenderJID:  sender.String(),
				SenderName: a.resolveSenderName(ctx, sender, parsed.Info.PushName),
				Text:       extractText(parsed.Message),
				Timestamp:  parsed.Info.Timestamp.UTC(),
				FromMe:     parsed.Info.IsFromMe,
				Receipt:    receiptForIncoming(parsed.Info.IsFromMe),
				IsGroup:    parsed.Info.IsGroup,
			}
			if msg.FromMe {
				msg.SenderName = "You"
			}
			applyMediaMetadata(&msg, parsed.Message)
			return msg, msg.ID != ""
		}
	}
	key := webMsg.GetKey()
	message := unwrapHistoryMessage(webMsg.GetMessage())
	var sender types.JID
	switch {
	case key.GetFromMe():
		sender = a.selfJID()
	case chatJID.Server == types.GroupServer && key.GetParticipant() != "":
		sender, _ = types.ParseJID(key.GetParticipant())
	default:
		sender = chatJID
	}
	msg := domain.Message{
		ID:         key.GetID(),
		ChatJID:    chatJID.String(),
		SenderJID:  sender.ToNonAD().String(),
		SenderName: a.resolveSenderName(ctx, sender.ToNonAD(), ""),
		Text:       extractText(message),
		Timestamp:  unixTimeFromUint64(webMsg.GetMessageTimestamp()),
		FromMe:     key.GetFromMe(),
		Receipt:    receiptForIncoming(key.GetFromMe()),
		IsGroup:    chatJID.Server == types.GroupServer,
	}
	if msg.FromMe {
		msg.SenderName = "You"
	}
	applyMediaMetadata(&msg, message)
	return msg, msg.ID != ""
}

func (a *Adapter) syncContactsFromSessionDB(ctx context.Context, targetJIDs map[string]struct{}) error {
	a.mu.RLock()
	db := a.sessionDB
	a.mu.RUnlock()
	if db == nil {
		return nil
	}
	if len(targetJIDs) == 0 {
		return nil
	}

	type contactRow struct {
		displayName  string
		pushName     string
		businessName string
	}
	contacts := make(map[string]contactRow, len(targetJIDs))
	lookupJIDs := make([]string, 0, len(targetJIDs))
	for jid := range targetJIDs {
		lookupJIDs = append(lookupJIDs, jid)
	}
	query, args := buildSessionContactLookupQuery(lookupJIDs)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("query session contacts: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var jid, fullName, firstName, pushName, businessName string
		if err := rows.Scan(&jid, &fullName, &firstName, &pushName, &businessName); err != nil {
			return fmt.Errorf("scan session contact: %w", err)
		}
		contacts[jid] = contactRow{
			displayName:  firstNonEmpty(fullName, firstName),
			pushName:     strings.TrimSpace(pushName),
			businessName: strings.TrimSpace(businessName),
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate session contacts: %w", err)
	}

	lidRows, err := db.QueryContext(ctx, `SELECT lid, pn FROM whatsmeow_lid_map`)
	if err != nil {
		return fmt.Errorf("query lid mappings: %w", err)
	}
	defer lidRows.Close()
	for lidRows.Next() {
		var lidUser, pnUser string
		if err := lidRows.Scan(&lidUser, &pnUser); err != nil {
			return fmt.Errorf("scan lid mapping: %w", err)
		}
		pnJID := pnUser + "@s.whatsapp.net"
		lidJID := lidUser + "@lid"
		if _, wanted := targetJIDs[lidJID]; !wanted {
			if _, wanted = targetJIDs[pnJID]; !wanted {
				continue
			}
		}
		if row, ok := contacts[pnJID]; ok {
			if _, exists := contacts[lidJID]; !exists {
				contacts[lidJID] = row
			}
		}
	}
	if err := lidRows.Err(); err != nil {
		return fmt.Errorf("iterate lid mappings: %w", err)
	}

	for jid, row := range contacts {
		if err := a.repo.UpsertContact(ctx, domain.Contact{
			JID:          jid,
			DisplayName:  row.displayName,
			PushName:     row.pushName,
			BusinessName: row.businessName,
		}); err != nil {
			return fmt.Errorf("upsert session contact %s: %w", jid, err)
		}
	}
	return nil
}

func buildSessionContactLookupQuery(jids []string) (string, []any) {
	placeholders := make([]string, 0, len(jids))
	args := make([]any, 0, len(jids))
	for _, jid := range jids {
		placeholders = append(placeholders, "?")
		args = append(args, jid)
	}
	return fmt.Sprintf(`
SELECT their_jid, COALESCE(full_name, ''), COALESCE(first_name, ''), COALESCE(push_name, ''), COALESCE(business_name, '')
FROM whatsmeow_contacts
WHERE their_jid IN (%s)
`, strings.Join(placeholders, ",")), args
}

func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "SQLITE_BUSY") || strings.Contains(err.Error(), "database is locked")
}

func unwrapHistoryMessage(message *waE2E.Message) *waE2E.Message {
	if message == nil {
		return nil
	}
	if message.GetDeviceSentMessage().GetMessage() != nil {
		message = message.GetDeviceSentMessage().GetMessage()
	}
	if message.GetBotInvokeMessage().GetMessage() != nil {
		message = message.GetBotInvokeMessage().GetMessage()
	}
	if message.GetEphemeralMessage().GetMessage() != nil {
		message = message.GetEphemeralMessage().GetMessage()
	}
	if message.GetViewOnceMessage().GetMessage() != nil {
		message = message.GetViewOnceMessage().GetMessage()
	}
	if message.GetViewOnceMessageV2().GetMessage() != nil {
		message = message.GetViewOnceMessageV2().GetMessage()
	}
	if message.GetViewOnceMessageV2Extension().GetMessage() != nil {
		message = message.GetViewOnceMessageV2Extension().GetMessage()
	}
	if message.GetLottieStickerMessage().GetMessage() != nil {
		message = message.GetLottieStickerMessage().GetMessage()
	}
	if message.GetDocumentWithCaptionMessage().GetMessage() != nil {
		message = message.GetDocumentWithCaptionMessage().GetMessage()
	}
	if message.GetEditedMessage().GetMessage() != nil {
		message = message.GetEditedMessage().GetMessage()
	}
	return message
}

func (a *Adapter) bootstrapRecentHistory() {
	ctx, cancel := context.WithTimeout(a.appContext(), 15*time.Second)
	defer cancel()
	needsSync, err := a.needsInitialHistorySync(ctx)
	if err != nil {
		a.emit(domain.Event{Type: domain.EventError, Err: err})
		return
	}
	if !needsSync {
		return
	}

	a.bootstrapMu.Lock()
	if a.initialHistoryRequested {
		a.bootstrapMu.Unlock()
		return
	}
	a.initialHistoryRequested = true
	a.bootstrapMu.Unlock()

	a.emit(domain.Event{Type: domain.EventStatus, Status: "Waiting for recent chats from your phone..."})
	timer := time.NewTimer(12 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}

	recheckCtx, recheckCancel := context.WithTimeout(a.appContext(), 15*time.Second)
	stillEmpty, err := a.needsInitialHistorySync(recheckCtx)
	recheckCancel()
	if err != nil {
		a.emit(domain.Event{Type: domain.EventError, Err: err})
		return
	}
	if !stillEmpty {
		return
	}

	requestCtx, requestCancel := context.WithTimeout(a.appContext(), 45*time.Second)
	defer requestCancel()
	a.emit(domain.Event{Type: domain.EventStatus, Status: "Requesting recent chats from your phone..."})
	if err := a.requestRecentHistorySync(requestCtx, 30, 12); err != nil {
		if strings.Contains(err.Error(), "no signal session established") || strings.Contains(err.Error(), "failed to encrypt peer message") {
			a.emit(domain.Event{Type: domain.EventStatus, Status: "Phone sync is still warming up. Waiting for passive history sync..."})
			return
		}
		a.emit(domain.Event{Type: domain.EventError, Err: err})
		return
	}
}

func (a *Adapter) needsInitialHistorySync(ctx context.Context) (bool, error) {
	chats, err := a.repo.ListChats(ctx, "", 1)
	if err != nil {
		return false, fmt.Errorf("check cached chats: %w", err)
	}
	return len(chats) == 0, nil
}

func (a *Adapter) requestRecentHistorySync(ctx context.Context, days, maxMessagesPerChat uint32) error {
	client := a.clientRef()
	if client == nil {
		return errors.New("client is not ready")
	}

	since := time.Now().UTC().AddDate(0, 0, -int(days))
	requestID := client.GenerateMessageID()
	_, err := client.SendPeerMessage(ctx, &waE2E.Message{
		ProtocolMessage: &waE2E.ProtocolMessage{
			Type: waE2E.ProtocolMessage_PEER_DATA_OPERATION_REQUEST_MESSAGE.Enum(),
			PeerDataOperationRequestMessage: &waE2E.PeerDataOperationRequestMessage{
				PeerDataOperationRequestType: waE2E.PeerDataOperationRequestType_FULL_HISTORY_SYNC_ON_DEMAND.Enum(),
				FullHistorySyncOnDemandRequest: &waE2E.PeerDataOperationRequestMessage_FullHistorySyncOnDemandRequest{
					RequestMetadata: &waE2E.FullHistorySyncOnDemandRequestMetadata{
						RequestID: proto.String(string(requestID)),
					},
					HistorySyncConfig: &waCompanionReg.DeviceProps_HistorySyncConfig{
						RecentSyncDaysLimit:           proto.Uint32(days),
						InitialSyncMaxMessagesPerChat: proto.Uint32(maxMessagesPerChat),
						OnDemandReady:                 proto.Bool(true),
						CompleteOnDemandReady:         proto.Bool(true),
						SupportMessageAssociation:     proto.Bool(true),
						SupportGroupHistory:           proto.Bool(true),
					},
					FullHistorySyncOnDemandConfig: &waE2E.FullHistorySyncOnDemandConfig{
						HistoryFromTimestamp: proto.Uint64(uint64(since.Unix())),
						HistoryDurationDays:  proto.Uint32(days),
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("request recent history sync: %w", err)
	}
	return nil
}

func (a *Adapter) normalizeDirectChats(ctx context.Context) error {
	a.mu.RLock()
	db := a.sessionDB
	a.mu.RUnlock()
	if db == nil {
		return nil
	}
	rows, err := db.QueryContext(ctx, `SELECT lid, pn FROM whatsmeow_lid_map`)
	if err != nil {
		return fmt.Errorf("query lid mappings for chat normalization: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var lidUser, pnUser string
		if err := rows.Scan(&lidUser, &pnUser); err != nil {
			return fmt.Errorf("scan lid mapping for chat normalization: %w", err)
		}
		fromJID := lidUser + "@lid"
		toJID := pnUser + "@s.whatsapp.net"
		if err := a.repo.MergeChatJIDs(ctx, fromJID, toJID); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate lid mappings for chat normalization: %w", err)
	}
	return nil
}

func (a *Adapter) normalizeDirectChatJID(ctx context.Context, jid types.JID) (types.JID, error) {
	jid = jid.ToNonAD()
	if jid.Server != types.HiddenUserServer {
		return jid, nil
	}

	a.mu.RLock()
	db := a.sessionDB
	a.mu.RUnlock()
	if db == nil {
		return jid, nil
	}

	var pnUser string
	err := db.QueryRowContext(ctx, `SELECT pn FROM whatsmeow_lid_map WHERE lid = ?`, jid.User).Scan(&pnUser)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return jid, nil
		}
		return types.EmptyJID, fmt.Errorf("lookup lid mapping for %s: %w", jid, err)
	}
	if strings.TrimSpace(pnUser) == "" {
		return jid, nil
	}
	return types.NewJID(pnUser, types.DefaultUserServer), nil
}

func (a *Adapter) contextWithTimeout(timeout time.Duration) (context.Context, context.CancelFunc) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	base := a.baseCtx
	if base == nil {
		base = context.Background()
	}
	return context.WithTimeout(base, timeout)
}

func (a *Adapter) appContext() context.Context {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.baseCtx != nil {
		return a.baseCtx
	}
	return context.Background()
}

func (a *Adapter) clientRef() *whatsmeow.Client {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.client
}

func (a *Adapter) selfJID() types.JID {
	client := a.clientRef()
	if client == nil || client.Store == nil || client.Store.ID == nil {
		return types.EmptyJID
	}
	return client.Store.ID.ToNonAD()
}

func (a *Adapter) resolveChatTitle(ctx context.Context, chatJID types.JID, fallback string) string {
	if existing, err := a.repo.GetChat(ctx, chatJID.String()); err == nil && existing != nil && strings.TrimSpace(existing.Title) != "" {
		return existing.Title
	}
	if chatJID.Server != types.GroupServer {
		if name, err := a.repo.ContactName(ctx, chatJID.String()); err == nil && name != "" {
			return name
		}
	}
	if strings.TrimSpace(fallback) != "" {
		return strings.TrimSpace(fallback)
	}
	if chatJID.User != "" {
		return chatJID.User
	}
	return chatJID.String()
}

func (a *Adapter) resolveSenderName(ctx context.Context, sender types.JID, fallback string) string {
	if sender.IsEmpty() {
		return firstNonEmpty(strings.TrimSpace(fallback), "Unknown")
	}
	if name, err := a.repo.ContactName(ctx, sender.String()); err == nil && name != "" {
		return name
	}
	if strings.TrimSpace(fallback) != "" {
		return strings.TrimSpace(fallback)
	}
	if sender.User != "" {
		return sender.User
	}
	return sender.String()
}

func (a *Adapter) emit(event domain.Event) {
	select {
	case a.events <- event:
	default:
		if a.logger != nil {
			a.logger.Warn("dropping transport event", "type", event.Type, "status", event.Status)
		}
	}
}

func extractText(message *waE2E.Message) string {
	switch {
	case message == nil:
		return ""
	case strings.TrimSpace(message.GetConversation()) != "":
		return strings.TrimSpace(message.GetConversation())
	case strings.TrimSpace(message.GetExtendedTextMessage().GetText()) != "":
		return strings.TrimSpace(message.GetExtendedTextMessage().GetText())
	case strings.TrimSpace(message.GetImageMessage().GetCaption()) != "":
		return strings.TrimSpace(message.GetImageMessage().GetCaption())
	case strings.TrimSpace(message.GetVideoMessage().GetCaption()) != "":
		return strings.TrimSpace(message.GetVideoMessage().GetCaption())
	case strings.TrimSpace(message.GetDocumentMessage().GetCaption()) != "":
		return strings.TrimSpace(message.GetDocumentMessage().GetCaption())
	case message.GetImageMessage() != nil:
		return "[image]"
	case message.GetVideoMessage() != nil:
		return "[video]"
	case message.GetDocumentMessage() != nil:
		if name := strings.TrimSpace(message.GetDocumentMessage().GetFileName()); name != "" {
			return "[document] " + name
		}
		return "[document]"
	case message.GetStickerMessage() != nil:
		return "[sticker]"
	case message.GetAudioMessage() != nil:
		if message.GetAudioMessage().GetPTT() {
			return "[voice note]"
		}
		if name := strings.TrimSpace(message.GetAudioMessage().GetMimetype()); name != "" {
			return "[audio]"
		}
		return "[audio]"
	case message.GetReactionMessage() != nil:
		return "[reaction]"
	default:
		return "[unsupported message]"
	}
}

func applyMediaMetadata(msg *domain.Message, message *waE2E.Message) {
	if msg == nil || message == nil {
		return
	}
	switch {
	case message.GetImageMessage() != nil:
		imageMsg := message.GetImageMessage()
		msg.MediaKind = domain.MediaKindImage
		msg.MediaMIME = imageMsg.GetMimetype()
		msg.MediaDirectPath = imageMsg.GetDirectPath()
		msg.MediaFileLength = imageMsg.GetFileLength()
		msg.MediaKey = cloneBytes(imageMsg.GetMediaKey())
		msg.MediaFileSHA256 = cloneBytes(imageMsg.GetFileSHA256())
		msg.MediaFileEncSHA256 = cloneBytes(imageMsg.GetFileEncSHA256())
	case message.GetVideoMessage() != nil:
		videoMsg := message.GetVideoMessage()
		msg.MediaKind = domain.MediaKindVideo
		msg.MediaMIME = videoMsg.GetMimetype()
		msg.MediaDirectPath = videoMsg.GetDirectPath()
		msg.MediaFileLength = videoMsg.GetFileLength()
		msg.MediaKey = cloneBytes(videoMsg.GetMediaKey())
		msg.MediaFileSHA256 = cloneBytes(videoMsg.GetFileSHA256())
		msg.MediaFileEncSHA256 = cloneBytes(videoMsg.GetFileEncSHA256())
	case message.GetDocumentMessage() != nil:
		docMsg := message.GetDocumentMessage()
		msg.MediaKind = domain.MediaKindDocument
		msg.MediaMIME = docMsg.GetMimetype()
		msg.MediaFileName = docMsg.GetFileName()
		msg.MediaDirectPath = docMsg.GetDirectPath()
		msg.MediaFileLength = docMsg.GetFileLength()
		msg.MediaKey = cloneBytes(docMsg.GetMediaKey())
		msg.MediaFileSHA256 = cloneBytes(docMsg.GetFileSHA256())
		msg.MediaFileEncSHA256 = cloneBytes(docMsg.GetFileEncSHA256())
	case message.GetAudioMessage() != nil:
		audioMsg := message.GetAudioMessage()
		msg.MediaKind = domain.MediaKindAudio
		if audioMsg.GetPTT() {
			msg.MediaKind = domain.MediaKindVoice
		}
		msg.MediaMIME = audioMsg.GetMimetype()
		msg.MediaDirectPath = audioMsg.GetDirectPath()
		msg.MediaFileLength = audioMsg.GetFileLength()
		msg.MediaSeconds = audioMsg.GetSeconds()
		msg.MediaKey = cloneBytes(audioMsg.GetMediaKey())
		msg.MediaFileSHA256 = cloneBytes(audioMsg.GetFileSHA256())
		msg.MediaFileEncSHA256 = cloneBytes(audioMsg.GetFileEncSHA256())
	case message.GetStickerMessage() != nil:
		stickerMsg := message.GetStickerMessage()
		msg.MediaKind = domain.MediaKindSticker
		msg.MediaMIME = stickerMsg.GetMimetype()
		msg.MediaDirectPath = stickerMsg.GetDirectPath()
		msg.MediaFileLength = stickerMsg.GetFileLength()
		msg.MediaKey = cloneBytes(stickerMsg.GetMediaKey())
		msg.MediaFileSHA256 = cloneBytes(stickerMsg.GetFileSHA256())
		msg.MediaFileEncSHA256 = cloneBytes(stickerMsg.GetFileEncSHA256())
	}
}

func detectOutgoingMedia(path string) (domain.MediaKind, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return domain.MediaKindNone, "", fmt.Errorf("read media file: %w", err)
	}
	if len(data) == 0 {
		return domain.MediaKindNone, "", errors.New("media file is empty")
	}
	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if mimeType == "" {
		sample := data
		if len(sample) > 512 {
			sample = sample[:512]
		}
		mimeType = http.DetectContentType(sample)
	}
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		return domain.MediaKindImage, mimeType, nil
	case strings.HasPrefix(mimeType, "video/"):
		return domain.MediaKindVideo, mimeType, nil
	case strings.HasPrefix(mimeType, "audio/"):
		return domain.MediaKindAudio, mimeType, nil
	default:
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		return domain.MediaKindDocument, mimeType, nil
	}
}

func mediaUploadType(kind domain.MediaKind) whatsmeow.MediaType {
	switch kind {
	case domain.MediaKindImage, domain.MediaKindSticker:
		return whatsmeow.MediaImage
	case domain.MediaKindVideo:
		return whatsmeow.MediaVideo
	case domain.MediaKindAudio, domain.MediaKindVoice:
		return whatsmeow.MediaAudio
	default:
		return whatsmeow.MediaDocument
	}
}

func mediaDownloadMIMEType(mediaType whatsmeow.MediaType) string {
	switch mediaType {
	case whatsmeow.MediaImage:
		return "image"
	case whatsmeow.MediaVideo:
		return "video"
	case whatsmeow.MediaAudio:
		return "audio"
	default:
		return "document"
	}
}

func buildOutgoingMediaMessage(path, caption string, kind domain.MediaKind, mimeType string, duration time.Duration, upload whatsmeow.UploadResponse, data []byte) (*waE2E.Message, string, domain.MediaKind, error) {
	caption = strings.TrimSpace(caption)
	base := filepath.Base(path)
	switch kind {
	case domain.MediaKindImage:
		cfg, _, decodeErr := image.DecodeConfig(bytes.NewReader(data))
		if decodeErr != nil {
			cfg = image.Config{}
		}
		imageMsg := &waE2E.ImageMessage{
			Mimetype:      proto.String(mimeType),
			Caption:       proto.String(caption),
			URL:           proto.String(upload.URL),
			DirectPath:    proto.String(upload.DirectPath),
			MediaKey:      upload.MediaKey,
			FileEncSHA256: upload.FileEncSHA256,
			FileSHA256:    upload.FileSHA256,
			FileLength:    proto.Uint64(upload.FileLength),
		}
		if cfg.Width > 0 {
			imageMsg.Width = proto.Uint32(uint32(cfg.Width))
		}
		if cfg.Height > 0 {
			imageMsg.Height = proto.Uint32(uint32(cfg.Height))
		}
		return &waE2E.Message{ImageMessage: imageMsg}, mediaPreview(domain.MediaKindImage, base, caption), domain.MediaKindImage, nil
	case domain.MediaKindVideo:
		videoMsg := &waE2E.VideoMessage{
			Mimetype:      proto.String(mimeType),
			Caption:       proto.String(caption),
			URL:           proto.String(upload.URL),
			DirectPath:    proto.String(upload.DirectPath),
			MediaKey:      upload.MediaKey,
			FileEncSHA256: upload.FileEncSHA256,
			FileSHA256:    upload.FileSHA256,
			FileLength:    proto.Uint64(upload.FileLength),
		}
		return &waE2E.Message{VideoMessage: videoMsg}, mediaPreview(domain.MediaKindVideo, base, caption), domain.MediaKindVideo, nil
	case domain.MediaKindVoice:
		seconds := uint32(duration.Round(time.Second) / time.Second)
		audioMsg := &waE2E.AudioMessage{
			Mimetype:      proto.String(mimeType),
			URL:           proto.String(upload.URL),
			DirectPath:    proto.String(upload.DirectPath),
			MediaKey:      upload.MediaKey,
			FileEncSHA256: upload.FileEncSHA256,
			FileSHA256:    upload.FileSHA256,
			FileLength:    proto.Uint64(upload.FileLength),
			Seconds:       proto.Uint32(seconds),
			PTT:           proto.Bool(true),
		}
		return &waE2E.Message{AudioMessage: audioMsg}, mediaPreview(domain.MediaKindVoice, base, ""), domain.MediaKindVoice, nil
	case domain.MediaKindAudio:
		audioMsg := &waE2E.AudioMessage{
			Mimetype:      proto.String(mimeType),
			URL:           proto.String(upload.URL),
			DirectPath:    proto.String(upload.DirectPath),
			MediaKey:      upload.MediaKey,
			FileEncSHA256: upload.FileEncSHA256,
			FileSHA256:    upload.FileSHA256,
			FileLength:    proto.Uint64(upload.FileLength),
			PTT:           proto.Bool(false),
		}
		return &waE2E.Message{AudioMessage: audioMsg}, mediaPreview(domain.MediaKindAudio, base, caption), domain.MediaKindAudio, nil
	default:
		documentMsg := &waE2E.DocumentMessage{
			Mimetype:      proto.String(mimeType),
			Title:         proto.String(base),
			FileName:      proto.String(base),
			Caption:       proto.String(caption),
			URL:           proto.String(upload.URL),
			DirectPath:    proto.String(upload.DirectPath),
			MediaKey:      upload.MediaKey,
			FileEncSHA256: upload.FileEncSHA256,
			FileSHA256:    upload.FileSHA256,
			FileLength:    proto.Uint64(upload.FileLength),
		}
		return &waE2E.Message{DocumentMessage: documentMsg}, mediaPreview(domain.MediaKindDocument, base, caption), domain.MediaKindDocument, nil
	}
}

func mediaPreview(kind domain.MediaKind, fileName, caption string) string {
	label := "[" + string(kind) + "]"
	if kind == domain.MediaKindVoice {
		label = "[voice note]"
	}
	preview := strings.TrimSpace(label + " " + fileName)
	if caption != "" {
		preview += " — " + caption
	}
	return preview
}

func downloadFileName(msg domain.Message) string {
	name := strings.TrimSpace(msg.MediaFileName)
	if name == "" {
		name = msg.ID + mediaExtension(msg.MediaMIME, msg.MediaKind)
	}
	return name
}

func mediaExtension(mimeType string, kind domain.MediaKind) string {
	if extensions, _ := mime.ExtensionsByType(mimeType); len(extensions) > 0 {
		return extensions[0]
	}
	switch kind {
	case domain.MediaKindImage:
		return ".img"
	case domain.MediaKindVideo:
		return ".mp4"
	case domain.MediaKindVoice:
		return ".ogg"
	case domain.MediaKindAudio:
		return ".audio"
	case domain.MediaKindDocument:
		return ".bin"
	default:
		return ".dat"
	}
}

func cloneBytes(input []byte) []byte {
	if len(input) == 0 {
		return nil
	}
	return append([]byte(nil), input...)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func receiptForIncoming(fromMe bool) domain.ReceiptState {
	if fromMe {
		return domain.ReceiptStateSent
	}
	return domain.ReceiptStateReceived
}

func unixTimeFromUint64(seconds uint64) time.Time {
	if seconds == 0 {
		return time.Time{}
	}
	if seconds > math.MaxInt64 {
		seconds = math.MaxInt64
	}
	return time.Unix(int64(seconds), 0).UTC()
}
