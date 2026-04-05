package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/chirag/whatsapp-terminal/internal/domain"
	appstore "github.com/chirag/whatsapp-terminal/internal/store"
)

type fakeTransport struct {
	events       chan domain.Event
	sentChatJID  string
	sentText     string
	sentImage    string
	sentCaption  string
	historyChat  string
	historyCount int
}

type fakeClipboard struct {
	image []byte
	err   error
}

func (f *fakeTransport) Start(context.Context) error { return nil }
func (f *fakeTransport) Stop() error                 { return nil }
func (f *fakeTransport) Events() <-chan domain.Event { return f.events }
func (f *fakeTransport) SendText(_ context.Context, chatJID, text string) error {
	f.sentChatJID = chatJID
	f.sentText = text
	return nil
}
func (f *fakeTransport) SendImage(_ context.Context, chatJID, path, caption string) error {
	f.sentChatJID = chatJID
	f.sentImage = path
	f.sentCaption = caption
	return nil
}
func (f *fakeTransport) RequestHistory(_ context.Context, chatJID string, count int) error {
	f.historyChat = chatJID
	f.historyCount = count
	return nil
}

func (f *fakeClipboard) ReadImage() ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.image, nil
}

func TestModelOpensSelectedThread(t *testing.T) {
	t.Parallel()

	repo, err := appstore.New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ctx := context.Background()
	now := time.Date(2026, 4, 5, 11, 0, 0, 0, time.UTC)
	if err := repo.UpsertChat(ctx, domain.ChatSummary{
		JID:                "111@s.whatsapp.net",
		Title:              "Alice",
		LastMessagePreview: "hello",
		LastMessageAt:      now,
	}); err != nil {
		t.Fatalf("UpsertChat() error = %v", err)
	}

	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 100
	m.height = 30
	m.ready = true

	updated, _ := m.Update(chatsLoadedMsg{chats: []domain.ChatSummary{{
		JID:                "111@s.whatsapp.net",
		Title:              "Alice",
		LastMessagePreview: "hello",
		LastMessageAt:      now,
	}}})
	model := updated.(Model)

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)

	if model.mode != viewThread {
		t.Fatalf("mode = %v, want viewThread", model.mode)
	}
	if model.currentChatID != "111@s.whatsapp.net" {
		t.Fatalf("currentChatID = %q, want %q", model.currentChatID, "111@s.whatsapp.net")
	}
}

func TestChatListViewFitsTerminalWidth(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 100
	m.height = 28
	m.ready = true
	m.status = "connected (demo mode)"
	m.chats = []domain.ChatSummary{
		{
			JID:                "project-alpha@g.us",
			Title:              "Project Alpha",
			LastMessagePreview: "I’ll review the summary tonight and send comments.",
			LastSenderName:     "You",
			LastMessageAt:      time.Date(2026, 4, 5, 18, 0, 0, 0, time.UTC),
			UnreadCount:        1,
			IsGroup:            true,
		},
		{
			JID:                "alice@s.whatsapp.net",
			Title:              "Alice Mercer",
			LastMessagePreview: "Yes. Let’s do 7:30.",
			LastSenderName:     "You",
			LastMessageAt:      time.Date(2026, 4, 5, 17, 40, 0, 0, time.UTC),
		},
	}

	view := m.View()
	assertViewFitsWidth(t, view, m.width)
	if !strings.Contains(view, "Project Alpha") {
		t.Fatalf("view missing Project Alpha:\n%s", view)
	}
	if !strings.Contains(view, "Alice Mercer") {
		t.Fatalf("view missing Alice Mercer:\n%s", view)
	}
}

