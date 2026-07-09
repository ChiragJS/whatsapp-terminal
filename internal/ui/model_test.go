package ui

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
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
	events            chan domain.Event
	sentChatJID       string
	sentText          string
	sentImage         string
	sentMedia         string
	sentVoice         string
	sentCaption       string
	sentVoiceDuration time.Duration
	downloadedMessage string
	downloadedDir     string
	historyChat       string
	historyCount      int

	reactionTargetSender string
	reactionTargetID     string
	reactionEmoji        string
	sentMentions         []string
}

type fakeClipboard struct {
	image []byte
	err   error
}

type fakeSounder struct {
	bells int
	err   error
}

type fakeVoiceRecorder struct {
	startErr error
	stopErr  error
	path     string
	duration time.Duration
	started  bool
	stopped  bool
	canceled bool
}

func (f *fakeTransport) Start(context.Context) error { return nil }
func (f *fakeTransport) Stop() error                 { return nil }
func (f *fakeTransport) Events() <-chan domain.Event { return f.events }
func (f *fakeTransport) SendText(_ context.Context, chatJID, text string, mentionJIDs ...string) error {
	f.sentChatJID = chatJID
	f.sentText = text
	f.sentMentions = mentionJIDs
	return nil
}
func (f *fakeTransport) SendImage(_ context.Context, chatJID, path, caption string) error {
	f.sentChatJID = chatJID
	f.sentImage = path
	f.sentCaption = caption
	return nil
}
func (f *fakeTransport) SendMedia(_ context.Context, chatJID, path, caption string) error {
	f.sentChatJID = chatJID
	f.sentMedia = path
	f.sentCaption = caption
	return nil
}
func (f *fakeTransport) SendVoiceNote(_ context.Context, chatJID, path string, duration time.Duration) error {
	f.sentChatJID = chatJID
	f.sentVoice = path
	f.sentVoiceDuration = duration
	return nil
}
func (f *fakeTransport) SendReaction(_ context.Context, chatJID, targetSenderJID, targetMessageID, emoji string) error {
	f.sentChatJID = chatJID
	f.reactionTargetSender = targetSenderJID
	f.reactionTargetID = targetMessageID
	f.reactionEmoji = emoji
	return nil
}
func (f *fakeTransport) DownloadMedia(_ context.Context, msg domain.Message, dir string) (string, error) {
	f.downloadedMessage = msg.ID
	f.downloadedDir = dir
	return filepath.Join(dir, "downloaded.bin"), nil
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

func (f *fakeSounder) Bell() error {
	f.bells++
	return f.err
}

func (f *fakeVoiceRecorder) Start() error {
	if f.startErr != nil {
		return f.startErr
	}
	f.started = true
	return nil
}

func (f *fakeVoiceRecorder) Stop() (voiceRecordingResult, error) {
	if f.stopErr != nil {
		return voiceRecordingResult{}, f.stopErr
	}
	f.stopped = true
	return voiceRecordingResult{Path: f.path, Duration: f.duration}, nil
}

func (f *fakeVoiceRecorder) Cancel() error {
	f.canceled = true
	return nil
}

func assertQuitCmd(t *testing.T, cmd tea.Cmd) {
	t.Helper()

	if cmd == nil {
		t.Fatal("expected quit command")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("cmd() type = %T, want tea.QuitMsg", msg)
	}
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

func TestDisplayChatTitleMasksJIDShapedGroupTitles(t *testing.T) {
	t.Parallel()

	chat := domain.ChatSummary{
		JID:     "120363405662701156@g.us",
		Title:   "120363405662701156",
		IsGroup: true,
	}
	if got := displayChatTitle(chat); got != "Unnamed group · …1156" {
		t.Fatalf("displayChatTitle() = %q, want friendly unknown group label", got)
	}

	chat.Title = chat.JID
	if got := displayChatTitle(chat); got != "Unnamed group · …1156" {
		t.Fatalf("displayChatTitle(full JID) = %q, want friendly unknown group label", got)
	}
}

func TestDisplayChatTitleCollapsesEmbeddedNewlines(t *testing.T) {
	t.Parallel()

	chat := domain.ChatSummary{
		JID:   "120363405662701156@g.us",
		Title: "Coding Club 2023-2024\nBidadi Boys 2024.",
	}
	got := displayChatTitle(chat)
	if strings.ContainsAny(got, "\r\n\t") {
		t.Fatalf("displayChatTitle() = %q, must not contain newlines/tabs", got)
	}
	if got != "Coding Club 2023-2024 Bidadi Boys 2024." {
		t.Fatalf("displayChatTitle() = %q, want collapsed single line", got)
	}
}

func TestDisplayChatTitleHandlesUnknownServerAndStrippedSuffix(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		chat domain.ChatSummary
		want string
	}{
		{
			name: "long digit title with @g.us jid that doesn't exactly match",
			chat: domain.ChatSummary{JID: "207485927428170@g.us", Title: "207485927428170 "}, // trailing whitespace
			want: "Unnamed group · …8170",
		},
		{
			name: "long digit title with @lid jid (linked id)",
			chat: domain.ChatSummary{JID: "53210366615785@lid", Title: "53210366615785"},
			want: "Linked contact · …5785",
		},
		{
			name: "long digit title with bare-digit jid (no @ at all)",
			chat: domain.ChatSummary{JID: "143263080181835", Title: "143263080181835"},
			want: "Unknown contact · …1835",
		},
		{
			name: "long digit title on direct chat masks phone number",
			chat: domain.ChatSummary{JID: "919160001284@s.whatsapp.net", Title: "120229036318754"},
			want: "Unknown contact · …1284",
		},
		{
			name: "redacted phone-like title on direct chat masks phone number",
			chat: domain.ChatSummary{JID: "917619531904@s.whatsapp.net", Title: "+91∙∙∙∙∙∙∙∙04"},
			want: "Unknown contact · …1904",
		},
		{
			name: "normal direct chat title is preserved",
			chat: domain.ChatSummary{JID: "alice@s.whatsapp.net", Title: "Alice Mercer"},
			want: "Alice Mercer",
		},
		{
			name: "short numeric title (could be initials) is preserved",
			chat: domain.ChatSummary{JID: "g@g.us", Title: "2024"},
			want: "2024",
		},
	}
	for _, tc := range cases {
		if got := displayChatTitle(tc.chat); got != tc.want {
			t.Errorf("%s: displayChatTitle() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestDisplaySenderLabelMasksDigitSenders(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"real name kept", "Bob Chen", "Bob Chen"},
		{"digit-only sender (15 digits)", "234273336496234", "…6234"},
		{"digit-only with whitespace", "  155168645603548\n", "…3548"},
		{"full JID with digit user", "232121910177920@s.whatsapp.net", "…7920"},
		{"redacted phone-like sender", "+91∙∙∙∙∙∙∙∙04", "…9104"},
		{"empty", "", ""},
		{"short digit run is left alone (could be a username)", "1234", "1234"},
	}
	for _, tc := range cases {
		if got := displaySenderLabel(tc.in); got != tc.want {
			t.Errorf("%s: displaySenderLabel(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestDumpChatListDiagnosticsEmitsPerChatRecords(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(handler)

	m := Model{
		logger: logger,
		chats: []domain.ChatSummary{
			{JID: "alice@s.whatsapp.net", Title: "Alice Mercer"},
			{JID: "120363405662701156@g.us", Title: "120363405662701156", IsGroup: true, LastSenderName: "234273336496234"},
		},
	}
	m.dumpChatListDiagnostics()
	out := buf.String()

	for _, needle := range []string{
		"ui:dump.begin",
		"ui:dump.chat",
		"ui:dump.end",
		`jid=alice@s.whatsapp.net`,
		`display_title="Alice Mercer"`,
		`jid=120363405662701156@g.us`,
		`display_title="Unnamed group · …1156"`,
		`display_sender=…6234`,
	} {
		if !strings.Contains(out, needle) {
			t.Errorf("dump log missing %q\nfull output:\n%s", needle, out)
		}
	}
}

func TestRenderFooterHasStableThreeLineHeight(t *testing.T) {
	t.Parallel()

	m := Model{width: 80}
	withoutErr := m.renderFooter("j/k move  enter open")
	m.lastErr = "database is locked"
	withErr := m.renderFooter("j/k move  enter open")
	if got := countRenderedLines(withoutErr); got != 3 {
		t.Fatalf("footer without error = %d lines, want 3:\n%s", got, withoutErr)
	}
	if got := countRenderedLines(withErr); got != 3 {
		t.Fatalf("footer with error = %d lines, want 3:\n%s", got, withErr)
	}
}

func TestRenderChatItemAlwaysProducesExactlyTwoLines(t *testing.T) {
	t.Parallel()

	cases := []domain.ChatSummary{
		{JID: "alice@s.whatsapp.net", Title: "Alice", LastMessagePreview: "hi"},
		{JID: "g@g.us", Title: "Multi\nLine\nTitle\nHere", LastMessagePreview: "preview\nwith\nnewlines", IsGroup: true},
		{JID: "120363@g.us", Title: "", LastMessagePreview: "", IsGroup: true, UnreadCount: 7},
		{JID: "120363@g.us", Title: "120363", LastMessagePreview: "x", IsGroup: true, UnreadCount: 250},
	}
	for i, chat := range cases {
		out := renderChatItem(chat, 40, i == 1, nil)
		if got := countRenderedLines(out); got != 2 {
			t.Fatalf("case %d: renderChatItem produced %d lines, want 2:\n%s", i, got, out)
		}
	}
}

func TestRenderChatItemMasksUnknownDirectPhoneArtifacts(t *testing.T) {
	t.Parallel()

	chat := domain.ChatSummary{
		JID:                "919160001284@s.whatsapp.net",
		Title:              "120229036318754",
		LastSenderName:     "120229036318754",
		LastMessagePreview: "[unsupported message]",
		UnreadCount:        2,
	}
	out := renderChatItem(chat, 48, true, nil)
	if !strings.Contains(out, "Unknown contact · …1284") {
		t.Fatalf("renderChatItem() missing masked contact label:\n%s", out)
	}
	for _, forbidden := range []string{"120229036318754", "+919160001284"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("renderChatItem() leaked %q:\n%s", forbidden, out)
		}
	}
}

func TestChatListNavigationDoesNotRecenterWithinVisiblePage(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 96
	m.height = 32
	m.ready = true
	m.chats = makeChatSummaries(20)

	for i := 0; i < 5; i++ {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		m = updated.(Model)
	}

	if m.selected != 5 {
		t.Fatalf("selected = %d, want 5", m.selected)
	}
	view := m.View()
	assertViewFitsWidth(t, view, m.width)
	assertViewFitsHeight(t, view, m.height)
	if !strings.Contains(view, "Chat 00") {
		t.Fatalf("viewport recentered before selected left the first page:\n%s", view)
	}
	if !strings.Contains(view, "Chat 05") {
		t.Fatalf("view missing selected chat:\n%s", view)
	}
}

func TestChatPreviewCapsLongLatestPreview(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.chats = []domain.ChatSummary{{
		JID:                "long-preview@s.whatsapp.net",
		Title:              "Long Preview",
		LastMessagePreview: strings.Repeat("This preview is intentionally long and should not consume the whole side pane. ", 20),
		LastMessageAt:      time.Date(2026, 4, 5, 18, 0, 0, 0, time.UTC),
	}}

	body := m.chatPreviewBody(54, 16)
	if lines := countRenderedLines(body); lines > 16 {
		t.Fatalf("preview body lines = %d, want <= 16:\n%s", lines, body)
	}
	if !strings.Contains(body, "Actions") {
		t.Fatalf("preview body lost actions after capping long preview:\n%s", body)
	}
}

func TestViewFramesAvoidAutowrapColumn(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 96
	m.height = 26
	m.ready = true
	m.chats = makeChatSummaries(20)

	first := m.View()
	assertViewAvoidsAutowrapColumn(t, first, m.width, m.height)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = updated.(Model)
	second := m.View()
	assertViewAvoidsAutowrapColumn(t, second, m.width, m.height)
}

func TestForceRepaintChangesRawFrameWithoutChangingVisibleFrame(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)}).WithForceRepaint(true)
	m.width = 96
	m.height = 26
	m.ready = true
	m.chats = makeChatSummaries(20)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = updated.(Model)
	first := m.View()
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = updated.(Model)
	second := m.View()

	if first == second {
		t.Fatal("raw frames are identical; expected repaint marker to force redraw")
	}
	if ansiPattern.ReplaceAllString(first, "") != ansiPattern.ReplaceAllString(second, "") {
		t.Fatalf("visible frames differ after equivalent selection:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestSmallCapLeavesRightEdgeSafetyMargin(t *testing.T) {
	t.Parallel()

	const width = 54
	for _, label := range []string{"Detail", "Latest preview", "Actions"} {
		line := smallCap(label, width)
		if got, maxWidth := lipgloss.Width(line), width-3; got > maxWidth {
			t.Fatalf("smallCap(%q) width = %d, want <= %d", label, got, maxWidth)
		}
	}
}

func TestThreadLongMessageReceiptDoesNotOverflowPanel(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 96
	m.height = 26
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.chats = []domain.ChatSummary{{JID: "project-alpha@g.us", Title: "Project Alpha", IsGroup: true}}
	m.messages = []domain.Message{{
		ID:        "long-1",
		ChatJID:   "project-alpha@g.us",
		SenderJID: "self@s.whatsapp.net",
		Text: strings.Repeat(
			"But your doesn't depend on it, tu ubi ka end product dekh raha, the foundations are done already. ",
			4,
		),
		Timestamp: time.Date(2026, 4, 5, 18, 0, 0, 0, time.UTC),
		FromMe:    true,
		Receipt:   domain.ReceiptStateRead,
		IsGroup:   true,
	}}

	view := m.View()
	assertViewFitsWidth(t, view, m.width)
	assertViewFitsHeight(t, view, m.height)
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

	msg := loadChatsCmd(repo, "alice", 0)()
	updated, _ := m.Update(msg)
	model := updated.(Model)

	if len(model.chats) != 1 {
		t.Fatalf("len(model.chats) = %d, want 1", len(model.chats))
	}
	if model.chats[0].Title != "Alice Mercer" {
		t.Fatalf("model.chats[0].Title = %q, want Alice Mercer", model.chats[0].Title)
	}
}

func TestChatListRequiresEscapeBeforeQuit(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 96
	m.height = 24
	m.ready = true

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	model := updated.(Model)
	if cmd != nil {
		t.Fatal("did not expect q to quit without escape")
	}
	if model.quitArmed {
		t.Fatal("did not expect quit to stay armed after q")
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if !model.quitArmed {
		t.Fatal("expected escape to arm quit from the chat list")
	}

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	_ = updated.(Model)
	assertQuitCmd(t, cmd)
}

func TestThreadEscapeReturnsToChatListWithoutArmingQuit(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 96
	m.height = 24
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model := updated.(Model)
	if model.mode != viewChats {
		t.Fatalf("mode = %v, want viewChats", model.mode)
	}
	if model.quitArmed {
		t.Fatal("did not expect thread escape to arm quit")
	}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatal("did not expect q to quit after thread escape")
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if !model.quitArmed {
		t.Fatal("expected chat-list escape to arm quit")
	}
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	_ = updated.(Model)
	assertQuitCmd(t, cmd)
}

func TestThreadEscapeCanArmQuitWhenConfigured(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)}).WithQuitAfterNavigation(true)
	m.width = 96
	m.height = 24
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model := updated.(Model)
	if !model.quitArmed {
		t.Fatal("expected configured thread escape to arm quit")
	}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	_ = updated.(Model)
	assertQuitCmd(t, cmd)
}

func TestSearchTypingQDoesNotQuit(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 96
	m.height = 24
	m.ready = true
	m.searching = true
	m.search.Focus()
	m.search.SetValue("al")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	model := updated.(Model)
	if got := model.search.Value(); got != "alq" {
		t.Fatalf("search value = %q, want %q", got, "alq")
	}
	if !model.searching {
		t.Fatal("expected search mode to stay active while typing")
	}
	if cmd == nil {
		t.Fatal("expected search update command")
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if model.searching {
		t.Fatal("expected escape to exit search mode")
	}
	if model.quitArmed {
		t.Fatal("did not expect search escape to arm quit")
	}

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatal("did not expect q to quit after search escape")
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if !model.quitArmed {
		t.Fatal("expected chat-list escape to arm quit")
	}
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	_ = updated.(Model)
	assertQuitCmd(t, cmd)
}

func TestSearchBarRendersAsSingleStaticLineWhileTyping(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 96
	m.height = 26
	m.ready = true
	m.chats = makeChatSummaries(12)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = updated.(Model)
	for _, r := range "Harsh" {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(Model)
	}

	view := m.View()
	assertViewFitsWidth(t, view, m.width)
	assertViewAvoidsAutowrapColumn(t, view, m.width, m.height)
	clean := ansiPattern.ReplaceAllString(view, "")
	if got := strings.Count(clean, "Search:"); got != 1 {
		t.Fatalf("Search: occurrences = %d, want 1:\n%s", got, clean)
	}
	if !strings.Contains(clean, "Search: Harsh") {
		t.Fatalf("view missing current search value:\n%s", clean)
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

	msg := loadChatsCmd(repo, "bob", 0)()
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

func TestLoadChatsCmdReturnsAllChatsNewestFirst(t *testing.T) {
	t.Parallel()

	repo, err := appstore.New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ctx := context.Background()
	base := time.Date(2026, 4, 5, 18, 0, 0, 0, time.UTC)
	const seeded = 6
	for i := 0; i < seeded; i++ {
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

	msg := loadChatsCmd(repo, "", 0)()
	loaded, ok := msg.(chatsLoadedMsg)
	if !ok {
		t.Fatalf("loadChatsCmd() type = %T, want chatsLoadedMsg", msg)
	}
	if loaded.err != nil {
		t.Fatalf("loadChatsCmd() error = %v", loaded.err)
	}
	if len(loaded.chats) != seeded {
		t.Fatalf("len(loaded.chats) = %d, want %d", len(loaded.chats), seeded)
	}
	if loaded.chats[0].Title != "User 5" {
		t.Fatalf("loaded.chats[0].Title = %q, want User 5 (newest)", loaded.chats[0].Title)
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
	updated, _ = model.Update(result)
	model = updated.(Model)
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
	updated, _ = model.Update(result)
	model = updated.(Model)
	if model.composer.Value() != "" {
		t.Fatalf("composer value = %q, want cleared", model.composer.Value())
	}
}

func TestThreadComposeCtrlVStagesClipboardImage(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	transport := &fakeTransport{events: make(chan domain.Event, 1)}
	clipboard := &fakeClipboard{image: []byte{0x89, 'P', 'N', 'G'}}
	m := NewModel(repo, transport).WithClipboard(clipboard)
	m.width = 96
	m.height = 26
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.composing = true

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	model := updated.(Model)
	if cmd == nil {
		t.Fatal("expected clipboard image command")
	}

	msg := cmd()
	result, ok := msg.(attachmentStagedMsg)
	if !ok {
		t.Fatalf("cmd() type = %T, want attachmentStagedMsg", msg)
	}
	if result.err != nil {
		t.Fatalf("paste result error = %v", result.err)
	}
	updated, _ = model.Update(result)
	model = updated.(Model)
	if transport.sentImage != "" {
		t.Fatalf("clipboard image was sent immediately: %q", transport.sentImage)
	}
	if !strings.Contains(model.composer.Value(), "[Image #1]") {
		t.Fatalf("composer value = %q, want staged image token", model.composer.Value())
	}
	if len(model.pendingAttachments) != 1 || model.pendingAttachments[0].kind != domain.MediaKindImage {
		t.Fatalf("pendingAttachments = %#v, want one image attachment", model.pendingAttachments)
	}
}

func TestThreadComposeEnterSendsStagedClipboardImage(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	transport := &fakeTransport{events: make(chan domain.Event, 1)}
	clipboard := &fakeClipboard{image: []byte{0x89, 'P', 'N', 'G'}}
	m := NewModel(repo, transport).WithClipboard(clipboard)
	m.width = 96
	m.height = 26
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.composing = true

	stageMsg := stageClipboardImageCmd(clipboard, "[Image #1]")()
	updated, _ := m.Update(stageMsg)
	model := updated.(Model)
	model.composer.SetValue("[Image #1] sprint update")

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("expected staged attachment send command")
	}
	msg := cmd()
	result, ok := msg.(opResultMsg)
	if !ok {
		t.Fatalf("cmd() type = %T, want opResultMsg", msg)
	}
	if result.err != nil {
		t.Fatalf("send result error = %v", result.err)
	}
	if transport.sentImage == "" {
		t.Fatal("expected staged image to be sent")
	}
	if transport.sentCaption != "sprint update" {
		t.Fatalf("sentCaption = %q, want sprint update", transport.sentCaption)
	}
	updated, _ = model.Update(result)
	model = updated.(Model)
	if model.composer.Value() != "" {
		t.Fatalf("composer value = %q, want cleared", model.composer.Value())
	}
	if len(model.pendingAttachments) != 0 {
		t.Fatalf("pendingAttachments = %#v, want cleared", model.pendingAttachments)
	}
}

func TestThreadComposeAltVStagesVoiceNote(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	transport := &fakeTransport{events: make(chan domain.Event, 1)}
	recorder := &fakeVoiceRecorder{path: filepath.Join(t.TempDir(), "voice.ogg"), duration: 3 * time.Second}
	if err := os.WriteFile(recorder.path, []byte("voice"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	m := NewModel(repo, transport).WithClipboard(&fakeClipboard{}).WithSounder(&fakeSounder{}).WithRecorder(recorder).WithDownloadDir(t.TempDir())
	m.width = 96
	m.height = 26
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.composing = true

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}, Alt: true})
	model := updated.(Model)
	if !recorder.started || !model.recordingVoice {
		t.Fatalf("voice recorder did not start: started=%v recordingVoice=%v", recorder.started, model.recordingVoice)
	}
	model.recordingSince = time.Now().Add(-time.Second)

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}, Alt: true})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("expected stop recording command")
	}
	msg := cmd()
	stageResult, ok := msg.(attachmentStagedMsg)
	if !ok {
		t.Fatalf("cmd() type = %T, want attachmentStagedMsg", msg)
	}
	if stageResult.err != nil {
		t.Fatalf("stageResult.err = %v", stageResult.err)
	}
	updated, _ = model.Update(stageResult)
	model = updated.(Model)
	if !recorder.stopped {
		t.Fatal("voice recorder was not stopped")
	}
	if !strings.Contains(model.composer.Value(), "[Voice #1]") {
		t.Fatalf("composer value = %q, want voice token", model.composer.Value())
	}
	if len(model.pendingAttachments) != 1 || model.pendingAttachments[0].kind != domain.MediaKindVoice {
		t.Fatalf("pendingAttachments = %#v, want one voice attachment", model.pendingAttachments)
	}
}

func TestThreadComposeEnterSendsStagedVoiceNote(t *testing.T) {
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
	m.pendingAttachments = []stagedAttachment{{token: "[Voice #1]", path: "/tmp/voice.ogg", kind: domain.MediaKindVoice, secs: 4 * time.Second}}
	m.composer.SetValue("[Voice #1]")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model := updated.(Model)
	if cmd == nil {
		t.Fatal("expected voice send command")
	}
	msg := cmd()
	result, ok := msg.(opResultMsg)
	if !ok {
		t.Fatalf("cmd() type = %T, want opResultMsg", msg)
	}
	if result.err != nil {
		t.Fatalf("send result error = %v", result.err)
	}
	if transport.sentVoice != "/tmp/voice.ogg" {
		t.Fatalf("sentVoice = %q, want /tmp/voice.ogg", transport.sentVoice)
	}
	if transport.sentVoiceDuration != 4*time.Second {
		t.Fatalf("sentVoiceDuration = %v, want 4s", transport.sentVoiceDuration)
	}
	updated, _ = model.Update(result)
	model = updated.(Model)
	if len(model.pendingAttachments) != 0 {
		t.Fatalf("pendingAttachments = %#v, want cleared", model.pendingAttachments)
	}
}

func TestThreadComposeSendsGenericMediaCommand(t *testing.T) {
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
	m.composer.SetValue(`/media "/tmp/demo.pdf" :: sprint brief`)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model := updated.(Model)
	if cmd == nil {
		t.Fatal("expected media send command")
	}
	msg := cmd()
	result, ok := msg.(opResultMsg)
	if !ok {
		t.Fatalf("cmd() type = %T, want opResultMsg", msg)
	}
	if result.err != nil {
		t.Fatalf("send result error = %v", result.err)
	}
	if transport.sentMedia != "/tmp/demo.pdf" {
		t.Fatalf("sentMedia = %q, want /tmp/demo.pdf", transport.sentMedia)
	}
	if transport.sentCaption != "sprint brief" {
		t.Fatalf("sentCaption = %q, want sprint brief", transport.sentCaption)
	}
	updated, _ = model.Update(result)
	model = updated.(Model)
	if model.composer.Value() != "" {
		t.Fatalf("composer value = %q, want cleared", model.composer.Value())
	}
}

func TestThreadComposeTypingQDoesNotQuit(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 96
	m.height = 28
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.composing = true
	m.composer.Focus()
	m.composer.SetValue("hello")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	model := updated.(Model)
	if got := model.composer.Value(); got != "helloq" {
		t.Fatalf("composer value = %q, want %q", got, "helloq")
	}
	if cmd == nil {
		t.Fatal("expected composer update command")
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if model.composing {
		t.Fatal("expected escape to leave compose mode")
	}
	if model.quitArmed {
		t.Fatal("did not expect compose escape to arm quit")
	}

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatal("did not expect q to quit after compose escape")
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if model.mode != viewChats {
		t.Fatalf("mode = %v, want viewChats", model.mode)
	}
	if model.quitArmed {
		t.Fatal("did not expect thread escape after compose to arm quit")
	}

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatal("did not expect q to quit after returning to chat list")
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if !model.quitArmed {
		t.Fatal("expected chat-list escape to arm quit")
	}
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	_ = updated.(Model)
	assertQuitCmd(t, cmd)
}

func TestThreadComposeCtrlOOpensFilePickerAndStagesAttachment(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	transport := &fakeTransport{events: make(chan domain.Event, 1)}
	dir := t.TempDir()
	target := filepath.Join(dir, "brief.pdf")
	if err := os.WriteFile(target, []byte("brief"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	m := NewModel(repo, transport)
	m.width = 96
	m.height = 28
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.composing = true
	m.filePickerDir = dir

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	model := updated.(Model)
	if !model.filePickerOpen {
		t.Fatal("expected file picker to open")
	}
	if len(model.filePickerEntries) == 0 {
		t.Fatal("expected file picker entries")
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if model.filePickerOpen {
		t.Fatal("expected file picker to close after selecting file")
	}
	if len(model.pendingAttachments) != 1 {
		t.Fatalf("pendingAttachments = %#v, want 1 attachment", model.pendingAttachments)
	}
	if model.pendingAttachments[0].path != target {
		t.Fatalf("pending attachment path = %q, want %q", model.pendingAttachments[0].path, target)
	}
	if !strings.Contains(model.composer.Value(), "brief.pdf") {
		t.Fatalf("composer value = %q, want attachment token with file name", model.composer.Value())
	}
}

func TestFilePickerEscapeDoesNotArmQuit(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 96
	m.height = 28
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.composing = true
	m.filePickerOpen = true
	m.filePickerEntries = []filePickerEntry{{name: "brief.pdf", path: "/tmp/brief.pdf"}}
	m.composer.Focus()
	m.composer.SetValue("draft")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model := updated.(Model)
	if model.filePickerOpen {
		t.Fatal("expected escape to close the file picker")
	}
	if model.quitArmed {
		t.Fatal("did not expect file picker escape to arm quit while composing")
	}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	model = updated.(Model)
	if got := model.composer.Value(); got != "draftq" {
		t.Fatalf("composer value = %q, want %q", got, "draftq")
	}
	if cmd == nil {
		t.Fatal("expected composer update command")
	}
}

func TestThreadComposePlusInsertsLiteralPlus(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	transport := &fakeTransport{events: make(chan domain.Event, 1)}

	m := NewModel(repo, transport)
	m.width = 96
	m.height = 28
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.composing = true

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'+'}})
	model := updated.(Model)
	if model.filePickerOpen {
		t.Fatal("did not expect file picker to open")
	}
	if model.composer.Value() != "+" {
		t.Fatalf("composer value = %q, want \"+\"", model.composer.Value())
	}
}

func TestThreadComposeCtrlJInsertsNewline(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	transport := &fakeTransport{events: make(chan domain.Event, 1)}

	m := NewModel(repo, transport)
	m.width = 96
	m.height = 28
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.composing = true
	m.composer.SetValue("hello")
	m.composer.SetCursor(5)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	model := updated.(Model)
	if got := model.composer.Value(); got != "hello\n" {
		t.Fatalf("composer value = %q, want %q", got, "hello\n")
	}
}

func TestThreadComposeExpandsForMultilineDraft(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	transport := &fakeTransport{events: make(chan domain.Event, 1)}

	m := NewModel(repo, transport)
	m.width = 96
	m.height = 28
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.composing = true
	m.composer.Focus()
	m.composer.SetValue("hello\nmy name is Chirag\n- hello")

	m.resizeComposer(80, 8)
	view := m.composerBody(80)
	if !strings.Contains(view, "hello") || !strings.Contains(view, "my name is Chirag") || !strings.Contains(view, "- hello") {
		t.Fatalf("thread view missing multiline draft:\n%s", view)
	}
	if got := m.composer.Height(); got != 3 {
		t.Fatalf("composer height = %d, want exactly 3 rows for a 3-line draft", got)
	}

	// An empty draft needs a single row: one prompt arrow, not three.
	m.composer.SetValue("")
	m.resizeComposer(80, 8)
	if got := m.composer.Height(); got != 1 {
		t.Fatalf("composer height = %d, want 1 for empty draft", got)
	}
}

func TestThreadComposeShowsPathSuggestionsForMediaCommand(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	transport := &fakeTransport{events: make(chan domain.Event, 1)}
	dir := t.TempDir()
	mediaDir := filepath.Join(dir, "media")
	if err := os.MkdirAll(mediaDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	target := filepath.Join(mediaDir, "demo.pdf")
	if err := os.WriteFile(target, []byte("demo"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	m := NewModel(repo, transport)
	m.width = 96
	m.height = 28
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.composing = true
	m.composer.SetValue(fmt.Sprintf(`/media "%s`, filepath.Join(mediaDir, "de")))
	m.refreshPathSuggestions()

	if len(m.pathSuggestions) == 0 {
		t.Fatalf("pathSuggestions = %#v, want non-empty", m.pathSuggestions)
	}
	if !strings.Contains(m.pathSuggestions[0].label, "demo.pdf") {
		t.Fatalf("first suggestion = %#v, want demo.pdf", m.pathSuggestions[0])
	}

	view := m.View()
	if !strings.Contains(view, "Paths") || !strings.Contains(view, "demo.pdf") {
		t.Fatalf("view missing path suggestions:\n%s", view)
	}
}

func TestThreadComposeTabAppliesSelectedMediaSuggestion(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	transport := &fakeTransport{events: make(chan domain.Event, 1)}
	dir := t.TempDir()
	target := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(target, []byte("notes"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	m := NewModel(repo, transport)
	m.width = 96
	m.height = 28
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.composing = true
	m.composer.SetValue(fmt.Sprintf(`/media "%s" :: sprint brief`, filepath.Join(dir, "no")))
	m.refreshPathSuggestions()
	if len(m.pathSuggestions) == 0 {
		t.Fatal("expected path suggestions")
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	model := updated.(Model)
	if !model.pathSuggestionFocus {
		t.Fatal("expected path suggestion focus after first tab")
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	want := fmt.Sprintf(`/media "%s" :: sprint brief`, target)
	if model.composer.Value() != want {
		t.Fatalf("composer value = %q, want %q", model.composer.Value(), want)
	}
}

func TestThreadComposeSuggestionFocusUsesJKWithoutBreakingTyping(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	transport := &fakeTransport{events: make(chan domain.Event, 1)}
	dir := t.TempDir()
	first := filepath.Join(dir, "alpha.txt")
	second := filepath.Join(dir, "beta.txt")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("demo"), 0o600); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}

	m := NewModel(repo, transport)
	m.width = 96
	m.height = 28
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.composing = true
	m.composer.Focus()
	m.composer.SetValue(fmt.Sprintf(`/media "%s`, dir+string(os.PathSeparator)))
	m.refreshPathSuggestions()
	if len(m.pathSuggestions) < 2 {
		t.Fatalf("pathSuggestions = %#v, want at least 2", m.pathSuggestions)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	model := updated.(Model)
	if !model.pathSuggestionFocus {
		t.Fatal("expected suggestion focus after tab")
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	model = updated.(Model)
	if model.pathSuggestionIdx != 1 {
		t.Fatalf("pathSuggestionIdx = %d, want 1", model.pathSuggestionIdx)
	}

	model.pathSuggestionFocus = false
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	model = updated.(Model)
	if !strings.Contains(model.composer.Value(), "j") {
		t.Fatalf("composer value = %q, want typed j preserved", model.composer.Value())
	}
}

func TestMessagesLoadedEmptyThreadRequestsHistory(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	transport := &fakeTransport{events: make(chan domain.Event, 1)}
	m := NewModel(repo, transport)
	m.width = 96
	m.height = 28
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "devesh@s.whatsapp.net"

	updated, cmd := m.Update(messagesLoadedMsg{chatJID: "devesh@s.whatsapp.net", messages: nil})
	model := updated.(Model)
	if !model.threadHistoryPending {
		t.Fatal("expected thread history request to be marked pending")
	}
	if cmd == nil {
		t.Fatal("expected history request command")
	}
	msg := cmd()
	result, ok := msg.(opResultMsg)
	if !ok {
		t.Fatalf("cmd() type = %T, want opResultMsg", msg)
	}
	if result.err != nil {
		t.Fatalf("history request result error = %v", result.err)
	}
	if transport.historyChat != "devesh@s.whatsapp.net" || transport.historyCount != 50 {
		t.Fatalf("history request = %q/%d, want devesh@s.whatsapp.net/50", transport.historyChat, transport.historyCount)
	}
}

func TestThreadDownloadsLatestMedia(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	transport := &fakeTransport{events: make(chan domain.Event, 1)}
	m := NewModel(repo, transport).WithClipboard(&fakeClipboard{}).WithSounder(&fakeSounder{}).WithRecorder(&fakeVoiceRecorder{}).WithDownloadDir(t.TempDir())
	m.width = 96
	m.height = 26
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.messages = []domain.Message{
		{ID: "demo-1", ChatJID: "project-alpha@g.us", Text: "hello"},
		{ID: "demo-2", ChatJID: "project-alpha@g.us", Text: "[image] board.png", MediaKind: domain.MediaKindImage, MediaDirectPath: "/media/demo"},
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	_ = updated.(Model)
	if cmd == nil {
		t.Fatal("expected download command")
	}
	msg := cmd()
	result, ok := msg.(opResultMsg)
	if !ok {
		t.Fatalf("cmd() type = %T, want opResultMsg", msg)
	}
	if result.err != nil {
		t.Fatalf("download result error = %v", result.err)
	}
	if transport.downloadedMessage != "demo-2" {
		t.Fatalf("downloadedMessage = %q, want demo-2", transport.downloadedMessage)
	}
}

func TestTransportNotifyRingsBell(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	sound := &fakeSounder{}
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)}).WithClipboard(&fakeClipboard{}).WithSounder(sound)
	m.width = 96
	m.height = 26
	m.ready = true

	updated, cmd := m.Update(transportEventMsg{event: domain.Event{Type: domain.EventChatUpdate, ChatJID: "alice@s.whatsapp.net", Notify: true}})
	_ = updated.(Model)
	if cmd == nil {
		t.Fatal("expected notification command")
	}

	_ = cmd()
	if sound.bells != 1 {
		t.Fatalf("sound.bells = %d, want 1", sound.bells)
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

func TestThreadScrollsMessageWindow(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 96
	m.height = 26
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.messages = makeThreadMessages(m.currentChatID, 20)
	m.threadMessageLimit = messageLimit

	body := ansiPattern.ReplaceAllString(m.threadBody(5, 80), "")
	if !strings.Contains(body, "Message 20") {
		t.Fatalf("initial body missing latest message:\n%s", body)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	model := updated.(Model)
	if model.threadScroll != 1 {
		t.Fatalf("threadScroll = %d, want 1", model.threadScroll)
	}
	body = ansiPattern.ReplaceAllString(model.threadBody(5, 80), "")
	if strings.Contains(body, "Message 20") || !strings.Contains(body, "Message 19") {
		t.Fatalf("scrolled body = %q, want previous window without latest", body)
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	model = updated.(Model)
	if model.threadScroll != 0 {
		t.Fatalf("threadScroll after down = %d, want 0", model.threadScroll)
	}
}

func TestThreadScrollRequestsHistoryAtOldestMessage(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	transport := &fakeTransport{events: make(chan domain.Event, 1)}
	m := NewModel(repo, transport)
	m.width = 96
	m.height = 26
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.messages = makeThreadMessages(m.currentChatID, 1)
	m.threadMessageLimit = messageLimit

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	model := updated.(Model)
	if !model.threadHistoryPending {
		t.Fatal("expected history request to be pending")
	}
	if cmd == nil {
		t.Fatal("expected history request command")
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
	if transport.historyCount != historyRequestCount {
		t.Fatalf("historyCount = %d, want %d", transport.historyCount, historyRequestCount)
	}
}

func TestThreadScrollLoadsOlderCachedMessages(t *testing.T) {
	t.Parallel()

	repo, err := appstore.New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	ctx := context.Background()
	chatJID := "project-alpha@g.us"
	for _, msg := range makeThreadMessages(chatJID, messageLimit+5) {
		if err := repo.RecordMessage(ctx, msg, false); err != nil {
			t.Fatalf("RecordMessage(%s) error = %v", msg.ID, err)
		}
	}
	initial, err := repo.ListMessages(ctx, chatJID, messageLimit)
	if err != nil {
		t.Fatalf("ListMessages(initial) error = %v", err)
	}
	if len(initial) != messageLimit {
		t.Fatalf("len(initial) = %d, want %d", len(initial), messageLimit)
	}

	transport := &fakeTransport{events: make(chan domain.Event, 1)}
	m := NewModel(repo, transport)
	m.width = 96
	m.height = 26
	m.ready = true
	m.mode = viewThread
	m.currentChatID = chatJID
	m.messages = initial
	m.threadMessageLimit = messageLimit
	m.threadScroll = m.maxThreadScroll()

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	model := updated.(Model)
	if !model.threadLoadingOlder {
		t.Fatal("expected older cached load to be pending")
	}
	if model.threadMessageLimit != messageLimit+messagePageSize {
		t.Fatalf("threadMessageLimit = %d, want %d", model.threadMessageLimit, messageLimit+messagePageSize)
	}
	if cmd == nil {
		t.Fatal("expected load messages command")
	}

	msg := cmd()
	loaded, ok := msg.(messagesLoadedMsg)
	if !ok {
		t.Fatalf("cmd() type = %T, want messagesLoadedMsg", msg)
	}
	if len(loaded.messages) != messageLimit+5 {
		t.Fatalf("len(loaded.messages) = %d, want %d", len(loaded.messages), messageLimit+5)
	}

	updated, _ = model.Update(loaded)
	model = updated.(Model)
	if len(model.messages) != messageLimit+5 {
		t.Fatalf("len(model.messages) = %d, want %d", len(model.messages), messageLimit+5)
	}
	if model.threadLoadingOlder {
		t.Fatal("expected older cached load to complete")
	}
	if transport.historyChat != "" {
		t.Fatalf("historyChat = %q, want no network history request", transport.historyChat)
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
	if !strings.Contains(view, "Latest preview") || !strings.Contains(view, "Project Alpha") {
		t.Fatalf("view missing split-pane preview header:\n%s", view)
	}
}

func TestChatListShowsLoaderWhileRecentSyncIsRunning(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 84
	m.height = 24
	m.ready = true
	m.chats = nil
	m.status = "Waiting for recent chats from your phone..."
	m.syncingRecent = true

	view := m.View()
	assertViewFitsWidth(t, view, m.width)
	if !strings.Contains(view, "Syncing recent chats") {
		t.Fatalf("view missing sync loader:\n%s", view)
	}
	if strings.Contains(view, "No cached chats yet") {
		t.Fatalf("view fell back to empty-cache copy during sync:\n%s", view)
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
	msg := loadChatsCmd(repo, "h", 0)()
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

	view := m.renderFooter("j/k move  enter open  / search  r refresh  esc then q quit")
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

func makeThreadMessages(chatJID string, count int) []domain.Message {
	base := time.Date(2026, 4, 5, 18, 0, 0, 0, time.UTC)
	messages := make([]domain.Message, 0, count)
	for i := 0; i < count; i++ {
		messages = append(messages, domain.Message{
			ID:        fmt.Sprintf("thread-%03d", i+1),
			ChatJID:   chatJID,
			SenderJID: chatJID,
			Text:      fmt.Sprintf("Message %d", i+1),
			Timestamp: base.Add(time.Duration(i) * time.Minute),
			Receipt:   domain.ReceiptStateReceived,
			IsGroup:   strings.HasSuffix(chatJID, "@g.us"),
		})
	}
	return messages
}

func makeChatSummaries(count int) []domain.ChatSummary {
	base := time.Date(2026, 4, 5, 18, 0, 0, 0, time.UTC)
	chats := make([]domain.ChatSummary, 0, count)
	for i := 0; i < count; i++ {
		chats = append(chats, domain.ChatSummary{
			JID:                fmt.Sprintf("chat-%02d@s.whatsapp.net", i),
			Title:              fmt.Sprintf("Chat %02d", i),
			LastMessagePreview: fmt.Sprintf("Preview %02d", i),
			LastMessageAt:      base.Add(-time.Duration(i) * time.Minute),
		})
	}
	return chats
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

func assertViewFitsHeight(t *testing.T, view string, height int) {
	t.Helper()

	if got := countRenderedLines(view); got > height {
		t.Fatalf("view height %d exceeds %d:\n%s", got, height, view)
	}
}

func assertViewAvoidsAutowrapColumn(t *testing.T, view string, terminalWidth, height int) {
	t.Helper()

	lines := strings.Split(view, "\n")
	if len(lines) != height {
		t.Fatalf("view height = %d, want %d:\n%s", len(lines), height, view)
	}
	maxWidth := terminalWidth - 1
	for idx, line := range lines {
		clean := ansiPattern.ReplaceAllString(line, "")
		if got := lipgloss.Width(clean); got > maxWidth {
			t.Fatalf("line %d width = %d, want <= %d: %q", idx+1, got, maxWidth, clean)
		}
	}
}

func TestClearPendingAttachmentsKeepsUserPickedFiles(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	dir := t.TempDir()
	userFile := filepath.Join(dir, "brief.pdf")
	tempFile := filepath.Join(dir, "clipboard.png")
	for _, path := range []string{userFile, tempFile} {
		if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}

	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 96
	m.height = 28
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.composing = true
	m.pendingAttachments = []stagedAttachment{
		{token: "[File brief.pdf]", path: userFile, kind: domain.MediaKindDocument},
		{token: "[Image #1]", path: tempFile, kind: domain.MediaKindImage, temp: true},
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model := updated.(Model)
	if len(model.pendingAttachments) != 0 {
		t.Fatalf("pendingAttachments = %#v, want cleared", model.pendingAttachments)
	}
	if _, err := os.Stat(userFile); err != nil {
		t.Fatalf("user-picked file was removed on cancel: %v", err)
	}
	if _, err := os.Stat(tempFile); !os.IsNotExist(err) {
		t.Fatalf("temp attachment file should be removed on cancel, stat err = %v", err)
	}
}

func TestVoiceStopErrorReleasesStoppingLatch(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 96
	m.height = 28
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.composing = true
	m.stoppingVoice = true

	updated, _ := m.Update(attachmentStagedMsg{err: fmt.Errorf("gst-launch died")})
	model := updated.(Model)
	if model.stoppingVoice {
		t.Fatal("expected failed voice stop to release the stopping latch")
	}
	if model.lastErr == "" {
		t.Fatal("expected the stop error to surface in lastErr")
	}
}

func TestTruncateTextMeasuresDisplayWidth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		text  string
		width int
	}{
		{name: "emoji", text: "👍👍👍👍", width: 5},
		{name: "cjk", text: "你好世界你好世界", width: 7},
		{name: "mixed", text: "call 📞 me later today", width: 9},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := truncateText(tt.text, tt.width)
			if w := lipgloss.Width(got); w > tt.width {
				t.Fatalf("truncateText(%q, %d) width = %d, want <= %d (got %q)", tt.text, tt.width, w, tt.width, got)
			}
			if !strings.HasSuffix(got, "…") {
				t.Fatalf("truncateText(%q, %d) = %q, want ellipsis suffix", tt.text, tt.width, got)
			}
		})
	}
}

func TestTruncateTextHeadKeepsTail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		text  string
		width int
		want  string
	}{
		{name: "short passes through", text: "ab", width: 4, want: "ab"},
		{name: "long keeps tail", text: "abcdef", width: 4, want: "…def"},
		{name: "cjk tail", text: "你好世界", width: 5, want: "…世界"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := truncateTextHead(tt.text, tt.width); got != tt.want {
				t.Fatalf("truncateTextHead(%q, %d) = %q, want %q", tt.text, tt.width, got, tt.want)
			}
		})
	}
}

func TestRenderHeaderStaysSingleLineForLongThreadTitles(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.ready = true

	for _, width := range []int{42, 60, 96} {
		m.width = width
		header := m.renderHeader("Project Alpha Extended Launch Planning Group", "")
		lines := strings.Split(header, "\n")
		if len(lines) != 3 {
			t.Fatalf("width %d: header rendered %d lines, want 3 (blank, title, rule):\n%s", width, len(lines), header)
		}
		clean := ansiPattern.ReplaceAllString(lines[1], "")
		if got := lipgloss.Width(clean); got > max(40, width-2) {
			t.Fatalf("width %d: header line width = %d, want <= %d: %q", width, got, max(40, width-2), clean)
		}
	}
}

func TestCycleSelectionWrapsBothDirections(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		selected int
		delta    int
		total    int
		want     int
	}{
		{name: "down", selected: 0, delta: 1, total: 3, want: 1},
		{name: "down wraps", selected: 2, delta: 1, total: 3, want: 0},
		{name: "up wraps", selected: 0, delta: -1, total: 3, want: 2},
		{name: "empty list", selected: 0, delta: -1, total: 0, want: 0},
		{name: "out of range normalizes", selected: 5, delta: 1, total: 3, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := cycleSelection(tt.selected, tt.delta, tt.total); got != tt.want {
				t.Fatalf("cycleSelection(%d, %d, %d) = %d, want %d", tt.selected, tt.delta, tt.total, got, tt.want)
			}
		})
	}
}

func TestCtrlLForcesFullRepaint(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 96
	m.height = 28
	m.ready = true

	for _, mode := range []viewMode{viewChats, viewThread} {
		m.mode = mode
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlL})
		if cmd == nil {
			t.Fatalf("mode %d: expected a clear-screen command from ctrl+l", mode)
		}
		if got := fmt.Sprintf("%T", cmd()); !strings.Contains(got, "clearScreenMsg") {
			t.Fatalf("mode %d: cmd() type = %s, want bubbletea clearScreenMsg", mode, got)
		}
	}
}

func TestRenderPanelSizeIsFixedRegardlessOfContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
	}{
		{name: "empty", content: ""},
		{name: "short", content: "hello"},
		{name: "unbroken url", content: "https://example.com/" + strings.Repeat("KVe6xeK12Gy7P9NKD6c-", 20)},
		{name: "too many lines", content: strings.Repeat("line\n", 40)},
		{name: "wide and tall", content: strings.Repeat(strings.Repeat("x", 200)+"\n", 40)},
	}
	const totalWidth, totalHeight = 40, 10
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			panel := renderPanel(boxStyle, totalWidth, totalHeight, tt.content)
			lines := strings.Split(panel, "\n")
			if len(lines) != totalHeight {
				t.Fatalf("panel height = %d lines, want exactly %d:\n%s", len(lines), totalHeight, panel)
			}
			for idx, line := range lines {
				if got := lipgloss.Width(line); got != totalWidth {
					t.Fatalf("panel line %d width = %d, want exactly %d: %q", idx+1, got, totalWidth, line)
				}
			}
		})
	}
}

func TestChatListHeightUnaffectedByLongPreview(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 96
	m.height = 30
	m.ready = true
	m.chats = makeChatSummaries(3)

	baseline := countRenderedLines(m.renderChatList())

	m.chats[0].LastMessagePreview = "https://u3447072.ct.sendgrid.net/ls/click?upn=" + strings.Repeat("2FfA8kUcTfdj2zZbRwWxeemwo7qmqfXC-", 15)
	m.selected = 0
	if got := countRenderedLines(m.renderChatList()); got != baseline {
		t.Fatalf("chat list height changed from %d to %d lines because of a long preview", baseline, got)
	}
}

func TestWrapTextClipsOverlongFirstWord(t *testing.T) {
	t.Parallel()

	got := wrapText(strings.Repeat("x", 100), 20)
	lines := strings.Split(got, "\n")
	if len(lines) != 1 {
		t.Fatalf("wrapText produced %d lines, want 1: %q", len(lines), got)
	}
	if w := lipgloss.Width(lines[0]); w > 20 {
		t.Fatalf("wrapped line width = %d, want <= 20: %q", w, lines[0])
	}
}

func TestThreadScrollReachesEveryLineOfTallMessage(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 96
	m.height = 26
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"

	// One message far taller than any viewport: 40 numbered lines.
	var b strings.Builder
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&b, "line-%02d\n", i)
	}
	m.messages = []domain.Message{{
		ID:        "tall",
		ChatJID:   m.currentChatID,
		SenderJID: "alice@s.whatsapp.net",
		Text:      strings.TrimSpace(b.String()),
		Timestamp: time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC),
		Receipt:   domain.ReceiptStateReceived,
	}}
	m.threadMessageLimit = messageLimit

	// Use the same viewport geometry the app renders with, so the scroll
	// ceiling and the visible window agree.
	layout := m.threadLayout()
	window := paddedContentHeight(layout.messageHeight)
	width := layout.contentWidth - boxStyle.GetHorizontalFrameSize()
	seen := make(map[string]bool)
	for scroll := 0; scroll <= m.maxThreadScroll(); scroll++ {
		m.threadScroll = scroll
		body := ansiPattern.ReplaceAllString(m.threadBody(window, width), "")
		if got := countRenderedLines(body); got != window {
			t.Fatalf("scroll %d: window rendered %d lines, want %d:\n%s", scroll, got, window, body)
		}
		for _, line := range strings.Split(body, "\n") {
			seen[strings.TrimSpace(line)] = true
		}
	}
	for i := 1; i <= 40; i++ {
		if !seen[fmt.Sprintf("line-%02d", i)] {
			t.Fatalf("line-%02d was never reachable by scrolling", i)
		}
	}
}

