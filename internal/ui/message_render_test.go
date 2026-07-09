package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/chirag/whatsapp-terminal/internal/domain"
)

func plain(s string) string {
	return ansiPattern.ReplaceAllString(s, "")
}

func TestRenderMessageBodyAppliesWhatsAppMarkup(t *testing.T) {
	t.Parallel()

	// Styled output is profile-dependent (lipgloss strips ANSI without a
	// TTY), so assert on the parsed word styles instead of escape codes.
	styledText := func(words []styledWord) string {
		parts := make([]string, 0, len(words))
		for _, w := range words {
			parts = append(parts, w.text)
		}
		return strings.Join(parts, " ")
	}
	tests := []struct {
		name    string
		text    string
		want    string // words joined after formatting (delimiters consumed)
		styled  string // the word expected to carry the style
		hasProp func(lipgloss.Style) bool
	}{
		{name: "bold", text: "this is *bold* text", want: "this is bold text", styled: "bold", hasProp: lipgloss.Style.GetBold},
		{name: "italic", text: "an _italic_ word", want: "an italic word", styled: "italic", hasProp: lipgloss.Style.GetItalic},
		{name: "strike", text: "a ~gone~ word", want: "a gone word", styled: "gone", hasProp: lipgloss.Style.GetStrikethrough},
		{name: "multiword bold", text: "*two words* here", want: "two words here", styled: "words", hasProp: lipgloss.Style.GetBold},
		{name: "unclosed stays raw", text: "2*3 is six", want: "2*3 is six"},
		{name: "space-padded stays raw", text: "a * not bold * b", want: "a * not bold * b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			words := parseMessageWords(tt.text, nil)
			if got := styledText(words); got != tt.want {
				t.Fatalf("parsed body = %q, want %q", got, tt.want)
			}
			if tt.hasProp == nil {
				return
			}
			for _, word := range words {
				if word.text == tt.styled {
					if !tt.hasProp(spanStyle(word.kind)) {
						t.Fatalf("word %q missing expected style attribute", tt.styled)
					}
					return
				}
			}
			t.Fatalf("styled word %q not found in %q", tt.styled, styledText(words))
		})
	}
}

func TestRenderMessageBodyResolvesMentions(t *testing.T) {
	t.Parallel()

	mentions := map[string]string{"41755755978893": "Sarthak Mittal"}
	lines := renderMessageBody("@41755755978893 teri bachpan ki video", mentions, 60)
	body := plain(strings.Join(lines, " "))
	if !strings.Contains(body, "@Sarthak Mittal") {
		t.Fatalf("body = %q, want mention resolved to @Sarthak Mittal", body)
	}
	if strings.Contains(body, "41755755978893") {
		t.Fatalf("body = %q, raw mention id should be replaced", body)
	}

	// Unknown ids keep the raw token rather than dropping it.
	lines = renderMessageBody("@99999999999999 hello", nil, 60)
	if body := plain(strings.Join(lines, " ")); !strings.Contains(body, "@99999999999999") {
		t.Fatalf("body = %q, want unresolved mention kept", body)
	}
}

func TestResolveMentionNamesTriesPhoneThenLIDAlias(t *testing.T) {
	t.Parallel()

	contacts := map[string]string{
		"911111@s.whatsapp.net": "Mom",
		"22233344455@lid":       "Dad",
	}
	lookup := func(jid string) string { return contacts[jid] }
	texts := []string{"@911111 and @22233344455 and @333333"}
	names := resolveMentionNames(lookup, texts)
	if names["911111"] != "Mom" || names["22233344455"] != "Dad" {
		t.Fatalf("names = %#v, want Mom via phone JID and Dad via LID alias", names)
	}
	if _, ok := names["333333"]; ok {
		t.Fatalf("names = %#v, unknown mention must stay unresolved", names)
	}
}