func TestThreadViewFitsTerminalWidthWhileComposing(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 96
	m.height = 26
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.status = "connected (demo mode)"
	m.composing = true
	m.composer.SetValue("Message draft")
	m.messages = []domain.Message{
		{
			ID:         "m1",
			ChatJID:    "project-alpha@g.us",
			SenderJID:  "bob@s.whatsapp.net",
			SenderName: "Bob",
			Text:       "Need project numbers by Friday. I pushed the draft sheet.",
			Timestamp:  time.Date(2026, 4, 5, 18, 0, 0, 0, time.UTC),
			Receipt:    domain.ReceiptStateReceived,
			IsGroup:    true,
		},
		{
			ID:         "m2",
			ChatJID:    "project-alpha@g.us",
			SenderJID:  "self@s.whatsapp.net",
			SenderName: "You",
			Text:       "I’ll review the summary tonight and send comments.",
			Timestamp:  time.Date(2026, 4, 5, 18, 2, 0, 0, time.UTC),
			FromMe:     true,
			Receipt:    domain.ReceiptStateDelivered,
			IsGroup:    true,
		},
	}
	m.chats = []domain.ChatSummary{{
		JID:   "project-alpha@g.us",
		Title: "Project Alpha",
	}}

	view := m.View()
	assertViewFitsWidth(t, view, m.width)
	if !strings.Contains(view, "Message draft") {
		t.Fatalf("view missing composer content:\n%s", view)
	}
}

func TestSearchFiltersChatList(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 100
	m.height = 28
	m.ready = true
	m.searching = true
	m.search.SetValue("alice")

	msg := loadChatsCmd(repo, "alice")()
	updated, _ := m.Update(msg)
	model := updated.(Model)

	if len(model.chats) != 1 {
		t.Fatalf("len(model.chats) = %d, want 1", len(model.chats))
	}
	if model.chats[0].Title != "Alice Mercer" {
		t.Fatalf("model.chats[0].Title = %q, want Alice Mercer", model.chats[0].Title)
	}
}

func TestSearchIncludesContactWithoutCachedChat(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 90
	m.height = 26
	m.ready = true
	m.searching = true
	m.search.SetValue("bob")

	msg := loadChatsCmd(repo, "bob")()
	updated, _ := m.Update(msg)
	model := updated.(Model)

	if len(model.chats) != 1 {
		t.Fatalf("len(model.chats) = %d, want 1", len(model.chats))
	}
	if model.chats[0].Title != "Bob Chen" {
		t.Fatalf("model.chats[0].Title = %q, want Bob Chen", model.chats[0].Title)
	}
	if model.chats[0].LastMessagePreview != "" {
		t.Fatalf("model.chats[0].LastMessagePreview = %q, want empty", model.chats[0].LastMessagePreview)
	}
}

