package demo

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chirag/whatsapp-terminal/internal/domain"
	"github.com/chirag/whatsapp-terminal/internal/media"
	appstore "github.com/chirag/whatsapp-terminal/internal/store"
)

type Transport struct {
	repo   *appstore.Store
	logger *slog.Logger
	events chan domain.Event

	mu sync.Mutex
}

func New(repo *appstore.Store, logger *slog.Logger) *Transport {
	return &Transport{
		repo:   repo,
		logger: logger,
		events: make(chan domain.Event, 64),
	}
}

func (t *Transport) Start(ctx context.Context) error {
	if err := t.seed(ctx); err != nil {
		return err
	}
	t.emit(domain.Event{Type: domain.EventStatus, Status: "connected (demo mode)"})
	t.emit(domain.Event{Type: domain.EventChatListUpdate})
	return nil
}

func (t *Transport) Stop() error {
	return nil
}

func (t *Transport) Events() <-chan domain.Event {
	return t.events
}

func (t *Transport) SendText(ctx context.Context, chatJID, text string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now().UTC()
	msg := domain.Message{
		ID:         fmt.Sprintf("demo-%d", now.UnixNano()),
		ChatJID:    chatJID,
		SenderJID:  "self@s.whatsapp.net",
		SenderName: "You",
		Text:       text,
		Timestamp:  now,
		FromMe:     true,
		Receipt:    domain.ReceiptStateSent,
		IsGroup:    chatJID == "project-alpha@g.us",
	}
	if err := t.repo.RecordMessageWithChatTitle(ctx, msg, "Unknown", false); err != nil {
		return err
	}

	t.emit(domain.Event{Type: domain.EventChatUpdate, ChatJID: chatJID})
	t.emit(domain.Event{Type: domain.EventStatus, Status: "Message sent in demo mode"})
	return nil
}

func (t *Transport) SendImage(ctx context.Context, chatJID, path, caption string) error {
	return t.recordOutgoingMedia(ctx, chatJID, path, caption, domain.MediaKindImage, domain.MediaKindImage, 0)
}

func (t *Transport) SendMedia(ctx context.Context, chatJID, path, caption string) error {
	kind := media.KindForMIME(detectMIME(path))
	return t.recordOutgoingMedia(ctx, chatJID, path, caption, kind, kind, 0)
}

func (t *Transport) SendVoiceNote(ctx context.Context, chatJID, path string, duration time.Duration) error {
	return t.recordOutgoingMedia(ctx, chatJID, path, "", domain.MediaKindVoice, domain.MediaKindAudio, duration)
}

func (t *Transport) DownloadMedia(ctx context.Context, msg domain.Message, downloadDir string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if msg.MediaKind == domain.MediaKindNone {
		return "", fmt.Errorf("message does not contain downloadable media")
	}
	if err := os.MkdirAll(downloadDir, 0o700); err != nil {
		return "", err
	}
	name := msg.MediaFileName
	if name == "" {
		name = fmt.Sprintf("%s%s", msg.ID, media.Extension(msg.MediaMIME, msg.MediaKind))
	}
	targetPath := filepath.Join(downloadDir, name)
	content := []byte("demo media placeholder")
	if err := os.WriteFile(targetPath, content, 0o600); err != nil {
		return "", err
	}
	if err := t.repo.MarkMessageDownloaded(ctx, msg.ChatJID, msg.ID, targetPath); err != nil {
		return "", err
	}
	return targetPath, nil
}

func (t *Transport) recordOutgoingMedia(ctx context.Context, chatJID, path, caption string, previewKind, storedKind domain.MediaKind, duration time.Duration) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now().UTC()
	preview := media.Preview(previewKind, filepath.Base(path), caption)
	msg := domain.Message{
		ID:            fmt.Sprintf("demo-media-%d", now.UnixNano()),
		ChatJID:       chatJID,
		SenderJID:     "self@s.whatsapp.net",
		SenderName:    "You",
		Text:          preview,
		Timestamp:     now,
		FromMe:        true,
		Receipt:       domain.ReceiptStateSent,
		IsGroup:       chatJID == "project-alpha@g.us",
		MediaKind:     storedKind,
		MediaMIME:     detectMIME(path),
		MediaFileName: filepath.Base(path),
		MediaSeconds:  durationSeconds(duration),
	}
	if err := t.repo.RecordMessageWithChatTitle(ctx, msg, "Unknown", false); err != nil {
		return err
	}
	t.emit(domain.Event{Type: domain.EventChatUpdate, ChatJID: chatJID})
	t.emit(domain.Event{Type: domain.EventStatus, Status: fmt.Sprintf("%s sent in demo mode", media.StatusLabel(previewKind))})
	return nil
}