func TestSenderStyleIsStablePerSender(t *testing.T) {
	t.Parallel()

	first := senderStyle("alice@s.whatsapp.net").GetForeground()
	if second := senderStyle("alice@s.whatsapp.net").GetForeground(); second != first {
		t.Fatalf("senderStyle not deterministic: %v vs %v", first, second)
	}
	distinct := map[string]bool{}
	for _, jid := range []string{"a@s.whatsapp.net", "b@s.whatsapp.net", "c@s.whatsapp.net", "d@s.whatsapp.net", "e@s.whatsapp.net"} {
		distinct[fmt.Sprintf("%v", senderStyle(jid).GetForeground())] = true
	}
	if len(distinct) < 2 {
		t.Fatal("expected at least two distinct sender colors across five senders")
	}
}

func TestThreadMessageLinesInsertDateSeparators(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 96
	m.height = 26
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.messages = []domain.Message{
		{ID: "d1", ChatJID: m.currentChatID, SenderJID: "a@s.whatsapp.net", Text: "one",
			Timestamp: time.Date(2026, 7, 6, 10, 0, 0, 0, time.UTC), Receipt: domain.ReceiptStateReceived},
		{ID: "d2", ChatJID: m.currentChatID, SenderJID: "a@s.whatsapp.net", Text: "two",
			Timestamp: time.Date(2026, 7, 7, 9, 0, 0, 0, time.UTC), Receipt: domain.ReceiptStateReceived},
	}

	joined := plain(strings.Join(m.threadMessageLines(80), "\n"))
	if strings.Count(joined, "──") < 2 {
		t.Fatalf("expected two date separators:\n%s", joined)
	}
	// The two messages fall on different days, so two distinct labels.
	if !strings.Contains(joined, "Jul 6") && !strings.Contains(joined, "Yesterday") && !strings.Contains(joined, "Today") {
		t.Fatalf("expected a date label in separators:\n%s", joined)
	}
}

func TestRenderThreadMessageAlignsOwnMessagesRight(t *testing.T) {
	t.Parallel()

	const width = 60
	msg := domain.Message{
		ID:        "m1",
		ChatJID:   "alice@s.whatsapp.net",
		SenderJID: "self@s.whatsapp.net",
		Text:      "short reply",
		Timestamp: time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
		FromMe:    true,
		Receipt:   domain.ReceiptStateRead,
	}
	for idx, line := range strings.Split(renderThreadMessage(msg, width, nil, false), "\n") {
		if strings.TrimSpace(plain(line)) == "" {
			continue
		}
		if got := ansi.StringWidth(line); got != width {
			t.Fatalf("own-message line %d width = %d, want right-aligned to %d: %q", idx, got, width, plain(line))
		}
	}

	msg.FromMe = false
	msg.SenderJID = "alice@s.whatsapp.net"
	msg.SenderName = "Alice"
	msg.Receipt = domain.ReceiptStateReceived
	first := strings.Split(renderThreadMessage(msg, width, nil, false), "\n")[0]
	if strings.HasPrefix(plain(first), " ") {
		t.Fatalf("peer message should stay left-aligned: %q", plain(first))
	}
}

func TestReceiptTicksReplaceSuffixLine(t *testing.T) {
	t.Parallel()

	msg := domain.Message{
		ID:        "m1",
		ChatJID:   "alice@s.whatsapp.net",
		SenderJID: "self@s.whatsapp.net",
		Text:      "hello",
		Timestamp: time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
		FromMe:    true,
		Receipt:   domain.ReceiptStateDelivered,
	}
	rendered := renderThreadMessage(msg, 60, nil, false)
	if lines := strings.Split(rendered, "\n"); len(lines) != 2 {
		t.Fatalf("rendered %d lines, want 2 (header with ticks + body):\n%s", len(lines), plain(rendered))
	}
	if !strings.Contains(plain(rendered), "✓✓") {
		t.Fatalf("rendered message missing delivery ticks:\n%s", plain(rendered))
	}
}

