package demo

import (
	"context"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chirag/whatsapp-terminal/internal/domain"
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
	if err := t.repo.RecordMessage(ctx, msg, false); err != nil {
		return err
	}

	title := "Unknown"
	if chat, err := t.repo.GetChat(ctx, chatJID); err == nil && chat != nil {
		title = chat.Title
	}
	if err := t.repo.UpsertChat(ctx, domain.ChatSummary{
		JID:                chatJID,
		Title:              title,
		LastMessageID:      msg.ID,
		LastMessagePreview: text,
		LastSenderName:     "You",
		LastMessageAt:      now,
		UnreadCount:        0,
		IsGroup:            msg.IsGroup,
	}); err != nil {
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
	kind, err := detectMediaKind(path)
	if err != nil {
		return err
	}
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
		name = fmt.Sprintf("%s%s", msg.ID, mediaExtension(msg.MediaMIME, msg.MediaKind))
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
	preview := mediaPreview(previewKind, path, caption)
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
		MediaSeconds:  uint32(duration.Round(time.Second) / time.Second),
	}
	if err := t.repo.RecordMessage(ctx, msg, false); err != nil {
		return err
	}

	title := "Unknown"
	if chat, err := t.repo.GetChat(ctx, chatJID); err == nil && chat != nil {
		title = chat.Title
	}
	if err := t.repo.UpsertChat(ctx, domain.ChatSummary{
		JID:                chatJID,
		Title:              title,
		LastMessageID:      msg.ID,
		LastMessagePreview: preview,
		LastSenderName:     "You",
		LastMessageAt:      now,
		UnreadCount:        0,
		IsGroup:            msg.IsGroup,
	}); err != nil {
		return err
	}
	t.emit(domain.Event{Type: domain.EventChatUpdate, ChatJID: chatJID})
	t.emit(domain.Event{Type: domain.EventStatus, Status: fmt.Sprintf("%s sent in demo mode", mediaStatusLabel(previewKind))})
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

func detectMediaKind(path string) (domain.MediaKind, error) {
	mimeType := detectMIME(path)
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		return domain.MediaKindImage, nil
	case strings.HasPrefix(mimeType, "video/"):
		return domain.MediaKindVideo, nil
	case strings.HasPrefix(mimeType, "audio/"):
		return domain.MediaKindAudio, nil
	default:
		return domain.MediaKindDocument, nil
	}
}

func detectMIME(path string) string {
	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if mimeType != "" {
		return mimeType
	}
	file, err := os.Open(path)
	if err != nil {
		return "application/octet-stream"
	}
	defer file.Close()
	sample := make([]byte, 512)
	n, _ := file.Read(sample)
	return http.DetectContentType(sample[:n])
}

func mediaPreview(kind domain.MediaKind, path, caption string) string {
	label := "[" + string(kind) + "]"
	if kind == domain.MediaKindVoice {
		label = "[voice note]"
	}
	preview := label + " " + filepath.Base(path)
	if strings.TrimSpace(caption) != "" {
		preview += " — " + strings.TrimSpace(caption)
	}
	return preview
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
	default:
		return ".bin"
	}
}

func mediaStatusLabel(kind domain.MediaKind) string {
	switch kind {
	case domain.MediaKindVoice:
		return "Voice note"
	case domain.MediaKindImage:
		return "Image"
	case domain.MediaKindVideo:
		return "Video"
	case domain.MediaKindAudio:
		return "Audio"
	default:
		return "Media"
	}
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
	for _, msg := range messages {
		if err := t.repo.RecordMessage(ctx, msg, !msg.FromMe); err != nil {
			return err
		}
	}

	chatsToSeed := []domain.ChatSummary{
		{
			JID:                "project-alpha@g.us",
			Title:              "Project Alpha",
			LastMessageID:      "demo-4",
			LastMessagePreview: "I’ll review the summary tonight and send comments.",
			LastSenderName:     "You",
			LastMessageAt:      base.Add(-8 * time.Minute),
			UnreadCount:        1,
			IsGroup:            true,
		},
		{
			JID:                "alice@s.whatsapp.net",
			Title:              "Alice Mercer",
			LastMessageID:      "demo-2",
			LastMessagePreview: "Yes. Let’s do 7:30.",
			LastSenderName:     "You",
			LastMessageAt:      base.Add(-22 * time.Minute),
		},
	}
	for _, chat := range chatsToSeed {
		if err := t.repo.UpsertChat(ctx, chat); err != nil {
			return err
		}
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
