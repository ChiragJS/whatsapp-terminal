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
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)", sessionPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("open session db: %w", err)
	}

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
	resp, err := client.SendMessage(ctx, jid, &waE2E.Message{
		Conversation: proto.String(text),
	})
	if err != nil {
		return fmt.Errorf("send message: %w", err)
	}

	stored := domain.Message{
		ID:         resp.ID,
		ChatJID:    chatJID,
		SenderJID:  a.selfJID().String(),
		SenderName: "You",
		Text:       strings.TrimSpace(text),
		Timestamp:  time.Now().UTC(),
		FromMe:     true,
		Receipt:    domain.ReceiptStateSent,
		IsGroup:    jid.Server == types.GroupServer,
	}
	if err := a.repo.RecordMessage(ctx, stored, false); err != nil {
		return err
	}
	if err := a.repo.UpsertChat(ctx, domain.ChatSummary{
		JID:                chatJID,
		Title:              a.resolveChatTitle(ctx, jid, ""),
		LastMessageID:      stored.ID,
		LastMessagePreview: stored.Text,
		LastSenderName:     stored.SenderName,
		LastMessageAt:      stored.Timestamp,
		IsGroup:            stored.IsGroup,
	}); err != nil {
		return err
	}
	a.emit(domain.Event{Type: domain.EventChatUpdate, ChatJID: chatJID})
	return nil
}