func TestLoadMessagesCmdResolvesMentionsFromStore(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	ctx := context.Background()
	if err := repo.UpsertContact(ctx, domain.Contact{JID: "41755755978893@lid", DisplayName: "Sarthak Mittal"}); err != nil {
		t.Fatalf("UpsertContact() error = %v", err)
	}
	if err := repo.RecordMessage(ctx, domain.Message{
		ID:        "mention-1",
		ChatJID:   "project-alpha@g.us",
		SenderJID: "bob@s.whatsapp.net",
		Text:      "@41755755978893 check this",
		Timestamp: time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC),
		Receipt:   domain.ReceiptStateReceived,
		IsGroup:   true,
	}, false); err != nil {
		t.Fatalf("RecordMessage() error = %v", err)
	}

	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 96
	m.height = 26
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"

	msg := loadMessagesCmd(repo, m.currentChatID, messageLimit)()
	loaded, ok := msg.(messagesLoadedMsg)
	if !ok {
		t.Fatalf("cmd() type = %T, want messagesLoadedMsg", msg)
	}
	if loaded.mentions["41755755978893"] != "Sarthak Mittal" {
		t.Fatalf("mentions = %#v, want mention resolved via LID contact", loaded.mentions)
	}

	updated, _ := m.Update(loaded)
	model := updated.(Model)
	body := plain(model.threadBody(12, 80))
	if !strings.Contains(body, "@Sarthak Mittal") {
		t.Fatalf("thread body = %q, want resolved mention", body)
	}
}

func TestChatListPreviewResolvesMentions(t *testing.T) {
	t.Parallel()

	mentions := map[string]string{"41755755978893": "Sarthak Mittal"}
	chat := domain.ChatSummary{
		JID:                "group@g.us",
		Title:              "Placement Group",
		LastMessagePreview: "@41755755978893 teri bachpan ki video",
		LastSenderName:     "Sahil",
		IsGroup:            true,
		LastMessageAt:      time.Date(2026, 7, 7, 14, 32, 0, 0, time.UTC),
	}
	out := plain(renderChatItem(chat, 60, false, mentions))
	if !strings.Contains(out, "@Sarthak") {
		t.Fatalf("chat item preview missing resolved mention:\n%s", out)
	}
	if strings.Contains(out, "41755755978893") {
		t.Fatalf("chat item preview leaked raw mention id:\n%s", out)
	}
}

func TestLoadChatsCmdResolvesPreviewMentions(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	ctx := context.Background()
	if err := repo.UpsertContact(ctx, domain.Contact{JID: "41755755978893@lid", DisplayName: "Sarthak Mittal"}); err != nil {
		t.Fatalf("UpsertContact() error = %v", err)
	}
	if err := repo.RecordMessage(ctx, domain.Message{
		ID:        "preview-mention",
		ChatJID:   "project-alpha@g.us",
		SenderJID: "bob@s.whatsapp.net",
		Text:      "@41755755978893 dekho",
		Timestamp: time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC),
		Receipt:   domain.ReceiptStateReceived,
		IsGroup:   true,
	}, false); err != nil {
		t.Fatalf("RecordMessage() error = %v", err)
	}

	msg := loadChatsCmd(repo, "", defaultChatListLimit)()
	loaded, ok := msg.(chatsLoadedMsg)
	if !ok {
		t.Fatalf("cmd() type = %T, want chatsLoadedMsg", msg)
	}
	if loaded.mentions["41755755978893"] != "Sarthak Mittal" {
		t.Fatalf("mentions = %#v, want preview mention resolved", loaded.mentions)
	}

	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 96
	m.height = 30
	m.ready = true
	updated, _ := m.Update(loaded)
	model := updated.(Model)
	view := plain(model.View())
	if !strings.Contains(view, "@Sarthak") {
		t.Fatalf("chat list view missing resolved mention:\n%s", view)
	}
}

func TestChatMonogramDerivesInitials(t *testing.T) {
	t.Parallel()

	tests := []struct{ title, want string }{
		{"Sonu Asansol", "SA"},
		{"Mom", "M "},
		{"8th Mile Website", "8M"},
		{"", "· "},
		{"— —", "· "},
	}
	for _, tt := range tests {
		if got := chatMonogram(tt.title); got != tt.want {
			t.Fatalf("chatMonogram(%q) = %q, want %q", tt.title, got, tt.want)
		}
	}
}