func TestThreadScrollMovesOneLineAtATime(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 96
	m.height = 26
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.messages = makeThreadMessages(m.currentChatID, 20)
	m.threadMessageLimit = messageLimit

	atBottom := ansiPattern.ReplaceAllString(m.threadBody(6, 80), "")
	m.threadScroll = 1
	scrolled := ansiPattern.ReplaceAllString(m.threadBody(6, 80), "")

	bottomLines := strings.Split(atBottom, "\n")
	scrolledLines := strings.Split(scrolled, "\n")
	// Shifting by one line means the scrolled window's tail equals the
	// bottom window's head region, offset by exactly one row.
	for i := 1; i < len(bottomLines); i++ {
		if scrolledLines[i] != bottomLines[i-1] {
			t.Fatalf("scroll by 1 did not shift by one line:\nbottom:\n%s\nscrolled:\n%s", atBottom, scrolled)
		}
	}
}

func selectModeThreadModel(t *testing.T, transport *fakeTransport, messages []domain.Message) Model {
	t.Helper()
	repo := seededRepo(t)
	m := NewModel(repo, transport).WithClipboard(&fakeClipboard{}).WithSounder(&fakeSounder{}).WithRecorder(&fakeVoiceRecorder{}).WithDownloadDir(t.TempDir())
	m.width = 96
	m.height = 26
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.messages = messages
	m.threadMessageLimit = messageLimit
	return m
}

