package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/chirag/whatsapp-terminal/internal/domain"
)

func TestNormalizeSearch(t *testing.T) {
	t.Parallel()

	if got := NormalizeSearch("  Alice   Smith \n "); got != "alice smith" {
		t.Fatalf("NormalizeSearch() = %q, want %q", got, "alice smith")
	}
}

func TestStoreRecordsChatsAndMessages(t *testing.T) {
	t.Parallel()

	repo, err := New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ctx := context.Background()
	base := time.Date(2026, 4, 5, 10, 0, 0, 0, time.UTC)

	if err := repo.UpsertContact(ctx, domain.Contact{
		JID:         "111@s.whatsapp.net",
		DisplayName: "Alice",
	}); err != nil {
		t.Fatalf("UpsertContact() error = %v", err)
	}

	if err := repo.RecordMessage(ctx, domain.Message{
		ID:         "m1",
		ChatJID:    "111@s.whatsapp.net",
		SenderJID:  "111@s.whatsapp.net",
		SenderName: "Alice",
		Text:       "hello from alice",
		Timestamp:  base,
		Receipt:    domain.ReceiptStateReceived,
	}, false); err != nil {
		t.Fatalf("RecordMessage(m1) error = %v", err)
	}

	if err := repo.RecordMessage(ctx, domain.Message{
		ID:         "m2",
		ChatJID:    "222@g.us",
		SenderJID:  "999@s.whatsapp.net",
		SenderName: "Bob",
		Text:       "group update",
		Timestamp:  base.Add(5 * time.Minute),
		Receipt:    domain.ReceiptStateReceived,
		IsGroup:    true,
	}, true); err != nil {
		t.Fatalf("RecordMessage(m2) error = %v", err)
	}

	if err := repo.UpsertChat(ctx, domain.ChatSummary{
		JID:         "111@s.whatsapp.net",
		Title:       "Alice",
		UnreadCount: 0,
	}); err != nil {
		t.Fatalf("UpsertChat(alice) error = %v", err)
	}
	if err := repo.UpsertChat(ctx, domain.ChatSummary{
		JID:         "222@g.us",
		Title:       "Project Group",
		UnreadCount: 1,
		IsGroup:     true,
	}); err != nil {
		t.Fatalf("UpsertChat(group) error = %v", err)
	}

	chats, err := repo.ListChats(ctx, "", 10)
	if err != nil {
		t.Fatalf("ListChats() error = %v", err)
	}
	if len(chats) != 2 {
		t.Fatalf("ListChats() len = %d, want 2", len(chats))
	}
	if chats[0].JID != "222@g.us" {
		t.Fatalf("ListChats()[0].JID = %q, want %q", chats[0].JID, "222@g.us")
	}

	filtered, err := repo.ListChats(ctx, "alice", 10)
	if err != nil {
		t.Fatalf("ListChats(filter) error = %v", err)
	}
	if len(filtered) != 1 || filtered[0].JID != "111@s.whatsapp.net" {
		t.Fatalf("filtered chats = %#v, want Alice chat", filtered)
	}

	messages, err := repo.ListMessages(ctx, "222@g.us", 10)
	if err != nil {
		t.Fatalf("ListMessages() error = %v", err)
	}
	if len(messages) != 1 || messages[0].Text != "group update" {
		t.Fatalf("messages = %#v, want one group message", messages)
	}

	if err := repo.UpdateReceipts(ctx, "222@g.us", []string{"m2"}, domain.ReceiptStateRead); err != nil {
		t.Fatalf("UpdateReceipts() error = %v", err)
	}

	oldest, err := repo.OldestMessage(ctx, "222@g.us")
	if err != nil {
		t.Fatalf("OldestMessage() error = %v", err)
	}
	if oldest == nil || oldest.ID != "m2" || oldest.Receipt != domain.ReceiptStateRead {
		t.Fatalf("oldest message = %#v, want m2 with read receipt", oldest)
	}

	if err := repo.ResetUnread(ctx, "222@g.us"); err != nil {
		t.Fatalf("ResetUnread() error = %v", err)
	}

	chat, err := repo.GetChat(ctx, "222@g.us")
	if err != nil {
		t.Fatalf("GetChat() error = %v", err)
	}
	if chat == nil || chat.UnreadCount != 0 {
		t.Fatalf("chat unread = %#v, want 0", chat)
	}
}