func TestMediaChipShowsDurationAndSize(t *testing.T) {
	t.Parallel()

	voice := domain.Message{
		ID: "v1", ChatJID: "a@s.whatsapp.net", SenderJID: "a@s.whatsapp.net",
		Text: "[voice note]", MediaKind: domain.MediaKindVoice, MediaSeconds: 72,
		Timestamp: time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC), Receipt: domain.ReceiptStateReceived,
	}
	out := plain(renderThreadMessage(voice, 60, nil, false))
	if !strings.Contains(out, "voice · 1:12") {
		t.Fatalf("voice message missing duration chip:\n%s", out)
	}
	if strings.Contains(out, "[voice note]") {
		t.Fatalf("placeholder text should be replaced by the chip:\n%s", out)
	}

	doc := domain.Message{
		ID: "d1", ChatJID: "a@s.whatsapp.net", SenderJID: "a@s.whatsapp.net",
		Text: "[document] brief.pdf", MediaKind: domain.MediaKindDocument,
		MediaFileName: "brief.pdf", MediaFileLength: 2202009,
		Timestamp: time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC), Receipt: domain.ReceiptStateReceived,
	}
	out = plain(renderThreadMessage(doc, 60, nil, false))
	if !strings.Contains(out, "document · brief.pdf · 2.1 MB") {
		t.Fatalf("document message missing metadata chip:\n%s", out)
	}
}

func TestScrollThumbAppearsOnlyWhenScrollable(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 96
	m.height = 26
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.messages = makeThreadMessages(m.currentChatID, 60)
	m.threadMessageLimit = messageLimit

	if !strings.Contains(m.renderThread(), "█") {
		t.Fatal("expected scroll thumb on a long thread")
	}

	m.messages = makeThreadMessages(m.currentChatID, 1)
	if strings.Contains(m.renderThread(), "█") {
		t.Fatal("did not expect a scroll thumb when the thread fits")
	}
}

func TestNewMessageHintWhileScrolledUp(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 96
	m.height = 26
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.messages = makeThreadMessages(m.currentChatID, 40)
	m.threadMessageLimit = messageLimit
	m.threadScroll = 20

	grown := makeThreadMessages(m.currentChatID, 42)
	updated, _ := m.Update(messagesLoadedMsg{chatJID: m.currentChatID, messages: grown, limit: messageLimit})
	model := updated.(Model)
	if model.threadNewWhileAway != 2 {
		t.Fatalf("threadNewWhileAway = %d, want 2", model.threadNewWhileAway)
	}
	if view := plain(model.renderThread()); !strings.Contains(view, "2 NEW") && !strings.Contains(view, "2 new") {
		t.Fatalf("thread header missing new-message hint:\n%s", view)
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnd})
	model = updated.(Model)
	if model.threadNewWhileAway != 0 {
		t.Fatalf("threadNewWhileAway after end = %d, want 0", model.threadNewWhileAway)
	}
}