func TestLoadChatsCmdDefaultsToRecentChatLimit(t *testing.T) {
	t.Parallel()

	repo, err := appstore.New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ctx := context.Background()
	base := time.Date(2026, 4, 5, 18, 0, 0, 0, time.UTC)
	for i := 0; i < 6; i++ {
		jid := fmt.Sprintf("user-%d@s.whatsapp.net", i)
		if err := repo.UpsertChat(ctx, domain.ChatSummary{
			JID:                jid,
			Title:              fmt.Sprintf("User %d", i),
			LastMessagePreview: "hello",
			LastMessageAt:      base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("UpsertChat(%s) error = %v", jid, err)
		}
	}

	msg := loadChatsCmd(repo, "")()
	loaded, ok := msg.(chatsLoadedMsg)
	if !ok {
		t.Fatalf("loadChatsCmd() type = %T, want chatsLoadedMsg", msg)
	}
	if loaded.err != nil {
		t.Fatalf("loadChatsCmd() error = %v", loaded.err)
	}
	if len(loaded.chats) != defaultChatListLimit {
		t.Fatalf("len(loaded.chats) = %d, want %d", len(loaded.chats), defaultChatListLimit)
	}
	if loaded.chats[0].Title != "User 5" {
		t.Fatalf("loaded.chats[0].Title = %q, want User 5", loaded.chats[0].Title)
	}
}

func TestThreadComposeSendsMessage(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	transport := &fakeTransport{events: make(chan domain.Event, 1)}
	m := NewModel(repo, transport)
	m.width = 96
	m.height = 26
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.composing = true
	m.composer.SetValue("Ship the draft tonight")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model := updated.(Model)
	if cmd == nil {
		t.Fatal("expected send command")
	}

	msg := cmd()
	result, ok := msg.(opResultMsg)
	if !ok {
		t.Fatalf("cmd() type = %T, want opResultMsg", msg)
	}
	if result.err != nil {
		t.Fatalf("send result error = %v", result.err)
	}
	if transport.sentChatJID != "project-alpha@g.us" {
		t.Fatalf("sentChatJID = %q, want project-alpha@g.us", transport.sentChatJID)
	}
	if transport.sentText != "Ship the draft tonight" {
		t.Fatalf("sentText = %q", transport.sentText)
	}
	if model.composer.Value() != "" {
		t.Fatalf("composer value = %q, want cleared", model.composer.Value())
	}
}

func TestThreadComposeSendsImageCommand(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	transport := &fakeTransport{events: make(chan domain.Event, 1)}
	m := NewModel(repo, transport)
	m.width = 96
	m.height = 26
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.composing = true
	m.composer.SetValue(`/image "/tmp/photo one.png" :: team update`)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model := updated.(Model)
	if cmd == nil {
		t.Fatal("expected image send command")
	}

	msg := cmd()
	result, ok := msg.(opResultMsg)
	if !ok {
		t.Fatalf("cmd() type = %T, want opResultMsg", msg)
	}
	if result.err != nil {
		t.Fatalf("send result error = %v", result.err)
	}
	if transport.sentImage != "/tmp/photo one.png" {
		t.Fatalf("sentImage = %q, want /tmp/photo one.png", transport.sentImage)
	}
	if transport.sentCaption != "team update" {
		t.Fatalf("sentCaption = %q, want team update", transport.sentCaption)
	}
	if model.composer.Value() != "" {
		t.Fatalf("composer value = %q, want cleared", model.composer.Value())
	}
}

func TestThreadComposeCtrlVPastesClipboardImage(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	transport := &fakeTransport{events: make(chan domain.Event, 1)}
	clipboard := &fakeClipboard{image: []byte{0x89, 'P', 'N', 'G'}}
	m := NewModelWithClipboard(repo, transport, clipboard)
	m.width = 96
	m.height = 26
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.composing = true

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	_ = updated.(Model)
	if cmd == nil {
		t.Fatal("expected clipboard image command")
	}

	msg := cmd()
	result, ok := msg.(opResultMsg)
	if !ok {
		t.Fatalf("cmd() type = %T, want opResultMsg", msg)
	}
	if result.err != nil {
		t.Fatalf("paste result error = %v", result.err)
	}
	if transport.sentChatJID != "project-alpha@g.us" {
		t.Fatalf("sentChatJID = %q, want project-alpha@g.us", transport.sentChatJID)
	}
	if transport.sentImage == "" {
		t.Fatal("sentImage path is empty")
	}
	if result.status != "Clipboard image sent" {
		t.Fatalf("result.status = %q, want Clipboard image sent", result.status)
	}
}

func TestThreadRequestsHistory(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	transport := &fakeTransport{events: make(chan domain.Event, 1)}
	m := NewModel(repo, transport)
	m.width = 96
	m.height = 26
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	_ = updated.(Model)
	if cmd == nil {
		t.Fatal("expected history command")
	}

	msg := cmd()
	result, ok := msg.(opResultMsg)
	if !ok {
		t.Fatalf("cmd() type = %T, want opResultMsg", msg)
	}
	if result.err != nil {
		t.Fatalf("history result error = %v", result.err)
	}
	if transport.historyChat != "project-alpha@g.us" {
		t.Fatalf("historyChat = %q, want project-alpha@g.us", transport.historyChat)
	}
	if transport.historyCount != 50 {
		t.Fatalf("historyCount = %d, want 50", transport.historyCount)
	}
}

func TestPairingViewFitsTerminalWidth(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 100
	m.height = 32
	m.ready = true
	m.status = "Scan the QR code with WhatsApp on your phone"
	m.qrCode = renderQRCode("2@demo,token,for,qr")

	view := m.View()
	assertViewFitsWidth(t, view, m.width)
	if strings.Contains(view, "\x1b[47m") || strings.Contains(view, "\x1b[40m") {
		t.Fatalf("pairing view still contains ANSI background QR blocks:\n%s", view)
	}
}

func TestChatListViewFitsNarrowTerminalWidth(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 78
	m.height = 24
	m.ready = true
	m.status = "connected"
	m.chats = []domain.ChatSummary{
		{
			JID:                "120363301815645442@g.us",
			Title:              "EIE placements/internships 2022-2026",
			LastMessagePreview: "Hello",
			LastSenderName:     "You",
			LastMessageAt:      time.Date(2026, 4, 5, 4, 42, 0, 0, time.UTC),
			IsGroup:            true,
		},
		{
			JID:                "hirings@s.whatsapp.net",
			Title:              "Hirings/Talent Exchange [1]",
			LastMessagePreview: "*Posting this on behalf of a client - Sheer Love* (Based in Bangalore) Sheer…",
			LastMessageAt:      time.Date(2026, 4, 7, 11, 23, 0, 0, time.UTC),
		},
	}

	view := m.View()
	assertViewFitsWidth(t, view, m.width)
	if !strings.Contains(view, "Latest preview") {
		t.Fatalf("view missing stacked preview section:\n%s", view)
	}
}

func TestChatListUsesSplitLayoutAtMediumWidth(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 84
	m.height = 24
	m.ready = true
	m.status = "connected"
	m.chats = []domain.ChatSummary{
		{
			JID:                "project-alpha@g.us",
			Title:              "Project Alpha",
			LastMessagePreview: "Need project numbers by Friday. I pushed the draft sheet.",
			LastSenderName:     "Bob",
			LastMessageAt:      time.Date(2026, 4, 5, 18, 0, 0, 0, time.UTC),
			UnreadCount:        1,
			IsGroup:            true,
		},
		{
			JID:                "alice@s.whatsapp.net",
			Title:              "Alice Mercer",
			LastMessagePreview: "Coffee later?",
			LastSenderName:     "Alice",
			LastMessageAt:      time.Date(2026, 4, 5, 17, 40, 0, 0, time.UTC),
		},
	}

	view := m.View()
	assertViewFitsWidth(t, view, m.width)
	if !strings.Contains(view, "Search and open") {
		t.Fatalf("view missing split-pane preview header:\n%s", view)
	}
}

func TestSearchResultsStayWithinTerminalHeight(t *testing.T) {
	t.Parallel()

	repo, err := appstore.New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ctx := context.Background()
	base := time.Date(2026, 4, 5, 18, 0, 0, 0, time.UTC)
	for i := 0; i < 18; i++ {
		name := fmt.Sprintf("H Person %02d", i)
		jid := fmt.Sprintf("h-%02d@s.whatsapp.net", i)
		if err := repo.UpsertContact(ctx, domain.Contact{JID: jid, DisplayName: name, PushName: name}); err != nil {
			t.Fatalf("UpsertContact(%s) error = %v", jid, err)
		}
		if err := repo.UpsertChat(ctx, domain.ChatSummary{
			JID:                jid,
			Title:              name,
			LastMessagePreview: "No messages yet",
			LastMessageAt:      base.Add(-time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("UpsertChat(%s) error = %v", jid, err)
		}
	}

	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 96
	m.height = 24
	m.ready = true
	m.status = "connected"
	m.searching = true
	m.search.SetValue("h")
	msg := loadChatsCmd(repo, "h")()
	updated, _ := m.Update(msg)
	model := updated.(Model)
	model.selected = 10

	view := model.View()
	assertViewFitsWidth(t, view, model.width)
	if !strings.Contains(view, "Search: h") {
		t.Fatalf("view missing active search bar:\n%s", view)
	}
	if !strings.Contains(view, "H Person 10") {
		t.Fatalf("view missing selected result window:\n%s", view)
	}
	if !strings.Contains(view, "Latest preview") {
		t.Fatalf("view missing split preview pane:\n%s", view)
	}
	if strings.Contains(view, "H Person 00") || strings.Contains(view, "H Person 17") {
		t.Fatalf("view did not clip large search result set:\n%s", view)
	}
}

func TestFooterWithErrorFitsTerminalWidth(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 88
	m.height = 24
	m.ready = true
	m.lastErr = "database is locked (5) (SQLITE_BUSY)"

	view := m.renderFooter("j/k move  enter open  / search  r refresh  q quit")
	assertViewFitsWidth(t, view, m.width)
	if !strings.Contains(view, "SQLITE_BUSY") {
		t.Fatalf("footer missing error text:\n%s", view)
	}
}

func seededRepo(t *testing.T) *appstore.Store {
	t.Helper()

	repo, err := appstore.New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ctx := context.Background()
	base := time.Date(2026, 4, 5, 18, 0, 0, 0, time.UTC)
	if err := repo.UpsertContact(ctx, domain.Contact{JID: "alice@s.whatsapp.net", DisplayName: "Alice Mercer", PushName: "Alice"}); err != nil {
		t.Fatalf("UpsertContact(alice) error = %v", err)
	}
	if err := repo.UpsertContact(ctx, domain.Contact{JID: "bob@s.whatsapp.net", DisplayName: "Bob Chen", PushName: "Bob"}); err != nil {
		t.Fatalf("UpsertContact(bob) error = %v", err)
	}

	for _, msg := range []domain.Message{
		{
			ID:         "demo-1",
			ChatJID:    "alice@s.whatsapp.net",
			SenderJID:  "alice@s.whatsapp.net",
			SenderName: "Alice",
			Text:       "Coffee later?",
			Timestamp:  base.Add(-20 * time.Minute),
			Receipt:    domain.ReceiptStateReceived,
		},
		{
			ID:         "demo-2",
			ChatJID:    "project-alpha@g.us",
			SenderJID:  "bob@s.whatsapp.net",
			SenderName: "Bob",
			Text:       "Need project numbers by Friday. I pushed the draft sheet.",
			Timestamp:  base.Add(-10 * time.Minute),
			Receipt:    domain.ReceiptStateReceived,
			IsGroup:    true,
		},
	} {
		if err := repo.RecordMessage(ctx, msg, false); err != nil {
			t.Fatalf("RecordMessage(%s) error = %v", msg.ID, err)
		}
	}

	for _, chat := range []domain.ChatSummary{
		{
			JID:                "project-alpha@g.us",
			Title:              "Project Alpha",
			LastMessagePreview: "Need project numbers by Friday. I pushed the draft sheet.",
			LastSenderName:     "Bob",
			LastMessageAt:      base.Add(-10 * time.Minute),
			UnreadCount:        1,
			IsGroup:            true,
		},
		{
			JID:                "alice@s.whatsapp.net",
			Title:              "Alice Mercer",
			LastMessagePreview: "Coffee later?",
			LastSenderName:     "Alice",
			LastMessageAt:      base.Add(-20 * time.Minute),
		},
	} {
		if err := repo.UpsertChat(ctx, chat); err != nil {
			t.Fatalf("UpsertChat(%s) error = %v", chat.JID, err)
		}
	}

	return repo
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

func assertViewFitsWidth(t *testing.T, view string, width int) {
	t.Helper()

	for _, line := range strings.Split(view, "\n") {
		clean := ansiPattern.ReplaceAllString(line, "")
		if lipgloss.Width(clean) > width {
			t.Fatalf("line width %d exceeds %d: %q", lipgloss.Width(clean), width, clean)
		}
	}
}