func TestSelectModeDownloadsSelectedMedia(t *testing.T) {
	t.Parallel()

	transport := &fakeTransport{events: make(chan domain.Event, 1)}
	base := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	m := selectModeThreadModel(t, transport, []domain.Message{
		{ID: "s1", ChatJID: "project-alpha@g.us", SenderJID: "a@s.whatsapp.net", Text: "hi", Timestamp: base},
		{ID: "s2", ChatJID: "project-alpha@g.us", SenderJID: "a@s.whatsapp.net", Text: "[image] board.png",
			MediaKind: domain.MediaKindImage, MediaDirectPath: "/wa/media/s2", Timestamp: base.Add(time.Minute)},
		{ID: "s3", ChatJID: "project-alpha@g.us", SenderJID: "a@s.whatsapp.net", Text: "bye", Timestamp: base.Add(2 * time.Minute)},
	})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model := updated.(Model)
	// r lands on the newest (index 2); k moves onto the middle media message.
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	model = updated.(Model)
	if model.selectIndex != 1 {
		t.Fatalf("selectIndex = %d, want 1 (middle media message)", model.selectIndex)
	}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("expected download command")
	}
	result, ok := cmd().(opResultMsg)
	if !ok || result.err != nil {
		t.Fatalf("download result = %#v", result)
	}
	if transport.downloadedMessage != "s2" {
		t.Fatalf("downloadedMessage = %q, want s2", transport.downloadedMessage)
	}
	if !model.selecting {
		t.Fatal("expected to stay in select mode after downloading")
	}
}