func TestHelpOverlayOpensAndCloses(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 96
	m.height = 30
	m.ready = true
	m.chats = makeChatSummaries(3)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	model := updated.(Model)
	if !model.helpOpen {
		t.Fatal("expected ? to open the help overlay")
	}
	view := plain(model.View())
	for _, want := range []string{"Inbox", "Thread", "Compose", "Global", "force repaint"} {
		if !strings.Contains(view, want) {
			t.Fatalf("help view missing %q:\n%s", want, view)
		}
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if model.helpOpen {
		t.Fatal("expected esc to close the help overlay")
	}
	if model.quitArmed {
		t.Fatal("closing help must not arm quit")
	}
}

func TestSyncSpinnerTicksWhileSyncing(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 96
	m.height = 26
	m.ready = true
	m.syncingRecent = true

	updated, cmd := m.Update(spinnerTickMsg{})
	model := updated.(Model)
	if model.spinnerFrame != 1 || cmd == nil {
		t.Fatalf("spinner frame = %d, cmd nil = %v; want advancing tick", model.spinnerFrame, cmd == nil)
	}

	model.syncingRecent = false
	updated, cmd = model.Update(spinnerTickMsg{})
	model = updated.(Model)
	if model.spinnerActive || cmd != nil {
		t.Fatal("spinner must stop once syncing completes")
	}
}

func TestFooterShowsTransportStatus(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 96
	m.height = 26
	m.ready = true
	m.status = "History sync updated"

	if footer := plain(m.renderFooter(m.chatListHelpText())); !strings.Contains(footer, "History sync updated") {
		t.Fatalf("footer missing status line:\n%s", footer)
	}
	m.lastErr = "boom"
	if footer := plain(m.renderFooter(m.chatListHelpText())); !strings.Contains(footer, "boom") {
		t.Fatalf("footer must prioritize errors:\n%s", footer)
	}
}

type fakeAudioPlayer struct {
	playing    bool
	playedPath string
	stopped    bool
	playErr    error
}

func (f *fakeAudioPlayer) Play(path string) error {
	if f.playErr != nil {
		return f.playErr
	}
	f.playedPath = path
	f.playing = true
	return nil
}

func (f *fakeAudioPlayer) Stop() error {
	f.stopped = true
	f.playing = false
	return nil
}

func (f *fakeAudioPlayer) Playing() bool { return f.playing }

func voiceThreadModel(t *testing.T, player audioPlayer, transport *fakeTransport, msg domain.Message) Model {
	t.Helper()
	repo := seededRepo(t)
	m := NewModel(repo, transport).WithPlayer(player)
	m.width = 96
	m.height = 26
	m.ready = true
	m.mode = viewThread
	m.currentChatID = msg.ChatJID
	m.messages = []domain.Message{msg}
	m.threadMessageLimit = messageLimit
	return m
}

func TestPlayKeyPlaysDownloadedVoiceNote(t *testing.T) {
	t.Parallel()

	player := &fakeAudioPlayer{}
	transport := &fakeTransport{events: make(chan domain.Event, 1)}
	m := voiceThreadModel(t, player, transport, domain.Message{
		ID: "v1", ChatJID: "alice@s.whatsapp.net", SenderJID: "alice@s.whatsapp.net",
		Text: "[voice note]", MediaKind: domain.MediaKindVoice, MediaSeconds: 12,
		DownloadedPath: "/tmp/voice.ogg",
		Timestamp:      time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC), Receipt: domain.ReceiptStateReceived,
	})

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if cmd == nil {
		t.Fatal("expected playback command")
	}
	result, ok := cmd().(opResultMsg)
	if !ok || result.err != nil {
		t.Fatalf("playback result = %#v", result)
	}
	if player.playedPath != "/tmp/voice.ogg" {
		t.Fatalf("playedPath = %q, want cached download used directly", player.playedPath)
	}
	if transport.downloadedMessage != "" {
		t.Fatal("cached voice note must not be downloaded again")
	}
	if !strings.Contains(result.status, "0:12") {
		t.Fatalf("status = %q, want duration included", result.status)
	}

	// p again stops.
	model := updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	model = updated.(Model)
	if !player.stopped {
		t.Fatal("expected second p to stop playback")
	}
	if model.status != "Playback stopped" {
		t.Fatalf("status = %q, want Playback stopped", model.status)
	}
}

func TestPlayKeyDownloadsVoiceNoteWhenNotCached(t *testing.T) {
	t.Parallel()

	player := &fakeAudioPlayer{}
	transport := &fakeTransport{events: make(chan domain.Event, 1)}
	m := voiceThreadModel(t, player, transport, domain.Message{
		ID: "v2", ChatJID: "alice@s.whatsapp.net", SenderJID: "alice@s.whatsapp.net",
		Text: "[voice note]", MediaKind: domain.MediaKindVoice,
		MediaDirectPath: "/wa/media/v2",
		Timestamp:       time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC), Receipt: domain.ReceiptStateReceived,
	})

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if cmd == nil {
		t.Fatal("expected playback command")
	}
	result, ok := cmd().(opResultMsg)
	if !ok || result.err != nil {
		t.Fatalf("playback result = %#v", result)
	}
	if transport.downloadedMessage != "v2" {
		t.Fatalf("downloadedMessage = %q, want v2 downloaded first", transport.downloadedMessage)
	}
	if player.playedPath == "" || !strings.HasSuffix(player.playedPath, "downloaded.bin") {
		t.Fatalf("playedPath = %q, want the downloaded file", player.playedPath)
	}
	if !result.refresh {
		t.Fatal("expected refresh so the saved-path annotation appears")
	}
}