func TestStoreHandlesConcurrentReadsAndWrites(t *testing.T) {
	t.Parallel()

	repo, err := New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ctx := context.Background()
	if err := repo.UpsertChat(ctx, domain.ChatSummary{
		JID:   "project-alpha@g.us",
		Title: "Project Alpha",
	}); err != nil {
		t.Fatalf("UpsertChat() error = %v", err)
	}

	base := time.Date(2026, 4, 5, 10, 0, 0, 0, time.UTC)
	errCh := make(chan error, 32)
	var wg sync.WaitGroup

	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				msg := domain.Message{
					ID:         fmt.Sprintf("msg-%d-%d", worker, i),
					ChatJID:    "project-alpha@g.us",
					SenderJID:  fmt.Sprintf("user-%d@s.whatsapp.net", worker),
					SenderName: fmt.Sprintf("Worker %d", worker),
					Text:       fmt.Sprintf("message %d from worker %d", i, worker),
					Timestamp:  base.Add(time.Duration(worker*25+i) * time.Second),
					Receipt:    domain.ReceiptStateReceived,
					IsGroup:    true,
				}
				if err := repo.RecordMessage(ctx, msg, true); err != nil {
					errCh <- fmt.Errorf("RecordMessage(%s): %w", msg.ID, err)
					return
				}
				if _, err := repo.ListChats(ctx, "project", 10); err != nil {
					errCh <- fmt.Errorf("ListChats(): %w", err)
					return
				}
				if _, err := repo.ListMessages(ctx, "project-alpha@g.us", 20); err != nil {
					errCh <- fmt.Errorf("ListMessages(): %w", err)
					return
				}
			}
		}(worker)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}

	messages, err := repo.ListMessages(ctx, "project-alpha@g.us", 200)
	if err != nil {
		t.Fatalf("ListMessages() error = %v", err)
	}
	if len(messages) != 100 {
		t.Fatalf("len(messages) = %d, want 100", len(messages))
	}
}