func (a *Adapter) SendImage(ctx context.Context, chatJID, path, caption string) error {
	client := a.clientRef()
	if client == nil {
		return errors.New("client is not ready")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read image: %w", err)
	}
	if len(data) == 0 {
		return errors.New("image file is empty")
	}

	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if mimeType == "" {
		sample := data
		if len(sample) > 512 {
			sample = sample[:512]
		}
		mimeType = http.DetectContentType(sample)
	}
	if !strings.HasPrefix(mimeType, "image/") {
		return fmt.Errorf("unsupported media type %q: only images are supported", mimeType)
	}

	cfg, _, decodeErr := image.DecodeConfig(bytes.NewReader(data))
	if decodeErr != nil {
		cfg = image.Config{}
	}

	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("parse chat JID: %w", err)
	}
	upload, err := client.Upload(ctx, data, whatsmeow.MediaImage)
	if err != nil {
		return fmt.Errorf("upload image: %w", err)
	}

	imageMsg := &waE2E.ImageMessage{
		Mimetype:      proto.String(mimeType),
		Caption:       proto.String(strings.TrimSpace(caption)),
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

	resp, err := client.SendMessage(ctx, jid, &waE2E.Message{ImageMessage: imageMsg})
	if err != nil {
		return fmt.Errorf("send image: %w", err)
	}

	preview := "[image] " + filepath.Base(path)
	if trimmed := strings.TrimSpace(caption); trimmed != "" {
		preview += " — " + trimmed
	}
	stored := domain.Message{
		ID:         resp.ID,
		ChatJID:    chatJID,
		SenderJID:  a.selfJID().String(),
		SenderName: "You",
		Text:       preview,
		Timestamp:  time.Now().UTC(),
		FromMe:     true,
		Receipt:    domain.ReceiptStateSent,
		IsGroup:    jid.Server == types.GroupServer,
	}
	if err := a.repo.RecordMessage(ctx, stored, false); err != nil {
		return err
	}
	if err := a.repo.UpsertChat(ctx, domain.ChatSummary{
		JID:                chatJID,
		Title:              a.resolveChatTitle(ctx, jid, ""),
		LastMessageID:      stored.ID,
		LastMessagePreview: preview,
		LastSenderName:     stored.SenderName,
		LastMessageAt:      stored.Timestamp,
		IsGroup:            stored.IsGroup,
	}); err != nil {
		return err
	}
	a.emit(domain.Event{Type: domain.EventChatUpdate, ChatJID: chatJID})
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
	if oldest == nil {
		return errors.New("no cached messages available for history sync")
	}
	chat, err := types.ParseJID(chatJID)
	if err != nil {
		return fmt.Errorf("parse chat JID: %w", err)
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
	ctx, cancel := a.contextWithTimeout(20 * time.Second)
	defer cancel()

	switch evt := raw.(type) {
	case *waevents.Connected:
		a.emit(domain.Event{Type: domain.EventStatus, Status: "connected"})
		if err := a.syncContacts(ctx); err != nil {
			a.emit(domain.Event{Type: domain.EventError, Err: err})
		}
		a.emit(domain.Event{Type: domain.EventChatListUpdate})
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
		_ = a.repo.UpsertContact(ctx, domain.Contact{
			JID:      evt.JID.ToNonAD().String(),
			PushName: evt.NewPushName,
		})
		a.emit(domain.Event{Type: domain.EventChatListUpdate})
	case *waevents.Contact:
		_ = a.syncContacts(ctx)
		a.emit(domain.Event{Type: domain.EventChatListUpdate})
	case *waevents.HistorySync:
		if err := a.handleHistorySync(ctx, evt.Data); err != nil {
			a.emit(domain.Event{Type: domain.EventError, Err: err})
		}
	case *waevents.Message:
		if err := a.handleMessage(ctx, evt.UnwrapRaw()); err != nil {
			a.emit(domain.Event{Type: domain.EventError, Err: err})
		}
	case *waevents.Receipt:
		if err := a.handleReceipt(ctx, evt); err != nil {
			a.emit(domain.Event{Type: domain.EventError, Err: err})
		}
	}
}

func (a *Adapter) handleHistorySync(ctx context.Context, sync *waHistorySync.HistorySync) error {
	for _, conversation := range sync.GetConversations() {
		chatJID, err := types.ParseJID(conversation.GetID())
		if err != nil || chatJID.IsEmpty() {
			continue
		}

		chat := domain.ChatSummary{
			JID:         chatJID.String(),
			Title:       a.resolveChatTitle(ctx, chatJID, firstNonEmpty(conversation.GetDisplayName(), conversation.GetName())),
			UnreadCount: int(conversation.GetUnreadCount()),
			IsGroup:     chatJID.Server == types.GroupServer,
		}

		var latest *domain.Message
		for _, item := range conversation.GetMessages() {
			msg, ok := a.historyMessageToDomain(ctx, chatJID, item)
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

		if latest != nil {
			chat.LastMessageID = latest.ID
			chat.LastMessagePreview = latest.Text
			chat.LastSenderName = latest.SenderName
			chat.LastMessageAt = latest.Timestamp
		} else if conversation.GetConversationTimestamp() > 0 {
			chat.LastMessageAt = time.Unix(int64(conversation.GetConversationTimestamp()), 0).UTC()
		}

		if err := a.repo.UpsertChat(ctx, chat); err != nil {
			return err
		}
	}

	a.emit(domain.Event{Type: domain.EventChatListUpdate, Status: "History sync updated"})
	return nil
}

func (a *Adapter) handleMessage(ctx context.Context, evt *waevents.Message) error {
	if evt == nil || evt.Message == nil {
		return nil
	}

	chatJID := evt.Info.Chat.ToNonAD()
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

	if !evt.Info.IsGroup && evt.Info.PushName != "" {
		_ = a.repo.UpsertContact(ctx, domain.Contact{
			JID:      evt.Info.Sender.ToNonAD().String(),
			PushName: evt.Info.PushName,
		})
	}

	if err := a.repo.RecordMessage(ctx, msg, !msg.FromMe); err != nil {
		return err
	}
	if err := a.repo.UpsertChat(ctx, domain.ChatSummary{
		JID:                msg.ChatJID,
		Title:              a.resolveChatTitle(ctx, chatJID, evt.Info.PushName),
		LastMessageID:      msg.ID,
		LastMessagePreview: msg.Text,
		LastSenderName:     msg.SenderName,
		LastMessageAt:      msg.Timestamp,
		IsGroup:            msg.IsGroup,
	}); err != nil {
		return err
	}

	a.emit(domain.Event{Type: domain.EventChatUpdate, ChatJID: msg.ChatJID})
	return nil
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
	if client == nil || client.Store == nil || client.Store.Contacts == nil {
		return nil
	}
	contacts, err := client.Store.Contacts.GetAllContacts(ctx)
	if err != nil {
		return fmt.Errorf("sync contacts: %w", err)
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
	return nil
}

func (a *Adapter) historyMessageToDomain(ctx context.Context, chatJID types.JID, item *waHistorySync.HistorySyncMsg) (domain.Message, bool) {
	if item == nil || item.GetMessage() == nil || item.GetMessage().GetMessage() == nil {
		return domain.Message{}, false
	}
	webMsg := item.GetMessage()
	key := webMsg.GetKey()
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
		Text:       extractText(webMsg.GetMessage()),
		Timestamp:  time.Unix(int64(webMsg.GetMessageTimestamp()), 0).UTC(),
		FromMe:     key.GetFromMe(),
		Receipt:    receiptForIncoming(key.GetFromMe()),
		IsGroup:    chatJID.Server == types.GroupServer,
	}
	if msg.FromMe {
		msg.SenderName = "You"
	}
	return msg, msg.ID != ""
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
		return "[document]"
	case message.GetStickerMessage() != nil:
		return "[sticker]"
	case message.GetAudioMessage() != nil:
		return "[audio]"
	case message.GetReactionMessage() != nil:
		return "[reaction]"
	default:
		return "[unsupported message]"
	}
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