func TestPlayKeyWithoutVoiceMessagesSetsError(t *testing.T) {
	t.Parallel()

	player := &fakeAudioPlayer{}
	transport := &fakeTransport{events: make(chan domain.Event, 1)}
	m := voiceThreadModel(t, player, transport, domain.Message{
		ID: "t1", ChatJID: "alice@s.whatsapp.net", SenderJID: "alice@s.whatsapp.net",
		Text:      "just text",
		Timestamp: time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC), Receipt: domain.ReceiptStateReceived,
	})

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	model := updated.(Model)
	if cmd != nil {
		t.Fatal("did not expect a command without playable audio")
	}
	if model.lastErr == "" {
		t.Fatal("expected an error message when no voice notes exist")
	}
}

func TestReactionLineAggregatesNamesAndCounts(t *testing.T) {
	t.Parallel()

	msg := domain.Message{
		ID: "m1", ChatJID: "g@g.us", SenderJID: "b@s.whatsapp.net", Text: "sticker",
		Timestamp: time.Date(2026, 7, 8, 14, 42, 0, 0, time.UTC), Receipt: domain.ReceiptStateReceived, IsGroup: true,
		Reactions: []domain.Reaction{
			{Emoji: "😂", SenderName: "Shashwat"},
		},
	}
	out := plain(renderThreadMessage(msg, 60, nil, false))
	if !strings.Contains(out, "😂 Shashwat") {
		t.Fatalf("single reaction should show the reactor's name:\n%s", out)
	}

	msg.Reactions = []domain.Reaction{
		{Emoji: "😂", SenderName: "A"}, {Emoji: "😂", SenderName: "B"},
		{Emoji: "😂", SenderName: "C"}, {Emoji: "👍", SenderName: "D"},
	}
	out = plain(renderThreadMessage(msg, 60, nil, false))
	if !strings.Contains(out, "😂 3") || !strings.Contains(out, "👍 1") {
		t.Fatalf("many reactions should aggregate to counts:\n%s", out)
	}
}

func TestReactModeSendsQuickReaction(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	transport := &fakeTransport{events: make(chan domain.Event, 1)}
	m := NewModel(repo, transport)
	m.width = 96
	m.height = 26
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.messages = makeThreadMessages(m.currentChatID, 5)
	m.threadMessageLimit = messageLimit

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model := updated.(Model)
	if !model.selecting || model.selectIndex != 4 {
		t.Fatalf("selecting = %v, selectIndex = %d; want select mode on newest message", model.selecting, model.selectIndex)
	}

	// Move the cursor up one message, then send 😂 (slot 3).
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	model = updated.(Model)
	if model.selectIndex != 3 {
		t.Fatalf("selectIndex = %d, want 3 after k", model.selectIndex)
	}
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	model = updated.(Model)
	if model.selecting {
		t.Fatal("select mode should exit after sending")
	}
	if cmd == nil {
		t.Fatal("expected reaction command")
	}
	result, ok := cmd().(opResultMsg)
	if !ok || result.err != nil {
		t.Fatalf("reaction result = %#v", result)
	}
	if transport.reactionTargetID != "thread-004" || transport.reactionEmoji != "😂" {
		t.Fatalf("reaction sent = %q on %q, want 😂 on thread-004", transport.reactionEmoji, transport.reactionTargetID)
	}

	// x removes: empty emoji.
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model = updated.(Model)
	_, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd == nil {
		t.Fatal("expected removal command")
	}
	if _ = cmd(); transport.reactionEmoji != "" {
		t.Fatalf("removal emoji = %q, want empty", transport.reactionEmoji)
	}
}

func TestComposerReplacesEmojiShortcodes(t *testing.T) {
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
	m.composer.SetValue("nice one :joy")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	model := updated.(Model)
	if got := model.composer.Value(); got != "nice one 😂" {
		t.Fatalf("composer value = %q, want shortcode replaced", got)
	}
}