func TestSelectModeDownloadRejectsNonMedia(t *testing.T) {
	t.Parallel()

	transport := &fakeTransport{events: make(chan domain.Event, 1)}
	base := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	m := selectModeThreadModel(t, transport, []domain.Message{
		{ID: "t1", ChatJID: "project-alpha@g.us", SenderJID: "a@s.whatsapp.net", Text: "just text", Timestamp: base},
	})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model := updated.(Model)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	model = updated.(Model)
	if cmd != nil {
		t.Fatal("did not expect a download command for a text message")
	}
	if model.lastErr == "" {
		t.Fatal("expected lastErr when downloading a message with no media")
	}
	if transport.downloadedMessage != "" {
		t.Fatalf("downloadedMessage = %q, want none", transport.downloadedMessage)
	}
}

func TestSelectModePlaysSelectedVoice(t *testing.T) {
	t.Parallel()

	player := &fakeAudioPlayer{}
	transport := &fakeTransport{events: make(chan domain.Event, 1)}
	base := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	repo := seededRepo(t)
	m := NewModel(repo, transport).WithPlayer(player).WithDownloadDir(t.TempDir())
	m.width = 96
	m.height = 26
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.messages = []domain.Message{
		{ID: "voice-1", ChatJID: "project-alpha@g.us", SenderJID: "a@s.whatsapp.net", Text: "[voice note]",
			MediaKind: domain.MediaKindVoice, MediaDirectPath: "/wa/media/voice-1", Timestamp: base},
		{ID: "text-1", ChatJID: "project-alpha@g.us", SenderJID: "a@s.whatsapp.net", Text: "later", Timestamp: base.Add(time.Minute)},
	}
	m.threadMessageLimit = messageLimit

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model := updated.(Model)
	// r lands on the newest text message; k moves onto the older voice note.
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	model = updated.(Model)
	if model.selectIndex != 0 {
		t.Fatalf("selectIndex = %d, want 0 (voice note)", model.selectIndex)
	}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("expected playback command")
	}
	result, ok := cmd().(opResultMsg)
	if !ok || result.err != nil {
		t.Fatalf("playback result = %#v", result)
	}
	if transport.downloadedMessage != "voice-1" {
		t.Fatalf("downloadedMessage = %q, want voice-1 downloaded first", transport.downloadedMessage)
	}
	if player.playedPath == "" {
		t.Fatal("expected the selected voice note to be played")
	}
	if !model.selecting {
		t.Fatal("expected to stay in select mode after playing")
	}
}