func TestListChatsExcludesStatusBroadcast(t *testing.T) {
	t.Parallel()

	repo, err := New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ctx := context.Background()
	if err := repo.UpsertChat(ctx, domain.ChatSummary{
		JID:                "status@broadcast",
		Title:              "status",
		LastMessagePreview: "ignore me",
		LastMessageAt:      time.Date(2026, 4, 5, 10, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("UpsertChat(status) error = %v", err)
	}
	if err := repo.UpsertChat(ctx, domain.ChatSummary{
		JID:                "alice@s.whatsapp.net",
		Title:              "Alice",
		LastMessagePreview: "hello",
		LastMessageAt:      time.Date(2026, 4, 5, 10, 1, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("UpsertChat(alice) error = %v", err)
	}

	chats, err := repo.ListChats(ctx, "", 10)
	if err != nil {
		t.Fatalf("ListChats() error = %v", err)
	}
	if len(chats) != 1 {
		t.Fatalf("len(chats) = %d, want 1", len(chats))
	}
	if chats[0].JID != "alice@s.whatsapp.net" {
		t.Fatalf("chats[0].JID = %q, want alice@s.whatsapp.net", chats[0].JID)
	}
}

func TestGetChatReturnsStoredLastMessageMetadataWithoutCachedMessages(t *testing.T) {
	t.Parallel()

	repo, err := New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ctx := context.Background()
	ts := time.Date(2026, 4, 5, 15, 30, 0, 0, time.UTC)
	if err := repo.UpsertChat(ctx, domain.ChatSummary{
		JID:                "devesh@s.whatsapp.net",
		Title:              "Devesh 306",
		LastMessageID:      "last-msg-1",
		LastMessagePreview: "Hey",
		LastSenderName:     "Devesh 306",
		LastMessageAt:      ts,
	}); err != nil {
		t.Fatalf("UpsertChat() error = %v", err)
	}

	chat, err := repo.GetChat(ctx, "devesh@s.whatsapp.net")
	if err != nil {
		t.Fatalf("GetChat() error = %v", err)
	}
	if chat == nil {
		t.Fatal("GetChat() = nil, want chat summary")
	}
	if chat.LastMessageID != "last-msg-1" {
		t.Fatalf("LastMessageID = %q, want last-msg-1", chat.LastMessageID)
	}
	if !chat.LastMessageAt.Equal(ts) {
		t.Fatalf("LastMessageAt = %v, want %v", chat.LastMessageAt, ts)
	}
}

func TestMergeChatJIDsCombinesSplitDirectThread(t *testing.T) {
	t.Parallel()

	repo, err := New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ctx := context.Background()
	if err := repo.RecordMessage(ctx, domain.Message{
		ID:         "incoming-1",
		ChatJID:    "919380050171@s.whatsapp.net",
		SenderJID:  "919380050171@s.whatsapp.net",
		SenderName: "Khushal",
		Text:       "reply",
		Timestamp:  time.Date(2026, 4, 5, 9, 58, 25, 0, time.UTC),
		Receipt:    domain.ReceiptStateReceived,
	}, false); err != nil {
		t.Fatalf("RecordMessage(incoming) error = %v", err)
	}
	if err := repo.RecordMessage(ctx, domain.Message{
		ID:         "outgoing-1",
		ChatJID:    "51861780467888@lid",
		SenderJID:  "self@s.whatsapp.net",
		SenderName: "You",
		Text:       "Yes",
		Timestamp:  time.Date(2026, 4, 5, 9, 58, 48, 0, time.UTC),
		FromMe:     true,
		Receipt:    domain.ReceiptStateDelivered,
	}, false); err != nil {
		t.Fatalf("RecordMessage(outgoing) error = %v", err)
	}

	if err := repo.MergeChatJIDs(ctx, "51861780467888@lid", "919380050171@s.whatsapp.net"); err != nil {
		t.Fatalf("MergeChatJIDs() error = %v", err)
	}

	messages, err := repo.ListMessages(ctx, "919380050171@s.whatsapp.net", 10)
	if err != nil {
		t.Fatalf("ListMessages() error = %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("len(messages) = %d, want 2", len(messages))
	}
	if messages[0].ID != "incoming-1" || messages[1].ID != "outgoing-1" {
		t.Fatalf("messages = %#v, want merged incoming/outgoing thread", messages)
	}
}

func TestListChatsIncludesMatchingContactsWithoutChatHistory(t *testing.T) {
	t.Parallel()

	repo, err := New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ctx := context.Background()
	if err := repo.UpsertContact(ctx, domain.Contact{
		JID:         "bob@s.whatsapp.net",
		DisplayName: "Bob Chen",
		PushName:    "Bob",
	}); err != nil {
		t.Fatalf("UpsertContact() error = %v", err)
	}

	chats, err := repo.ListChats(ctx, "bob", 10)
	if err != nil {
		t.Fatalf("ListChats() error = %v", err)
	}
	if len(chats) != 1 {
		t.Fatalf("len(chats) = %d, want 1", len(chats))
	}
	if chats[0].JID != "bob@s.whatsapp.net" {
		t.Fatalf("chats[0].JID = %q, want bob@s.whatsapp.net", chats[0].JID)
	}
	if chats[0].Title != "Bob Chen" {
		t.Fatalf("chats[0].Title = %q, want Bob Chen", chats[0].Title)
	}
}

func TestDirectChatPrefersContactNameOverCachedNumericTitle(t *testing.T) {
	t.Parallel()

	repo, err := New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ctx := context.Background()
	testJID := "15550001111@s.whatsapp.net"
	if err := repo.UpsertContact(ctx, domain.Contact{
		JID:         testJID,
		DisplayName: "Asha Rao",
		PushName:    "Asha",
	}); err != nil {
		t.Fatalf("UpsertContact() error = %v", err)
	}
	if err := repo.UpsertChat(ctx, domain.ChatSummary{
		JID:                testJID,
		Title:              "15550001111",
		LastMessagePreview: "hello",
		LastMessageAt:      time.Date(2026, 4, 5, 10, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("UpsertChat() error = %v", err)
	}

	chats, err := repo.ListChats(ctx, "", 10)
	if err != nil {
		t.Fatalf("ListChats() error = %v", err)
	}
	if len(chats) != 1 {
		t.Fatalf("len(chats) = %d, want 1", len(chats))
	}
	if chats[0].Title != "Asha Rao" {
		t.Fatalf("chats[0].Title = %q, want Asha Rao", chats[0].Title)
	}

	chat, err := repo.GetChat(ctx, testJID)
	if err != nil {
		t.Fatalf("GetChat() error = %v", err)
	}
	if chat == nil || chat.Title != "Asha Rao" {
		t.Fatalf("chat = %#v, want title Asha Rao", chat)
	}
}

func TestListMessagesPrefersContactNameOverStoredSenderNumber(t *testing.T) {
	t.Parallel()

	repo, err := New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ctx := context.Background()
	testJID := "15550002222@s.whatsapp.net"
	if err := repo.UpsertContact(ctx, domain.Contact{
		JID:         testJID,
		DisplayName: "Aman Verma",
		PushName:    "Aman",
	}); err != nil {
		t.Fatalf("UpsertContact() error = %v", err)
	}
	if err := repo.RecordMessage(ctx, domain.Message{
		ID:         "msg-1",
		ChatJID:    testJID,
		SenderJID:  testJID,
		SenderName: "15550002222",
		Text:       "Hi",
		Timestamp:  time.Date(2026, 4, 5, 10, 0, 0, 0, time.UTC),
		Receipt:    domain.ReceiptStateReceived,
	}, false); err != nil {
		t.Fatalf("RecordMessage() error = %v", err)
	}

	messages, err := repo.ListMessages(ctx, testJID, 10)
	if err != nil {
		t.Fatalf("ListMessages() error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("len(messages) = %d, want 1", len(messages))
	}
	if messages[0].SenderName != "Aman Verma" {
		t.Fatalf("messages[0].SenderName = %q, want Aman Verma", messages[0].SenderName)
	}
}

func TestListChatsUsesLatestStoredMessageMetadata(t *testing.T) {
	t.Parallel()

	repo, err := New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ctx := context.Background()
	chatJID := "chat-1@s.whatsapp.net"
	if err := repo.UpsertContact(ctx, domain.Contact{
		JID:         "friend@s.whatsapp.net",
		DisplayName: "Friend",
	}); err != nil {
		t.Fatalf("UpsertContact(friend) error = %v", err)
	}
	if err := repo.UpsertChat(ctx, domain.ChatSummary{
		JID:                chatJID,
		Title:              "Friend",
		LastMessageID:      "old-preview",
		LastMessagePreview: "older cached preview",
		LastSenderName:     "Friend",
		LastMessageAt:      time.Date(2026, 4, 5, 10, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("UpsertChat() error = %v", err)
	}
	if err := repo.RecordMessage(ctx, domain.Message{
		ID:         "new-message",
		ChatJID:    chatJID,
		SenderJID:  "self@s.whatsapp.net",
		SenderName: "You",
		Text:       "latest body",
		Timestamp:  time.Date(2026, 4, 5, 11, 0, 0, 0, time.UTC),
		FromMe:     true,
		Receipt:    domain.ReceiptStateRead,
	}, false); err != nil {
		t.Fatalf("RecordMessage() error = %v", err)
	}

	chats, err := repo.ListChats(ctx, "", 10)
	if err != nil {
		t.Fatalf("ListChats() error = %v", err)
	}
	if len(chats) != 1 {
		t.Fatalf("len(chats) = %d, want 1", len(chats))
	}
	if chats[0].LastMessagePreview != "latest body" {
		t.Fatalf("LastMessagePreview = %q, want latest body", chats[0].LastMessagePreview)
	}
	if chats[0].LastSenderName != "You" {
		t.Fatalf("LastSenderName = %q, want You", chats[0].LastSenderName)
	}
	if !chats[0].LastMessageAt.Equal(time.Date(2026, 4, 5, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("LastMessageAt = %s", chats[0].LastMessageAt)
	}
}

func TestListMessagesStableOrderForEqualTimestamp(t *testing.T) {
	t.Parallel()

	repo, err := New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ctx := context.Background()
	chatJID := "project-alpha@g.us"
	ts := time.Date(2026, 4, 5, 10, 0, 0, 0, time.UTC)
	for _, msg := range []domain.Message{
		{
			ID:         "m1",
			ChatJID:    chatJID,
			SenderJID:  "self@s.whatsapp.net",
			SenderName: "You",
			Text:       "first at same second",
			Timestamp:  ts,
			FromMe:     true,
			Receipt:    domain.ReceiptStateSent,
			IsGroup:    true,
		},
		{
			ID:         "m2",
			ChatJID:    chatJID,
			SenderJID:  "friend@s.whatsapp.net",
			SenderName: "Friend",
			Text:       "second at same second",
			Timestamp:  ts,
			Receipt:    domain.ReceiptStateReceived,
			IsGroup:    true,
		},
	} {
		if err := repo.RecordMessage(ctx, msg, false); err != nil {
			t.Fatalf("RecordMessage(%s) error = %v", msg.ID, err)
		}
	}

	messages, err := repo.ListMessages(ctx, chatJID, 10)
	if err != nil {
		t.Fatalf("ListMessages() error = %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("len(messages) = %d, want 2", len(messages))
	}
	if messages[0].ID != "m1" || messages[1].ID != "m2" {
		t.Fatalf("message order = [%s %s], want [m1 m2]", messages[0].ID, messages[1].ID)
	}
}

func TestListChatsPrefersLatestInsertedMessageForEqualTimestamp(t *testing.T) {
	t.Parallel()

	repo, err := New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ctx := context.Background()
	chatJID := "equal-ts@s.whatsapp.net"
	ts := time.Date(2026, 4, 5, 10, 0, 0, 0, time.UTC)
	for _, msg := range []domain.Message{
		{
			ID:         "m1",
			ChatJID:    chatJID,
			SenderJID:  "friend@s.whatsapp.net",
			SenderName: "Friend",
			Text:       "older render at same second",
			Timestamp:  ts,
			Receipt:    domain.ReceiptStateReceived,
		},
		{
			ID:         "m2",
			ChatJID:    chatJID,
			SenderJID:  "self@s.whatsapp.net",
			SenderName: "You",
			Text:       "latest render at same second",
			Timestamp:  ts,
			FromMe:     true,
			Receipt:    domain.ReceiptStateSent,
		},
	} {
		if err := repo.RecordMessage(ctx, msg, false); err != nil {
			t.Fatalf("RecordMessage(%s) error = %v", msg.ID, err)
		}
	}

	chats, err := repo.ListChats(ctx, "", 10)
	if err != nil {
		t.Fatalf("ListChats() error = %v", err)
	}
	if len(chats) != 1 {
		t.Fatalf("len(chats) = %d, want 1", len(chats))
	}
	if chats[0].LastMessageID != "m2" {
		t.Fatalf("LastMessageID = %q, want m2", chats[0].LastMessageID)
	}
	if chats[0].LastMessagePreview != "latest render at same second" {
		t.Fatalf("LastMessagePreview = %q", chats[0].LastMessagePreview)
	}
	if chats[0].LastSenderName != "You" {
		t.Fatalf("LastSenderName = %q, want You", chats[0].LastSenderName)
	}
}

func TestMessageMediaMetadataRoundTrips(t *testing.T) {
	t.Parallel()

	repo, err := New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ctx := context.Background()
	msg := domain.Message{
		ID:                 "media-1",
		ChatJID:            "voice@s.whatsapp.net",
		SenderJID:          "friend@s.whatsapp.net",
		SenderName:         "Friend",
		Text:               "[voice note]",
		Timestamp:          time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC),
		Receipt:            domain.ReceiptStateReceived,
		MediaKind:          domain.MediaKindVoice,
		MediaMIME:          "audio/ogg; codecs=opus",
		MediaFileName:      "voice.ogg",
		MediaDirectPath:    "/mms/audio/demo",
		MediaFileLength:    12345,
		MediaSeconds:       8,
		MediaKey:           []byte{1, 2, 3},
		MediaFileSHA256:    []byte{4, 5, 6},
		MediaFileEncSHA256: []byte{7, 8, 9},
	}
	if err := repo.RecordMessage(ctx, msg, false); err != nil {
		t.Fatalf("RecordMessage() error = %v", err)
	}
	if err := repo.MarkMessageDownloaded(ctx, msg.ChatJID, msg.ID, "/tmp/voice.ogg"); err != nil {
		t.Fatalf("MarkMessageDownloaded() error = %v", err)
	}

	messages, err := repo.ListMessages(ctx, msg.ChatJID, 10)
	if err != nil {
		t.Fatalf("ListMessages() error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("len(messages) = %d, want 1", len(messages))
	}
	got := messages[0]
	if got.MediaKind != domain.MediaKindVoice {
		t.Fatalf("MediaKind = %q, want %q", got.MediaKind, domain.MediaKindVoice)
	}
	if got.MediaDirectPath != "/mms/audio/demo" {
		t.Fatalf("MediaDirectPath = %q", got.MediaDirectPath)
	}
	if got.MediaFileName != "voice.ogg" {
		t.Fatalf("MediaFileName = %q", got.MediaFileName)
	}
	if got.MediaFileLength != 12345 {
		t.Fatalf("MediaFileLength = %d", got.MediaFileLength)
	}
	if got.MediaSeconds != 8 {
		t.Fatalf("MediaSeconds = %d", got.MediaSeconds)
	}
	if got.DownloadedPath != "/tmp/voice.ogg" {
		t.Fatalf("DownloadedPath = %q", got.DownloadedPath)
	}
}