func TestEmojiSuggestionsForTrailingShortcode(t *testing.T) {
	t.Parallel()

	suggestions := emojiSuggestions("brb :fi", 5)
	if len(suggestions) == 0 {
		t.Fatal("expected emoji suggestions for :fi prefix")
	}
	if !strings.Contains(suggestions[0].label, "🔥") {
		t.Fatalf("first suggestion = %q, want fire emoji", suggestions[0].label)
	}
	if suggestions[0].replacement != "brb 🔥" {
		t.Fatalf("replacement = %q, want token swapped for emoji", suggestions[0].replacement)
	}
	if emojiSuggestions("no token here", 5) != nil {
		t.Fatal("expected no suggestions without a trailing :prefix")
	}
}

func TestLegacyReactionRowsAreHiddenNotDeleted(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 96
	m.height = 26
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.messages = []domain.Message{
		{ID: "m1", ChatJID: m.currentChatID, SenderJID: "a@s.whatsapp.net", Text: "hello",
			Timestamp: time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC), Receipt: domain.ReceiptStateReceived},
		{ID: "legacy", ChatJID: m.currentChatID, SenderJID: "a@s.whatsapp.net", Text: "[reaction]",
			Timestamp: time.Date(2026, 7, 8, 10, 1, 0, 0, time.UTC), Receipt: domain.ReceiptStateReceived},
	}
	m.threadMessageLimit = messageLimit

	body := plain(strings.Join(m.threadMessageLines(80), "\n"))
	if strings.Contains(body, "[reaction]") {
		t.Fatalf("legacy [reaction] placeholder row should be hidden:\n%s", body)
	}
	if !strings.Contains(body, "hello") {
		t.Fatalf("real messages must still render:\n%s", body)
	}
}

func TestMediaCaptionStartingWithBracketIsShown(t *testing.T) {
	t.Parallel()

	msg := domain.Message{
		ID: "m1", ChatJID: "a@s.whatsapp.net", SenderJID: "a@s.whatsapp.net",
		Text: "[URGENT] prod is down, call me", MediaKind: domain.MediaKindImage,
		Timestamp: time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC), Receipt: domain.ReceiptStateReceived,
	}
	out := plain(renderThreadMessage(msg, 60, nil, false))
	if !strings.Contains(out, "[URGENT] prod is down") {
		t.Fatalf("caption starting with '[' must render:\n%s", out)
	}

	// Exact placeholders (with or without filename) still collapse to the chip.
	msg.Text = "[image]"
	out = plain(renderThreadMessage(msg, 60, nil, false))
	if strings.Contains(out, "[image]") {
		t.Fatalf("bare placeholder should be replaced by the chip:\n%s", out)
	}
}

func TestReactCursorReanchorsAfterReload(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	transport := &fakeTransport{events: make(chan domain.Event, 1)}
	m := NewModel(repo, transport)
	m.width = 96
	m.height = 26
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.messages = makeThreadMessages(m.currentChatID, 10)
	m.threadMessageLimit = messageLimit

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model := updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	model = updated.(Model)
	targetID := model.messages[model.selectIndex].ID

	// Reload arrives with the two oldest messages dropped and two new ones
	// appended (capped-window shape): indexes shift by two.
	reloaded := makeThreadMessages(m.currentChatID, 12)[2:]
	updated, _ = model.Update(messagesLoadedMsg{chatJID: m.currentChatID, messages: reloaded, limit: messageLimit})
	model = updated.(Model)
	if !model.selecting {
		t.Fatal("select mode should survive a reload that keeps the target")
	}
	if got := model.messages[model.selectIndex].ID; got != targetID {
		t.Fatalf("select cursor now points at %q, want re-anchored to %q", got, targetID)
	}

	// A reload that drops the target exits select mode instead of guessing.
	updated, _ = model.Update(messagesLoadedMsg{chatJID: m.currentChatID, messages: makeThreadMessages(m.currentChatID, 3), limit: messageLimit})
	model = updated.(Model)
	if model.selecting {
		t.Fatal("select mode must exit when the target message disappears")
	}
}