func TestMediaPickerListsAndDownloads(t *testing.T) {
	t.Parallel()

	transport := &fakeTransport{events: make(chan domain.Event, 1)}
	base := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	m := selectModeThreadModel(t, transport, []domain.Message{
		{ID: "p1", ChatJID: "project-alpha@g.us", SenderJID: "a@s.whatsapp.net", Text: "hi", Timestamp: base},
		{ID: "p2", ChatJID: "project-alpha@g.us", SenderJID: "a@s.whatsapp.net", Text: "[image] one.png",
			MediaKind: domain.MediaKindImage, MediaDirectPath: "/wa/media/p2", Timestamp: base.Add(time.Minute)},
		{ID: "p3", ChatJID: "project-alpha@g.us", SenderJID: "a@s.whatsapp.net", Text: "note", Timestamp: base.Add(2 * time.Minute)},
		{ID: "p4", ChatJID: "project-alpha@g.us", SenderJID: "a@s.whatsapp.net", Text: "[image] two.png",
			MediaKind: domain.MediaKindImage, MediaDirectPath: "/wa/media/p4", Timestamp: base.Add(3 * time.Minute)},
	})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	model := updated.(Model)
	if !model.mediaPickerOpen {
		t.Fatal("expected media picker to open")
	}
	if model.mediaPickerIndex != 1 {
		t.Fatalf("mediaPickerIndex = %d, want 1 (newest media)", model.mediaPickerIndex)
	}

	// k moves to the older media item (p2).
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	model = updated.(Model)
	if model.mediaPickerIndex != 0 {
		t.Fatalf("mediaPickerIndex = %d, want 0 after k", model.mediaPickerIndex)
	}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("expected download command from enter")
	}
	result, ok := cmd().(opResultMsg)
	if !ok || result.err != nil {
		t.Fatalf("download result = %#v", result)
	}
	if transport.downloadedMessage != "p2" {
		t.Fatalf("downloadedMessage = %q, want p2", transport.downloadedMessage)
	}
	if !model.mediaPickerOpen {
		t.Fatal("expected media picker to stay open after downloading")
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if model.mediaPickerOpen {
		t.Fatal("expected esc to close the media picker")
	}
}