func (t *Transport) RequestHistory(ctx context.Context, chatJID string, count int) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	oldest, err := t.repo.OldestMessage(ctx, chatJID)
	if err != nil {
		return err
	}
	base := time.Now().UTC()
	if oldest != nil {
		base = oldest.Timestamp
	}
	for i := 0; i < min(3, count); i++ {
		ts := base.Add(-time.Duration((i + 1) * int(time.Hour)))
		msg := domain.Message{
			ID:         fmt.Sprintf("history-%d-%d", ts.Unix(), i),
			ChatJID:    chatJID,
			SenderJID:  chatJID,
			SenderName: "Archive",
			Text:       fmt.Sprintf("Older demo message %d", i+1),
			Timestamp:  ts,
			Receipt:    domain.ReceiptStateReceived,
			IsGroup:    chatJID == "project-alpha@g.us",
		}
		if err := t.repo.RecordMessage(ctx, msg, false); err != nil {
			return err
		}
	}

	t.emit(domain.Event{Type: domain.EventChatUpdate, ChatJID: chatJID})
	t.emit(domain.Event{Type: domain.EventStatus, Status: "Loaded older demo history"})
	return nil
}

func detectMIME(path string) string {
	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if mimeType != "" {
		return mimeType
	}
	// #nosec G304 -- path is a user-selected local attachment path in demo mode.
	file, err := os.Open(path)
	if err != nil {
		return "application/octet-stream"
	}
	defer file.Close()
	sample := make([]byte, 512)
	n, _ := file.Read(sample)
	return http.DetectContentType(sample[:n])
}
func (t *Transport) seed(ctx context.Context) error {
	chats, err := t.repo.ListChats(ctx, "", 1)
	if err != nil {
		return err
	}
	if len(chats) > 0 {
		return nil
	}

	contacts := []domain.Contact{
		{JID: "alice@s.whatsapp.net", DisplayName: "Alice Mercer", PushName: "Alice"},
		{JID: "bob@s.whatsapp.net", DisplayName: "Bob Chen", PushName: "Bob"},
		{JID: "project-alpha@g.us", DisplayName: "Project Alpha"},
	}
	for _, contact := range contacts {
		if err := t.repo.UpsertContact(ctx, contact); err != nil {
			return err
		}
	}

	base := time.Date(2026, 4, 5, 18, 0, 0, 0, time.UTC)
	messages := []domain.Message{
		{
			ID:         "demo-1",
			ChatJID:    "alice@s.whatsapp.net",
			SenderJID:  "alice@s.whatsapp.net",
			SenderName: "Alice",
			Text:       "Coffee later? I found a place with strong Wi-Fi and no crowd.",
			Timestamp:  base.Add(-25 * time.Minute),
			Receipt:    domain.ReceiptStateReceived,
		},
		{
			ID:         "demo-2",
			ChatJID:    "alice@s.whatsapp.net",
			SenderJID:  "self@s.whatsapp.net",
			SenderName: "You",
			Text:       "Yes. Let’s do 7:30.",
			Timestamp:  base.Add(-22 * time.Minute),
			FromMe:     true,
			Receipt:    domain.ReceiptStateRead,
		},
		{
			ID:         "demo-3",
			ChatJID:    "project-alpha@g.us",
			SenderJID:  "bob@s.whatsapp.net",
			SenderName: "Bob",
			Text:       "Need project numbers by Friday. I pushed the draft sheet.",
			Timestamp:  base.Add(-10 * time.Minute),
			Receipt:    domain.ReceiptStateReceived,
			IsGroup:    true,
		},
		{
			ID:         "demo-4",
			ChatJID:    "project-alpha@g.us",
			SenderJID:  "self@s.whatsapp.net",
			SenderName: "You",
			Text:       "I’ll review the summary tonight and send comments.",
			Timestamp:  base.Add(-8 * time.Minute),
			FromMe:     true,
			Receipt:    domain.ReceiptStateDelivered,
			IsGroup:    true,
		},
	}
	if err := t.repo.RecordHistoryBatch(ctx, domain.ChatSummary{
		JID:         "alice@s.whatsapp.net",
		Title:       "Alice Mercer",
		UnreadCount: 0,
	}, messages[:2]); err != nil {
		return err
	}
	if err := t.repo.RecordHistoryBatch(ctx, domain.ChatSummary{
		JID:         "project-alpha@g.us",
		Title:       "Project Alpha",
		UnreadCount: 1,
		IsGroup:     true,
	}, messages[2:]); err != nil {
		return err
	}
	return nil
}

func (t *Transport) emit(event domain.Event) {
	select {
	case t.events <- event:
	default:
		if t.logger != nil {
			t.logger.Warn("dropping demo event", "type", event.Type)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func durationSeconds(duration time.Duration) uint32 {
	seconds := duration.Round(time.Second) / time.Second
	if seconds <= 0 {
		return 0
	}
	if seconds > time.Duration(math.MaxUint32) {
		return math.MaxUint32
	}
	return uint32(seconds)
}