func TestNewMessageCountingSurvivesCappedReload(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 96
	m.height = 26
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.messages = makeThreadMessages(m.currentChatID, 40)
	m.threadMessageLimit = messageLimit
	m.threadScroll = 20

	// Capped reload: same slice length (40), two dropped from the top and
	// two new appended at the bottom.
	reloaded := makeThreadMessages(m.currentChatID, 42)[2:]
	updated, _ := m.Update(messagesLoadedMsg{chatJID: m.currentChatID, messages: reloaded, limit: messageLimit})
	model := updated.(Model)
	if model.threadNewWhileAway != 2 {
		t.Fatalf("threadNewWhileAway = %d, want 2 despite unchanged slice length", model.threadNewWhileAway)
	}
}

func TestMentionSuggestionsFromThreadSenders(t *testing.T) {
	t.Parallel()

	repo := seededRepo(t)
	m := NewModel(repo, &fakeTransport{events: make(chan domain.Event, 1)})
	m.width = 96
	m.height = 28
	m.ready = true
	m.mode = viewThread
	m.currentChatID = "project-alpha@g.us"
	m.messages = []domain.Message{
		{ID: "m1", ChatJID: m.currentChatID, SenderJID: "911234@s.whatsapp.net", SenderName: "Pranjal Agarwal",
			Text: "hi", Timestamp: time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC), Receipt: domain.ReceiptStateReceived, IsGroup: true},
		{ID: "m2", ChatJID: m.currentChatID, SenderJID: "922222@s.whatsapp.net", SenderName: "Shashwat Jha",
			Text: "yo", Timestamp: time.Date(2026, 7, 8, 10, 1, 0, 0, time.UTC), Receipt: domain.ReceiptStateReceived, IsGroup: true},
	}
	m.composing = true
	m.composer.Focus()
	m.composer.SetValue("listen @pra")
	m.refreshPathSuggestions()

	if m.suggestionsKind != "Mentions" || len(m.pathSuggestions) != 1 {
		t.Fatalf("suggestions = %#v (%s), want one mention for @pra", m.pathSuggestions, m.suggestionsKind)
	}
	if m.pathSuggestions[0].label != "@Pranjal Agarwal" {
		t.Fatalf("label = %q, want @Pranjal Agarwal", m.pathSuggestions[0].label)
	}

	if !m.applySelectedPathSuggestion() {
		t.Fatal("expected suggestion to apply")
	}
	if got := m.composer.Value(); got != "listen @Pranjal Agarwal " {
		t.Fatalf("composer = %q, want tag inserted", got)
	}
	if m.draftMentions["Pranjal Agarwal"] != "911234@s.whatsapp.net" {
		t.Fatalf("draftMentions = %#v, want tagged JID recorded", m.draftMentions)
	}
}

func TestSendingTaggedMessageConvertsToWireFormat(t *testing.T) {
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
	m.composer.SetValue("@Pranjal Agarwal cab aa gayi")
	m.draftMentions = map[string]string{"Pranjal Agarwal": "911234@s.whatsapp.net"}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected send command")
	}
	if result := cmd(); result == nil {
		t.Fatal("expected send result")
	}
	if transport.sentText != "@911234 cab aa gayi" {
		t.Fatalf("sentText = %q, want wire-format mention", transport.sentText)
	}
	if len(transport.sentMentions) != 1 || transport.sentMentions[0] != "911234@s.whatsapp.net" {
		t.Fatalf("sentMentions = %#v, want tagged JID in context info", transport.sentMentions)
	}
}

func TestApplyDraftMentionsLongestNameFirst(t *testing.T) {
	t.Parallel()

	mentions := map[string]string{
		"Pranjal":         "911111@s.whatsapp.net",
		"Pranjal Agarwal": "922222@s.whatsapp.net",
	}
	text, jids := applyDraftMentions("@Pranjal Agarwal and @Pranjal", mentions)
	if text != "@922222 and @911111" {
		t.Fatalf("text = %q, want longest tag converted first", text)
	}
	if len(jids) != 2 {
		t.Fatalf("jids = %#v, want both", jids)
	}
}