func TestMediaPickerRendersRows(t *testing.T) {
	t.Parallel()

	transport := &fakeTransport{events: make(chan domain.Event, 1)}
	base := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	m := selectModeThreadModel(t, transport, []domain.Message{
		{ID: "doc-1", ChatJID: "project-alpha@g.us", SenderJID: "a@s.whatsapp.net", Text: "[document] brief.pdf",
			MediaKind: domain.MediaKindDocument, MediaFileName: "brief.pdf", MediaFileLength: 1572864,
			DownloadedPath: "/tmp/brief.pdf", Timestamp: base},
	})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	model := updated.(Model)
	if !model.mediaPickerOpen {
		t.Fatal("expected media picker to open")
	}

	view := model.View()
	clean := plain(view)
	for _, needle := range []string{"brief.pdf", "✓", "1.5 MB"} {
		if !strings.Contains(clean, needle) {
			t.Fatalf("media picker view missing %q:\n%s", needle, clean)
		}
	}
	assertViewFitsWidth(t, view, model.width)
	assertViewAvoidsAutowrapColumn(t, view, model.width, model.height)
	if got := countRenderedLines(view); got != model.height {
		t.Fatalf("view height = %d, want %d", got, model.height)
	}
}

func TestMediaPickerWithoutMediaSetsError(t *testing.T) {
	t.Parallel()

	transport := &fakeTransport{events: make(chan domain.Event, 1)}
	base := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	m := selectModeThreadModel(t, transport, []domain.Message{
		{ID: "t1", ChatJID: "project-alpha@g.us", SenderJID: "a@s.whatsapp.net", Text: "just text", Timestamp: base},
	})

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	model := updated.(Model)
	if model.mediaPickerOpen {
		t.Fatal("did not expect the media picker to open without media")
	}
	if model.lastErr == "" {
		t.Fatal("expected lastErr when there is no media to browse")
	}
}
