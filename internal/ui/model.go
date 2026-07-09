package ui

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	qrterminal "github.com/mdp/qrterminal/v3"

	"github.com/chirag/whatsapp-terminal/internal/domain"
	"github.com/chirag/whatsapp-terminal/internal/media"
	appstore "github.com/chirag/whatsapp-terminal/internal/store"
)

const (
	defaultChatListLimit = 500
	messageLimit         = 200
	messagePageSize      = 100
	historyRequestCount  = 50
	threadPrefetchMargin = 5
	threadPageScroll     = 8
	threadMouseScroll    = 3
	maxPathSuggestions   = 5
	chatItemLineCount    = 2
	chatListHelpText     = "j/k move  enter open  / search  r refresh  T theme  ? help  esc·q quit"
	smallCapRuleMargin   = 6
)

type viewMode int

const (
	viewChats viewMode = iota
	viewThread
)

type transportEventMsg struct {
	event domain.Event
}

// spinnerTickMsg advances the sync spinner animation.
type spinnerTickMsg struct{}

func spinnerTickCmd() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return spinnerTickMsg{} })
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// quickReactions is the 1-6 palette in react mode, mirroring WhatsApp's
// default reaction row.
var quickReactions = []string{"👍", "❤️", "😂", "😮", "😢", "🙏"}

type chatsLoadedMsg struct {
	chats []domain.ChatSummary
	// mentions maps "@123…" tokens found in chat previews to contact names.
	mentions map[string]string
	err      error
}

type messagesLoadedMsg struct {
	chatJID  string
	messages []domain.Message
	// mentions maps the numeric user part of "@123…" mention tokens found
	// in the loaded messages to contact names.
	mentions map[string]string
	limit    int
	err      error
}

type opResultMsg struct {
	err              error
	status           string
	chatJID          string
	refresh          bool
	clearComposer    bool
	clearAttachments bool
	historyRequest   bool
}

type composeActionType int

const (
	composeActionText composeActionType = iota
	composeActionImage
	composeActionMedia
)

type composeAction struct {
	kind    composeActionType
	text    string
	path    string
	caption string
}

type stagedAttachment struct {
	token string
	path  string
	kind  domain.MediaKind
	secs  time.Duration
	// temp marks files created by the app (clipboard captures, voice
	// recordings) that should be deleted once the draft is sent or
	// discarded. User-picked files are never removed.
	temp bool
}

type attachmentStagedMsg struct {
	attachment stagedAttachment
	status     string
	err        error
}

type pathSuggestion struct {
	label       string
	replacement string
	isDir       bool
	// mentionName/mentionJID are set on @-mention suggestions so applying
	// one records who was tagged for the send-time context info.
	mentionName string
	mentionJID  string
}

type filePickerEntry struct {
	name  string
	path  string
	isDir bool
}

type Model struct {
	repo         *appstore.Store
	transport    domain.Transport
	events       <-chan domain.Event
	forceRepaint bool
	frameNonce   int

	mode           viewMode
	width          int
	height         int
	status         string
	lastErr        string
	qrCode         string
	ready          bool
	syncingRecent  bool
	searching      bool
	composing      bool
	filePickerOpen bool
	recordingVoice bool
	stoppingVoice  bool
	helpOpen       bool
	spinnerActive  bool
	spinnerFrame   int
	selected       int
	chatListOffset int
	currentChatID  string
	nextImageID    int
	nextVoiceID    int
	downloadDir    string
	recordingSince time.Time

	search    textinput.Model
	composer  textarea.Model
	clipboard clipboardReader
	sounder   sounder
	recorder  voiceRecorder
	player    audioPlayer

	chats                []domain.ChatSummary
	messages             []domain.Message
	mentionNames         map[string]string
	chatMentionNames     map[string]string
	pendingAttachments   []stagedAttachment
	pathSuggestions      []pathSuggestion
	pathSuggestionIdx    int
	pathSuggestionFocus  bool
	filePickerDir        string
	filePickerEntries    []filePickerEntry
	filePickerSelected   int
	threadHistoryPending bool
	threadLoadingOlder   bool
	threadMessageLimit   int
	threadScroll         int
	threadNewWhileAway   int
	selecting            bool
	selectIndex          int
	mediaPickerOpen      bool
	mediaPickerIndex     int
	suggestionsKind      string
	draftMentions        map[string]string
	quitArmed            bool
	quitAfterNavigation  bool
	dataDir              string
	themeName            string
	chatListLimit        int
	logger               *slog.Logger
}

// NewModel builds a Model wired to the system clipboard, terminal bell, and
// system voice recorder. Swap any of those with the With* methods below.
func NewModel(repo *appstore.Store, transport domain.Transport) Model {
	search := textinput.New()
	search.Placeholder = "filter inbox…"
	search.Prompt = "Search: "
	search.CharLimit = 128

	composer := textarea.New()
	composer.Placeholder = "type a message…"
	composer.Prompt = "› "
	composer.CharLimit = 4096
	composer.ShowLineNumbers = false
	composer.SetHeight(1)
	composer.SetWidth(48)

	return Model{
		repo:          repo,
		transport:     transport,
		events:        transport.Events(),
		mode:          viewChats,
		status:        "Starting WhatsApp terminal...",
		search:        search,
		composer:      composer,
		clipboard:     newSystemClipboard(),
		sounder:       newTerminalBell(),
		recorder:      newSystemVoiceRecorder(),
		player:        newSystemAudioPlayer(),
		downloadDir:   defaultDownloadDir(),
		filePickerDir: defaultPickerDir(),
		themeName:     currentTheme.Name,
		chatListLimit: defaultChatListLimit,
	}
}

// WithClipboard overrides the clipboard reader. Nil keeps the current one.
func (m Model) WithClipboard(clipboard clipboardReader) Model {
	if clipboard != nil {
		m.clipboard = clipboard
	}
	return m
}

// WithSounder overrides the notification sounder. Nil keeps the current one.
func (m Model) WithSounder(sounder sounder) Model {
	if sounder != nil {
		m.sounder = sounder
	}
	return m
}

// WithRecorder overrides the voice recorder. Nil keeps the current one.
func (m Model) WithRecorder(recorder voiceRecorder) Model {
	if recorder != nil {
		m.recorder = recorder
	}
	return m
}

// WithPlayer overrides the audio player. Nil keeps the current one.
func (m Model) WithPlayer(player audioPlayer) Model {
	if player != nil {
		m.player = player
	}
	return m
}

// WithDownloadDir overrides where media downloads land. Blank keeps the
// current directory.
func (m Model) WithDownloadDir(dir string) Model {
	if strings.TrimSpace(dir) != "" {
		m.downloadDir = dir
	}
	return m
}

// WithChatListLimit overrides the max chats fetched per inbox query. Values
// <= 0 are ignored and the default (defaultChatListLimit) is kept.
func (m Model) WithChatListLimit(limit int) Model {
	if limit > 0 {
		m.chatListLimit = limit
	}
	return m
}

// WithLogger attaches a slog logger so the UI can emit diagnostic events
// alongside the transport. Nil silences UI logging. The logger is also
// installed package-wide so helpers without access to Model (e.g.
// displayChatTitle) can emit diagnostics through pkgLog.
func (m Model) WithLogger(logger *slog.Logger) Model {
	m.logger = logger
	pkgLog = logger
	return m
}

// pkgLog is a package-level logger used by helpers that have no Model
// receiver. Set via Model.WithLogger; nil means UI logging is off.
var pkgLog *slog.Logger

func uilog(msg string, args ...any) {
	if pkgLog == nil {
		return
	}
	pkgLog.Debug("ui:"+msg, args...)
}

// WithForceRepaint makes every rendered line differ between updates without
// changing visual output. This is useful in no-alt-screen mode where Bubble
// Tea's diff renderer can otherwise skip rows that need to be restored after a
// terminal autowrap/desync event.
func (m Model) WithForceRepaint(enabled bool) Model {
	m.forceRepaint = enabled
	return m
}

// WithDataDir records the data directory used for persisting per-user
// settings such as the active theme.
func (m Model) WithDataDir(dir string) Model {
	m.dataDir = dir
	return m
}

// WithTheme switches the active theme. The slug is resolved via LookupTheme;
// unknown slugs leave the previous theme in place.
func (m Model) WithTheme(slug string) Model {
	if t, ok := LookupTheme(slug); ok {
		applyTheme(t)
		m.themeName = t.Name
	}
	return m
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(tea.ClearScreen, waitForTransportEvent(m.events), m.loadChats(""))
}

func (m Model) WithQuitAfterNavigation(enabled bool) Model {
	m.quitAfterNavigation = enabled
	return m
}

func (m Model) canQuitWithKey() bool {
	return !m.searching && !m.composing
}

func (m Model) requestQuit() (tea.Model, tea.Cmd) {
	if m.recordingVoice && m.recorder != nil {
		_ = m.recorder.Cancel()
	}
	if m.player != nil {
		_ = m.player.Stop()
	}
	m.clearPendingAttachments()
	m.quitArmed = false
	return m, tea.Quit
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.frameNonce++
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		m.keepSelectedChatVisible()
		return m, nil
	case transportEventMsg:
		m = m.applyTransportEvent(msg.event)
		cmds := []tea.Cmd{waitForTransportEvent(m.events), m.loadChats(m.search.Value())}
		if m.syncingRecent && !m.spinnerActive {
			m.spinnerActive = true
			cmds = append(cmds, spinnerTickCmd())
		}
		if m.currentChatID != "" {
			cmds = append(cmds, loadMessagesCmd(m.repo, m.currentChatID, m.messageLoadLimit()))
		}
		if msg.event.Notify && m.sounder != nil {
			_ = m.sounder.Bell()
		}
		return m, tea.Batch(cmds...)
	case chatsLoadedMsg:
		if msg.err != nil {
			m.lastErr = msg.err.Error()
			return m, nil
		}
		if len(msg.chats) > 0 {
			m.syncingRecent = false
		}
		selectedJID := m.selectedChatJID()
		m.chats = msg.chats
		m.chatMentionNames = msg.mentions
		m.selected = clampSelection(m.selected, len(m.chats))
		if selectedJID != "" {
			for idx, chat := range m.chats {
				if chat.JID == selectedJID {
					m.selected = idx
					break
				}
			}
		}
		m.keepSelectedChatVisible()
		return m, nil
	case messagesLoadedMsg:
		if msg.err != nil {
			if msg.chatJID == m.currentChatID {
				m.threadLoadingOlder = false
			}
			m.lastErr = msg.err.Error()
			return m, nil
		}
		if msg.chatJID == m.currentChatID {
			lineWidth := m.threadLayout().contentWidth - boxStyle.GetHorizontalFrameSize()
			previousLineCount := len(m.threadMessageLines(lineWidth))
			previousOldestID := oldestMessageID(m.messages)
			previousNewestID := newestMessageID(m.messages)
			wasAtBottom := m.threadScroll == 0
			wasLoadingOlder := m.threadLoadingOlder

			m.threadLoadingOlder = false
			if msg.limit > 0 {
				m.threadMessageLimit = max(m.threadMessageLimit, msg.limit)
			} else if m.threadMessageLimit == 0 {
				m.threadMessageLimit = messageLimit
			}
			selectTargetID := ""
			if m.selecting && m.selectIndex >= 0 && m.selectIndex < len(m.messages) {
				selectTargetID = m.messages[m.selectIndex].ID
			}
			m.messages = msg.messages
			m.mentionNames = msg.mentions
			m.reanchorSelectCursor(selectTargetID)
			// Count messages appended after the previous newest by walking
			// back to its ID. A capped reload drops rows from the top, so
			// slice-length deltas undercount arrivals.
			appended := 0
			if previousNewestID != "" {
				for i := len(msg.messages) - 1; i >= 0; i-- {
					if msg.messages[i].ID == previousNewestID {
						break
					}
					appended++
				}
				if appended == len(msg.messages) {
					// Previous newest vanished: full reload, not an append.
					appended = 0
				}
			}
			newLineCount := len(m.threadMessageLines(lineWidth))
			if wasAtBottom {
				m.threadScroll = 0
			} else if appended > 0 {
				// Grow the offset by the appended blocks' real line count so
				// the view keeps showing the same lines, and count the new
				// messages for the "N new" hint.
				added := newLineCount - previousLineCount
				if added <= 0 {
					_, ends := m.threadMessageBlocks(lineWidth)
					added = newLineCount - ends[len(msg.messages)-appended-1]
				}
				m.threadScroll += added
				m.threadNewWhileAway += appended
			}
			m.threadScroll = min(max(0, m.threadScroll), m.maxThreadScroll())
			if m.threadScroll == 0 {
				m.threadNewWhileAway = 0
			}
			if len(msg.messages) == 0 && m.mode == viewThread && !m.threadHistoryPending {
				var cmd tea.Cmd
				m, cmd = m.loadOlderThreadMessages()
				return m, cmd
			}
			if len(msg.messages) > 0 {
				m.threadHistoryPending = false
			}
			if wasLoadingOlder && previousOldestID != "" && oldestMessageID(msg.messages) == previousOldestID && m.threadNearOldestBoundary() {
				var cmd tea.Cmd
				m, cmd = m.loadOlderThreadMessages()
				return m, cmd
			}
		}
		return m, nil
	case opResultMsg:
		if msg.err != nil {
			m.lastErr = msg.err.Error()
			if msg.historyRequest {
				m.threadHistoryPending = false
			}
		}
		if msg.status != "" {
			m.status = msg.status
		}
		m.stoppingVoice = false
		if msg.err == nil {
			if msg.clearComposer {
				m.composer.SetValue("")
				m.draftMentions = nil
			}
			if msg.clearAttachments {
				m.clearPendingAttachments()
			}
			m.refreshPathSuggestions()
		}
		if msg.refresh && msg.chatJID != "" {
			if msg.chatJID == m.currentChatID {
				m.threadHistoryPending = false
			}
			return m, tea.Batch(
				m.loadChats(m.search.Value()),
				loadMessagesCmd(m.repo, msg.chatJID, m.messageLoadLimit()),
			)
		}
		return m, nil
	case spinnerTickMsg:
		if !m.syncingRecent {
			m.spinnerActive = false
			return m, nil
		}
		m.spinnerFrame++
		return m, spinnerTickCmd()
	case attachmentStagedMsg:
		if msg.err != nil {
			m.lastErr = msg.err.Error()
			// A failed voice-note stop must release the stopping latch or
			// the recorder can never be started again.
			m.stoppingVoice = false
			return m, nil
		}
		m.pendingAttachments = append(m.pendingAttachments, msg.attachment)
		m.composer.SetValue(appendDraftToken(m.composer.Value(), msg.attachment.token))
		if msg.status != "" {
			m.status = msg.status
		}
		m.recordingVoice = false
		m.stoppingVoice = false
		m.refreshPathSuggestions()
		return m, nil
	case tea.KeyMsg:
		if m.helpOpen {
			switch msg.String() {
			case "ctrl+c":
				return m.requestQuit()
			case "esc", "q", "?", "enter":
				m.helpOpen = false
			}
			return m, nil
		}
		switch msg.String() {
		case "?":
			// Not while typing: "?" must stay typeable in search and compose.
			if m.canQuitWithKey() {
				m.helpOpen = true
				return m, nil
			}
		case "ctrl+c":
			return m.requestQuit()
		case "ctrl+l":
			// Manual full repaint: recovers the screen if the terminal ever
			// desynchronizes (glyph-width mismatch, resize glitch, ssh noise).
			return m, tea.ClearScreen
		case "q":
			if m.quitArmed && m.canQuitWithKey() {
				return m.requestQuit()
			}
			m.quitArmed = false
		case "esc":
		default:
			m.quitArmed = false
		}
	}

	if m.mode == viewThread {
		return m.updateThread(msg)
	}
	return m.updateChatList(msg)
}

func (m Model) View() string {
	if !m.ready {
		return "Loading terminal UI..."
	}

	var rendered string
	if m.qrCode != "" {
		rendered = m.renderPairing()
		return m.fitFrame(rendered)
	}
	if m.helpOpen {
		return m.fitFrame(m.renderHelp())
	}

	switch m.mode {
	case viewThread:
		rendered = m.renderThread()
	default:
		rendered = m.renderChatList()
	}
	return m.fitFrame(rendered)
}

func (m Model) updateChatList(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.searching {
			switch msg.String() {
			case "esc":
				m.searching = false
				m.quitArmed = m.quitAfterNavigation
				m.search.Blur()
				m.search.SetValue("")
				return m, m.loadChats("")
			case "enter":
				m.searching = false
				m.search.Blur()
				return m, nil
			}
			m.search, _ = m.search.Update(msg)
			return m, m.loadChats(m.search.Value())
		}

		switch msg.String() {
		case "esc":
			m.quitArmed = true
			return m, nil
		case "/":
			m.searching = true
			m.search.Focus()
			return m, nil
		case "j", "down":
			m.selected = clampSelection(m.selected+1, len(m.chats))
			m.keepSelectedChatVisible()
			return m, nil
		case "k", "up":
			m.selected = clampSelection(m.selected-1, len(m.chats))
			m.keepSelectedChatVisible()
			return m, nil
		case "r":
			return m, m.loadChats(m.search.Value())
		case "t", "T":
			next := nextTheme(m.themeName)
			applyTheme(next)
			m.themeName = next.Name
			m.status = "Theme: " + next.Label
			if m.dataDir != "" {
				SaveThemeName(m.dataDir, next.Name)
			}
			return m, nil
		case "D":
			// Diagnostic dump: write the raw state of every loaded chat to
			// the debug log so we can trace title/sender weirdness from a
			// real cache without screen-scraping the TUI.
			m.dumpChatListDiagnostics()
			m.status = fmt.Sprintf("Dumped %d chats to debug log", len(m.chats))
			return m, nil
		case "enter":
			chat := m.currentChat()
			if chat == nil {
				return m, nil
			}
			m.mode = viewThread
			m.currentChatID = chat.JID
			m.messages = nil
			m.mentionNames = nil
			m.threadNewWhileAway = 0
			m.selecting = false
			m.mediaPickerOpen = false
			m.threadHistoryPending = false
			m.threadLoadingOlder = false
			m.threadMessageLimit = messageLimit
			m.threadScroll = 0
			return m, tea.Batch(
				resetUnreadCmd(m.repo, chat.JID),
				loadMessagesCmd(m.repo, chat.JID, m.messageLoadLimit()),
				m.loadChats(m.search.Value()),
			)
		}
	}
	return m, nil
}

func (m Model) updateThread(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.MouseMsg:
		return m.updateThreadMouse(msg)
	case tea.KeyMsg:
		switch {
		case m.mediaPickerOpen:
			return m.updateMediaPickerKey(msg)
		case m.selecting:
			return m.updateSelectKey(msg)
		case m.composing && m.filePickerOpen:
			return m.updateFilePickerKey(msg)
		case m.composing:
			return m.updateComposerKey(msg)
		default:
			return m.updateThreadNavigationKey(msg)
		}
	}
	return m, nil
}

// updateSelectKey drives select mode: j/k picks the target message, 1-6
// sends a quick reaction, x removes this device's reaction, d saves the
// selected message's media, p plays it, esc cancels. Reactions exit the mode;
// media actions stay in it so several can chain.
func (m Model) updateSelectKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key := msg.String(); key {
	case "esc", "r":
		m.selecting = false
		return m, nil
	case "k", "up":
		m.selectIndex = clampSelection(m.selectIndex-1, len(m.messages))
		m.alignScrollToSelectCursor()
		return m, nil
	case "j", "down":
		m.selectIndex = clampSelection(m.selectIndex+1, len(m.messages))
		m.alignScrollToSelectCursor()
		return m, nil
	case "d":
		if m.selectIndex < 0 || m.selectIndex >= len(m.messages) {
			return m, nil
		}
		return m.downloadSelectedMessage(m.messages[m.selectIndex])
	case "p":
		if m.selectIndex < 0 || m.selectIndex >= len(m.messages) {
			return m, nil
		}
		return m.playSelectedMessage(m.messages[m.selectIndex])
	case "1", "2", "3", "4", "5", "6", "x":
		if m.selectIndex < 0 || m.selectIndex >= len(m.messages) {
			m.selecting = false
			return m, nil
		}
		target := m.messages[m.selectIndex]
		m.selecting = false
		emoji := ""
		if key != "x" {
			emoji = quickReactions[int(key[0]-'1')]
		}
		return m, sendReactionCmd(m.transport, target, emoji)
	}
	return m, nil
}

// downloadSelectedMessage saves the given message's media, staying in the
// current mode so more items can be actioned. No media, an already-saved
// file, or a no-longer-downloadable message each report a status without
// issuing a download.
func (m Model) downloadSelectedMessage(target domain.Message) (tea.Model, tea.Cmd) {
	switch {
	case target.MediaKind == domain.MediaKindNone:
		m.lastErr = "selected message has no media"
		return m, nil
	case target.DownloadedPath != "":
		m.status = "Already saved · " + filepath.Base(target.DownloadedPath)
		return m, nil
	case target.MediaDirectPath != "":
		return m, downloadMediaCmd(m.transport, target, m.downloadDir)
	default:
		m.lastErr = "media is no longer downloadable"
		return m, nil
	}
}

// playSelectedMessage plays the given message when it is a voice note or
// audio clip, downloading it first when needed. Stays in the current mode.
func (m Model) playSelectedMessage(target domain.Message) (tea.Model, tea.Cmd) {
	if m.player == nil {
		return m, nil
	}
	if target.MediaKind != domain.MediaKindVoice && target.MediaKind != domain.MediaKindAudio {
		m.lastErr = "selected message is not a voice note or audio"
		return m, nil
	}
	m.status = "Starting playback..."
	return m, playVoiceCmd(m.transport, m.player, target, m.downloadDir)
}

// alignScrollToSelectCursor keeps the select-mode cursor visible by scrolling
// so the selected message sits at the bottom of the viewport. It derives the
// offset from the real block layout, so date separators are accounted for.
func (m *Model) alignScrollToSelectCursor() {
	if len(m.messages) == 0 || m.selectIndex < 0 || m.selectIndex >= len(m.messages) {
		return
	}
	layout := m.threadLayout()
	width := layout.contentWidth - boxStyle.GetHorizontalFrameSize()
	lines, ends := m.threadMessageBlocks(width)
	below := len(lines) - ends[m.selectIndex]
	maxScroll := max(0, len(lines)-paddedContentHeight(layout.messageHeight))
	m.threadScroll = min(below, maxScroll)
}

// reanchorSelectCursor re-points the select-mode cursor at the same message
// after m.messages is replaced: a background reload can shift indexes or
// drop the target entirely, and a stale index would act on the wrong
// message. Exits select mode when the target is gone.
func (m *Model) reanchorSelectCursor(targetID string) {
	if !m.selecting {
		return
	}
	if targetID != "" {
		for i, msg := range m.messages {
			if msg.ID == targetID {
				m.selectIndex = i
				return
			}
		}
	}
	m.selecting = false
	m.selectIndex = 0
}

// sendReactionCmd sends (or with an empty emoji, removes) a reaction to the
// given message.
func sendReactionCmd(transport domain.Transport, target domain.Message, emoji string) tea.Cmd {
	return func() tea.Msg {
		err := transport.SendReaction(context.Background(), target.ChatJID, target.SenderJID, target.ID, emoji)
		status := "Reaction sent"
		if emoji == "" {
			status = "Reaction removed"
		}
		return opResultMsg{err: err, status: status, chatJID: target.ChatJID, refresh: err == nil}
	}
}

func (m Model) updateThreadMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if !m.composing {
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			return m.scrollThread(threadMouseScroll)
		case tea.MouseButtonWheelDown:
			return m.scrollThread(-threadMouseScroll)
		}
		return m, nil
	}
	if msg.Action != tea.MouseActionRelease || msg.Button != tea.MouseButtonLeft {
		return m, nil
	}
	switch m.threadToolbarHit(msg.X, msg.Y) {
	case "attach":
		m.openFilePicker()
		return m, nil
	case "voice":
		return m.toggleVoiceRecording()
	}
	return m, nil
}

func (m Model) updateFilePickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.filePickerOpen = false
	case "j", "down":
		m.filePickerSelected = cycleSelection(m.filePickerSelected, 1, len(m.filePickerEntries))
	case "k", "up":
		m.filePickerSelected = cycleSelection(m.filePickerSelected, -1, len(m.filePickerEntries))
	case "h", "backspace":
		m.openParentPickerDir()
	case "enter":
		return m.pickSelectedFileEntry()
	}
	return m, nil
}

func (m Model) updateComposerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		if m.pathSuggestionFocus {
			m.pathSuggestionFocus = false
			return m, nil
		}
		m.abandonCompose()
		return m, nil
	case "tab":
		if len(m.pathSuggestions) > 0 {
			if !m.pathSuggestionFocus {
				m.pathSuggestionFocus = true
				return m, nil
			}
			if m.applySelectedPathSuggestion() {
				return m, nil
			}
		}
	case "ctrl+o":
		m.openFilePicker()
		return m, nil
	case "+":
		// Insert directly so "+" always lands in the draft — even if
		// the textarea is blurred — instead of reading as the attach
		// toolbar button.
		m.composer.InsertRune('+')
		m.refreshPathSuggestions()
		return m, nil
	case "ctrl+n":
		if len(m.pathSuggestions) > 0 {
			m.pathSuggestionIdx = cycleSelection(m.pathSuggestionIdx, 1, len(m.pathSuggestions))
			return m, nil
		}
	case "ctrl+p":
		if len(m.pathSuggestions) > 0 {
			m.pathSuggestionIdx = cycleSelection(m.pathSuggestionIdx, -1, len(m.pathSuggestions))
			return m, nil
		}
	case "j", "down":
		if m.pathSuggestionFocus && len(m.pathSuggestions) > 0 {
			m.pathSuggestionIdx = cycleSelection(m.pathSuggestionIdx, 1, len(m.pathSuggestions))
			return m, nil
		}
	case "k", "up":
		if m.pathSuggestionFocus && len(m.pathSuggestions) > 0 {
			m.pathSuggestionIdx = cycleSelection(m.pathSuggestionIdx, -1, len(m.pathSuggestions))
			return m, nil
		}
	case "ctrl+v":
		return m, stageClipboardImageCmd(m.clipboard, m.nextImagePlaceholder())
	case "alt+v":
		return m.toggleVoiceRecording()
	case "ctrl+j", "alt+enter", "shift+enter":
		// ctrl+j is the reliable newline: terminals send a plain CR for
		// shift+enter, indistinguishable from enter, so "shift+enter" only
		// fires in the rare setups that report it distinctly.
		m.composer.InsertString("\n")
		m.refreshPathSuggestions()
		return m, nil
	case "enter":
		return m.submitComposer()
	}
	var cmd tea.Cmd
	m.composer, cmd = m.composer.Update(msg)
	if msg.String() == ":" {
		// Closing colon of a :shortcode: at the end of the draft. Only the
		// suffix is eligible: SetValue moves the cursor to the end, which is
		// correct only when the user was already typing there.
		if value := m.composer.Value(); value != replaceTrailingEmojiShortcode(value) {
			m.composer.SetValue(replaceTrailingEmojiShortcode(value))
		}
	}
	m.refreshPathSuggestions()
	if len(m.pathSuggestions) == 0 {
		m.pathSuggestionFocus = false
	}
	return m, cmd
}

// submitComposer sends the current draft: staged attachments first, else the
// parsed compose action (/image, /media, or plain text).
func (m Model) submitComposer() (tea.Model, tea.Cmd) {
	if m.pathSuggestionFocus && len(m.pathSuggestions) > 0 {
		if m.applySelectedPathSuggestion() {
			return m, nil
		}
	}
	chatID := m.currentChatID
	if len(m.pendingAttachments) > 0 {
		attachments := append([]stagedAttachment(nil), m.pendingAttachments...)
		caption := stripAttachmentTokens(m.composer.Value(), attachments)
		return m, sendStagedAttachmentsCmd(m.transport, chatID, attachments, caption)
	}
	action, err := parseComposeAction(m.composer.Value())
	if err != nil {
		m.lastErr = err.Error()
		return m, nil
	}
	switch action.kind {
	case composeActionImage:
		return m, sendImageCmd(m.transport, chatID, action.path, action.caption)
	case composeActionMedia:
		return m, sendMediaCmd(m.transport, chatID, action.path, action.caption)
	default:
		text, mentionJIDs := applyDraftMentions(action.text, m.draftMentions)
		return m, sendTextCmd(m.transport, chatID, text, mentionJIDs)
	}
}

func (m Model) updateThreadNavigationKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = viewChats
		m.currentChatID = ""
		m.messages = nil
		m.mentionNames = nil
		m.threadNewWhileAway = 0
		m.selecting = false
		m.mediaPickerOpen = false
		m.threadHistoryPending = false
		m.threadLoadingOlder = false
		m.threadMessageLimit = 0
		m.threadScroll = 0
		m.abandonCompose()
		return m, m.loadChats(m.search.Value())
	case "i", "tab":
		m.composing = true
		m.refreshPathSuggestions()
		return m, m.composer.Focus()
	case "k", "up":
		return m.scrollThread(1)
	case "j", "down":
		return m.scrollThread(-1)
	case "pgup", "ctrl+u":
		return m.scrollThread(threadPageScroll)
	case "pgdown", "ctrl+d":
		return m.scrollThread(-threadPageScroll)
	case "home":
		return m.scrollThread(max(1, m.maxThreadScroll()))
	case "end":
		m.threadScroll = 0
		m.threadNewWhileAway = 0
		return m, nil
	case "u":
		if m.currentChatID == "" {
			return m, nil
		}
		var cmd tea.Cmd
		m, cmd = m.loadOlderThreadMessages()
		return m, cmd
	case "d":
		if m.currentChatID == "" {
			return m, nil
		}
		latest := latestDownloadableMessage(m.messages)
		if latest == nil {
			m.lastErr = "no downloadable media found in this thread"
			return m, nil
		}
		return m, downloadMediaCmd(m.transport, *latest, m.downloadDir)
	case "p":
		return m.toggleVoicePlayback()
	case "r":
		if len(m.messages) == 0 {
			return m, nil
		}
		m.selecting = true
		m.selectIndex = len(m.messages) - 1
		m.alignScrollToSelectCursor()
		return m, nil
	case "m":
		items := m.mediaMessages()
		if len(items) == 0 {
			m.lastErr = "no media in the loaded messages"
			return m, nil
		}
		m.mediaPickerOpen = true
		m.mediaPickerIndex = len(items) - 1
		return m, nil
	}
	return m, nil
}

// mediaMessages returns every loaded message carrying media, in thread order.
func (m Model) mediaMessages() []domain.Message {
	var out []domain.Message
	for _, msg := range m.messages {
		if msg.MediaKind != domain.MediaKindNone {
			out = append(out, msg)
		}
	}
	return out
}

// updateMediaPickerKey drives the media picker overlay: j/k move (wrapping),
// enter/d download the selected item, p plays audio, esc/m close. The media
// slice is re-derived on every keypress so a background reload cannot leave a
// stale index behind.
func (m Model) updateMediaPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	items := m.mediaMessages()
	if len(items) == 0 {
		m.mediaPickerOpen = false
		return m, nil
	}
	m.mediaPickerIndex = clampSelection(m.mediaPickerIndex, len(items))
	switch msg.String() {
	case "esc", "m":
		m.mediaPickerOpen = false
		return m, nil
	case "j", "down":
		m.mediaPickerIndex = cycleSelection(m.mediaPickerIndex, 1, len(items))
		return m, nil
	case "k", "up":
		m.mediaPickerIndex = cycleSelection(m.mediaPickerIndex, -1, len(items))
		return m, nil
	case "enter", "d":
		return m.downloadSelectedMessage(items[m.mediaPickerIndex])
	case "p":
		return m.playSelectedMessage(items[m.mediaPickerIndex])
	}
	return m, nil
}

// toggleVoicePlayback plays the latest voice note or audio message in the
// thread (downloading it first when needed), or stops an active playback.
func (m Model) toggleVoicePlayback() (tea.Model, tea.Cmd) {
	if m.currentChatID == "" || m.player == nil {
		return m, nil
	}
	if m.player.Playing() {
		_ = m.player.Stop()
		m.status = "Playback stopped"
		return m, nil
	}
	latest := latestVoiceMessage(m.messages)
	if latest == nil {
		m.lastErr = "no voice notes or audio found in this thread"
		return m, nil
	}
	m.status = "Starting playback..."
	return m, playVoiceCmd(m.transport, m.player, *latest, m.downloadDir)
}

// abandonCompose leaves compose mode and discards the draft's transient
// state: an in-flight voice recording, staged attachments, path suggestions,
// and the file picker.
func (m *Model) abandonCompose() {
	m.composing = false
	m.quitArmed = m.quitAfterNavigation
	m.composer.Blur()
	if m.recordingVoice && m.recorder != nil {
		_ = m.recorder.Cancel()
		m.recordingVoice = false
	}
	m.clearPendingAttachments()
	m.clearPathSuggestions()
	m.draftMentions = nil
	m.filePickerOpen = false
}

func (m Model) applyTransportEvent(event domain.Event) Model {
	switch event.Type {
	case domain.EventStatus:
		if event.Status != "" {
			m.status = event.Status
			if event.Status == "connected" {
				m.qrCode = ""
			}
			if strings.Contains(strings.ToLower(event.Status), "recent chats") || strings.Contains(strings.ToLower(event.Status), "passive history sync") {
				m.syncingRecent = true
			}
			if strings.EqualFold(event.Status, "History sync updated") && len(m.chats) > 0 {
				m.syncingRecent = false
			}
		}
	case domain.EventQRCode:
		m.qrCode = renderQRCode(event.QRCode)
		if event.Status != "" {
			m.status = event.Status
		}
	case domain.EventError:
		if event.Err != nil {
			m.lastErr = event.Err.Error()
			m.status = "An error occurred"
		}
	}
	return m
}

func (m Model) renderPairing() string {
	header := m.renderHeader("Pair device", "Scan the QR code from WhatsApp")
	step := func(n int, label string) string {
		return chipKeyStyle.Render(fmt.Sprintf("%d", n)) + subtleStyle.Render(" · ") + slateStyle.Render(label)
	}
	steps := lipgloss.JoinHorizontal(lipgloss.Top,
		step(1, "Open WhatsApp"),
		subtleStyle.Render("   →   "),
		step(2, "Linked Devices"),
		subtleStyle.Render("   →   "),
		step(3, "Scan below"),
	)
	caption := slateStyle.Render("Awaiting handshake from your phone…")
	if m.lastErr != "" {
		caption = errorStyle.Render("✕ ") + slateStyle.Render(m.lastErr)
	}
	qr := qrBoxStyle.Render(m.qrCode)
	footnote := subtleStyle.Render("[") + chipKeyStyle.Render("esc·q") + subtleStyle.Render("]") + " " + chipLabelStyle.Render("quit")
	content := lipgloss.JoinVertical(
		lipgloss.Center,
		steps,
		"",
		caption,
		"",
		qr,
		"",
		footnote,
	)
	return lipgloss.JoinVertical(lipgloss.Left, header, lipgloss.Place(m.width, max(10, m.height-4), lipgloss.Center, lipgloss.Top, content))
}

// chatListViewLayout captures the per-frame geometry of the chat-list view.
// Narrow terminals (contentWidth < 68) stack all panels full-width; wider
// ones put search+list in a left column and the preview on the right.
type chatListViewLayout struct {
	contentWidth  int
	searchHeight  int
	listHeight    int
	previewHeight int
	stacked       bool
}

func (m Model) chatListLayout() chatListViewLayout {
	contentWidth := max(48, m.width-2)
	header := m.renderHeader("Inbox", "")
	footer := m.renderFooter(chatListHelpText)
	bodyHeight := max(8, m.height-countRenderedLines(header)-countRenderedLines(footer)-1)
	layout := chatListViewLayout{
		contentWidth: contentWidth,
		searchHeight: 3,
		stacked:      contentWidth < 68,
	}
	if layout.stacked {
		layout.previewHeight = max(8, bodyHeight/3)
		layout.listHeight = max(8, bodyHeight-layout.searchHeight-layout.previewHeight)
		return layout
	}
	layout.listHeight = max(8, bodyHeight-layout.searchHeight)
	layout.previewHeight = bodyHeight
	return layout
}

func (m Model) renderChatList() string {
	header := m.renderHeader("Inbox", "")
	footer := m.renderFooter(chatListHelpText)
	layout := m.chatListLayout()
	if layout.stacked {
		width := layout.contentWidth
		searchBar := renderPanel(boxMutedStyle, width, layout.searchHeight, m.searchBarText(width-boxMutedStyle.GetHorizontalFrameSize()))
		chatList := renderPanel(boxStyle, width, layout.listHeight, m.chatListBody(width-boxStyle.GetHorizontalFrameSize(), paddedContentHeight(layout.listHeight)))
		preview := renderPanel(boxStyle, width, layout.previewHeight, m.chatPreviewBody(width-boxStyle.GetHorizontalFrameSize(), paddedContentHeight(layout.previewHeight)))
		return lipgloss.JoinVertical(lipgloss.Left, header, searchBar, chatList, preview, footer)
	}

	leftWidth, rightWidth := m.splitWidths(layout.contentWidth)
	searchBar := renderPanel(boxMutedStyle, leftWidth, layout.searchHeight, m.searchBarText(leftWidth-boxMutedStyle.GetHorizontalFrameSize()))
	chatList := renderPanel(boxStyle, leftWidth, layout.listHeight, m.chatListBody(leftWidth-boxStyle.GetHorizontalFrameSize(), paddedContentHeight(layout.listHeight)))
	preview := renderPanel(boxStyle, rightWidth, layout.previewHeight, m.chatPreviewBody(rightWidth-boxStyle.GetHorizontalFrameSize(), paddedContentHeight(layout.previewHeight)))
	leftColumn := lipgloss.JoinVertical(lipgloss.Left, searchBar, chatList)
	body := lipgloss.JoinHorizontal(lipgloss.Top, leftColumn, lipgloss.NewStyle().Width(1).Render(""), preview)
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

// threadViewLayout captures the per-frame geometry of the thread view.
// renderThread and threadToolbarHit both read it, so rendering and mouse
// hit-testing can never disagree about where a row sits.
type threadViewLayout struct {
	contentWidth    int
	headerLines     int
	messageHeight   int
	composerHeight  int
	composerContent string
}

func (m Model) threadLayout() threadViewLayout {
	contentWidth := max(48, m.width-2)
	header := m.renderHeader(m.threadTitle(), "")
	footer := m.renderFooter(m.threadHelpText())
	bodyHeight := max(8, m.height-countRenderedLines(header)-countRenderedLines(footer)-1)
	composerContent := m.composerBody(contentWidth - boxMutedStyle.GetHorizontalFrameSize())
	composerHeight := max(3, countRenderedLines(composerContent)+boxMutedStyle.GetVerticalFrameSize())
	return threadViewLayout{
		contentWidth:    contentWidth,
		headerLines:     countRenderedLines(header),
		messageHeight:   max(8, bodyHeight-composerHeight),
		composerHeight:  composerHeight,
		composerContent: composerContent,
	}
}

func (m Model) renderThread() string {
	subtitle := ""
	if m.threadScroll > 0 {
		subtitle = "↑ history"
		if m.threadNewWhileAway > 0 {
			subtitle = fmt.Sprintf("↓ %d new", m.threadNewWhileAway)
		}
	}
	header := m.renderHeader(m.threadTitle(), subtitle)
	footer := m.renderFooter(m.threadHelpText())
	if m.composing {
		contentWidth := max(48, m.width-2)
		bodyHeight := max(8, m.height-countRenderedLines(header)-countRenderedLines(footer)-1)
		m.resizeComposer(contentWidth-boxMutedStyle.GetHorizontalFrameSize(), max(3, bodyHeight/3))
	}
	layout := m.threadLayout()
	viewport := paddedContentHeight(layout.messageHeight)
	lineWidth := layout.contentWidth - boxStyle.GetHorizontalFrameSize()
	var messages string
	if m.mediaPickerOpen {
		messages = renderPanel(boxStyle, layout.contentWidth, layout.messageHeight, m.mediaPickerBody(viewport, lineWidth))
	} else {
		messages = renderPanel(boxStyle, layout.contentWidth, layout.messageHeight, m.threadBody(viewport, lineWidth))
		messages = m.overlayScrollThumb(messages, viewport, lineWidth)
	}
	composer := renderPanel(boxMutedStyle, layout.contentWidth, layout.composerHeight, layout.composerContent)
	return lipgloss.JoinVertical(lipgloss.Left, header, messages, composer, footer)
}

// overlayScrollThumb draws a scroll-position thumb onto the right border of
// the rendered messages panel. Nothing is drawn when the whole thread fits.
func (m Model) overlayScrollThumb(panel string, viewport, lineWidth int) string {
	total := len(m.threadMessageLines(lineWidth))
	maxScroll := max(0, total-viewport)
	if maxScroll == 0 {
		return panel
	}
	lines := strings.Split(panel, "\n")
	inner := len(lines) - 2
	if inner < 2 {
		return panel
	}
	scroll := min(max(0, m.threadScroll), maxScroll)
	thumb := max(1, inner*viewport/total)
	if thumb >= inner {
		return panel
	}
	// scroll counts lines up from the bottom: scroll 0 pins the thumb to
	// the bottom of the track, maxScroll to the top.
	track := inner - thumb
	top := track - (track*scroll+maxScroll/2)/maxScroll
	for i := top; i < top+thumb && i+1 < len(lines)-1; i++ {
		row := i + 1
		width := lipgloss.Width(lines[row])
		lines[row] = ansi.Truncate(lines[row], width-1, "") + railStyle.Render("█")
	}
	return strings.Join(lines, "\n")
}

// renderHelp draws the full-screen keybinding reference opened with "?".
func (m Model) renderHelp() string {
	header := m.renderHeader("Help", "")
	footer := m.renderFooter("? or esc close  ctrl+c quit")
	contentWidth := max(48, m.width-2)
	bodyHeight := max(8, m.height-countRenderedLines(header)-countRenderedLines(footer)-1)
	innerWidth := contentWidth - boxStyle.GetHorizontalFrameSize()

	// Two columns so all four sections fit typical terminal heights.
	columnWidth := max(20, (innerWidth-4)/2)
	section := func(title string, rows [][2]string) []string {
		lines := []string{smallCap(title, columnWidth)}
		for _, row := range rows {
			lines = append(lines, actionRow(row[0], row[1], columnWidth))
		}
		lines = append(lines, "")
		return lines
	}
	var left []string
	left = append(left, section("Inbox", [][2]string{
		{"j/k", "move selection"}, {"enter", "open thread"}, {"/", "filter inbox"},
		{"r", "reload cache"}, {"T", "cycle theme"}, {"esc·q", "quit"},
	})...)
	left = append(left, section("Thread", [][2]string{
		{"j/k", "scroll one line"}, {"pgup/pgdn", "scroll a page"}, {"home/end", "oldest · latest"},
		{"i·tab", "compose"}, {"u", "load older history"}, {"d", "download latest media"},
		{"p", "play · stop voice note"}, {"r", "select message (react · save · play)"}, {"m", "browse chat media"},
		{"esc", "back to inbox"},
	})...)
	var right []string
	right = append(right, section("Compose", [][2]string{
		{"enter", "send"}, {"ctrl+j", "newline"}, {"ctrl+o", "file picker"},
		{"ctrl+v", "paste clipboard image"}, {"alt+v", "record voice note"},
		{"@name", "tag someone"}, {":code:", "insert emoji"}, {"tab", "suggestions"}, {"esc", "cancel draft"},
	})...)
	right = append(right, section("Global", [][2]string{
		{"?", "this help"}, {"ctrl+l", "force repaint"}, {"ctrl+c", "quit"},
	})...)

	rows := max(len(left), len(right))
	body := make([]string, 0, rows)
	for i := 0; i < rows; i++ {
		leftCell, rightCell := "", ""
		if i < len(left) {
			leftCell = left[i]
		}
		if i < len(right) {
			rightCell = right[i]
		}
		pad := max(0, columnWidth-ansi.StringWidth(leftCell))
		body = append(body, leftCell+strings.Repeat(" ", pad)+"    "+rightCell)
	}

	panel := renderPanel(boxStyle, contentWidth, bodyHeight, strings.Join(body, "\n"))
	return lipgloss.JoinVertical(lipgloss.Left, header, panel, footer)
}

func (m Model) threadHelpText() string {
	if m.mediaPickerOpen {
		return "enter·d save  p play  j/k move  esc close"
	}
	if m.selecting {
		return "1-6 react  x unreact  d save  p play  j/k pick  esc done"
	}
	if m.composing {
		if m.quitAfterNavigation {
			return "enter send  ctrl+j newline  esc cancel  ctrl+o files  alt+v voice  ctrl+v paste  esc·q quit"
		}
		return "enter send  ctrl+j newline  esc cancel  ctrl+o files  alt+v voice  ctrl+v paste"
	}
	return "esc back  j/k scroll  i compose  r select  m media  u history  d download  p play  ? help"
}

func (m Model) currentChat() *domain.ChatSummary {
	if len(m.chats) == 0 || m.selected < 0 || m.selected >= len(m.chats) {
		return nil
	}
	chat := m.chats[m.selected]
	return &chat
}

func (m Model) selectedChatJID() string {
	if chat := m.currentChat(); chat != nil {
		return chat.JID
	}
	return m.currentChatID
}

func (m Model) threadTitle() string {
	if chat := m.currentChat(); chat != nil && chat.JID == m.currentChatID {
		return displayChatTitle(*chat)
	}
	for _, chat := range m.chats {
		if chat.JID == m.currentChatID {
			return displayChatTitle(chat)
		}
	}
	return displayTitleFromJID(m.currentChatID)
}

// displayChatTitle returns a friendly title for a chat row. WhatsApp groups
// often arrive without metadata; in those cases the cached row may have an
// empty title (current code) or a raw JID baked in (older code, before the
// store fix). Either way we render something readable and stable.
func displayChatTitle(chat domain.ChatSummary) string {
	title := collapseWhitespace(chat.Title)
	reason := titleFallbackReason(chat.JID, title)
	if reason == "" {
		uilog("displayChatTitle.passthrough", "jid", chat.JID, "title_raw", chat.Title, "title_clean", title)
		return title
	}
	out := displayTitleFromJID(chat.JID)
	uilog("displayChatTitle.fallback", "reason", reason, "jid", chat.JID, "title_raw", chat.Title, "out", out)
	return out
}

// titleFallbackReason reports why a stored title is unusable for display, or
// "" when it is fine as-is.
func titleFallbackReason(jid, title string) string {
	switch {
	case title == "":
		return "empty"
	case title == jid:
		return "title_eq_jid"
	case title == jidUserPart(jid):
		return "title_eq_jid_user"
	case isPhoneLikeArtifact(title):
		return "phone_like_artifact"
	case isLongDigitRun(title):
		return "long_digit_run"
	default:
		return ""
	}
}

func isLongDigitRun(s string) bool {
	if len(s) < 10 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isPhoneLikeArtifact(s string) bool {
	s = collapseWhitespace(s)
	if s == "" {
		return false
	}
	digits := 0
	hasPhoneMarker := strings.HasPrefix(s, "+")
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '+' || r == ' ' || r == '-' || r == '(' || r == ')' || r == '.':
			// phone formatting
		case r == '∙' || r == '•' || r == '·' || r == '*':
			hasPhoneMarker = true
		default:
			return false
		}
	}
	return hasPhoneMarker && digits >= 4
}

// displaySenderLabel masks raw JID-shaped sender values (digit-only strings
// or full JIDs) into a stable short label so group previews don't read like
// "234273336496234: hello". A real contact name passes through untouched.
func displaySenderLabel(name string) string {
	name = collapseWhitespace(name)
	if name == "" {
		return ""
	}
	// Full JID like "234273336496234@s.whatsapp.net" -> use user part.
	user := jidUserPart(name)
	if user == "" {
		user = name
	}
	if isPhoneLikeArtifact(name) {
		return digitTailLabel(name)
	}
	if isLongDigitRun(user) {
		return digitTailLabel(user)
	}
	return name
}

func digitTailLabel(input string) string {
	tail := digitTail(input)
	if tail == "" {
		return "…"
	}
	return "…" + tail
}

func digitTail(input string) string {
	digits := make([]rune, 0, len(input))
	for _, r := range input {
		if r >= '0' && r <= '9' {
			digits = append(digits, r)
		}
	}
	if n := len(digits); n > 4 {
		digits = digits[n-4:]
	}
	return string(digits)
}

// collapseWhitespace joins newlines/tabs/runs of spaces into a single space.
// Used by display helpers so styled lines stay on a single visual row even
// when upstream titles contain embedded line breaks.
func collapseWhitespace(s string) string {
	if s == "" {
		return s
	}
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	return strings.Join(strings.Fields(s), " ")
}

func jidUserPart(jid string) string {
	jid = strings.TrimSpace(jid)
	if idx := strings.IndexByte(jid, '@'); idx > 0 {
		return jid[:idx]
	}
	return ""
}

func displayTitleFromJID(jid string) string {
	jid = strings.TrimSpace(jid)
	if jid == "" {
		return "Unnamed chat"
	}
	at := strings.IndexByte(jid, '@')
	user := jid
	server := ""
	if at >= 0 {
		user = jid[:at]
		server = jid[at+1:]
	}
	switch server {
	case "g.us", "broadcast":
		// Group/broadcast IDs are long opaque digit strings. Show the last
		// four so two unknowns are still distinguishable.
		label := "Unnamed group"
		if server == "broadcast" {
			label = "Broadcast list"
		}
		return digitLabel(label, user)
	case "s.whatsapp.net":
		if user != "" {
			return digitLabel("Unknown contact", user)
		}
	case "lid":
		// WhatsApp Linked ID — opaque digit string, treat as anonymous contact.
		return digitLabel("Linked contact", user)
	case "":
		// No '@' at all. If it's a long digit run, treat as a phone number;
		// otherwise fall through to the raw jid.
		if isLongDigitRun(user) {
			return digitLabel("Unknown contact", user)
		}
	}
	// Last-resort: if the user portion is digit-only, render a friendly
	// label rather than dumping the raw jid.
	if isLongDigitRun(user) {
		return digitLabel("Unknown contact", user)
	}
	return jid
}

func digitLabel(prefix, user string) string {
	tail := digitTail(user)
	if tail == "" {
		return prefix
	}
	return prefix + " · …" + tail
}

func waitForTransportEvent(events <-chan domain.Event) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-events
		if !ok {
			return transportEventMsg{event: domain.Event{Type: domain.EventStatus, Status: "event channel closed"}}
		}
		return transportEventMsg{event: event}
	}
}

func loadChatsCmd(repo *appstore.Store, query string, limit int) tea.Cmd {
	if limit <= 0 {
		limit = defaultChatListLimit
	}
	return func() tea.Msg {
		ctx := context.Background()
		chats, err := repo.ListChats(ctx, query, limit)
		var mentions map[string]string
		if err == nil {
			previews := make([]string, 0, len(chats))
			for _, chat := range chats {
				previews = append(previews, chat.LastMessagePreview)
			}
			mentions = resolveMentionNames(func(jid string) string {
				name, lookupErr := repo.ContactName(ctx, jid)
				if lookupErr != nil {
					return ""
				}
				return name
			}, previews)
		}
		return chatsLoadedMsg{chats: chats, mentions: mentions, err: err}
	}
}

func (m Model) loadChats(query string) tea.Cmd {
	return loadChatsCmd(m.repo, query, m.chatListLimit)
}

// dumpChatListDiagnostics writes the raw chat-list state to the debug log.
// Each row gets a structured entry capturing the field values that drive the
// renderer so we can diff "what we have" against "what we render".
func (m Model) dumpChatListDiagnostics() {
	if m.logger == nil {
		return
	}
	m.logger.Debug("ui:dump.begin", "chats", len(m.chats), "selected", m.selected, "search", m.search.Value())
	for idx, chat := range m.chats {
		m.logger.Debug("ui:dump.chat",
			"idx", idx,
			"jid", chat.JID,
			"title_raw", chat.Title,
			"title_quoted", fmt.Sprintf("%q", chat.Title),
			"is_group", chat.IsGroup,
			"unread", chat.UnreadCount,
			"sender_raw", chat.LastSenderName,
			"sender_quoted", fmt.Sprintf("%q", chat.LastSenderName),
			"preview_raw", chat.LastMessagePreview,
			"display_title", displayChatTitle(chat),
			"display_sender", displaySenderLabel(chat.LastSenderName),
			"last_at", chat.LastMessageAt,
		)
	}
	m.logger.Debug("ui:dump.end")
}

func loadMessagesCmd(repo *appstore.Store, chatJID string, limit int) tea.Cmd {
	if limit <= 0 {
		limit = messageLimit
	}
	return func() tea.Msg {
		ctx := context.Background()
		messages, err := repo.ListMessages(ctx, chatJID, limit)
		var mentions map[string]string
		if err == nil {
			texts := make([]string, 0, len(messages))
			for _, msg := range messages {
				texts = append(texts, msg.Text)
			}
			mentions = resolveMentionNames(func(jid string) string {
				name, lookupErr := repo.ContactName(ctx, jid)
				if lookupErr != nil {
					return ""
				}
				return name
			}, texts)
		}
		return messagesLoadedMsg{chatJID: chatJID, messages: messages, mentions: mentions, limit: limit, err: err}
	}
}

func resetUnreadCmd(repo *appstore.Store, chatJID string) tea.Cmd {
	return func() tea.Msg {
		err := repo.ResetUnread(context.Background(), chatJID)
		return opResultMsg{err: err}
	}
}

func sendTextCmd(transport domain.Transport, chatJID, text string, mentionJIDs []string) tea.Cmd {
	return func() tea.Msg {
		err := transport.SendText(context.Background(), chatJID, text, mentionJIDs...)
		return opResultMsg{err: err, status: "Message sent", chatJID: chatJID, refresh: err == nil, clearComposer: err == nil}
	}
}

func sendImageCmd(transport domain.Transport, chatJID, path, caption string) tea.Cmd {
	return func() tea.Msg {
		err := transport.SendImage(context.Background(), chatJID, path, caption)
		return opResultMsg{err: err, status: "Image sent", chatJID: chatJID, refresh: err == nil, clearComposer: err == nil}
	}
}

func sendMediaCmd(transport domain.Transport, chatJID, path, caption string) tea.Cmd {
	return func() tea.Msg {
		err := transport.SendMedia(context.Background(), chatJID, path, caption)
		return opResultMsg{err: err, status: "Media sent", chatJID: chatJID, refresh: err == nil, clearComposer: err == nil}
	}
}

func stageClipboardImageCmd(clipboard clipboardReader, token string) tea.Cmd {
	return func() tea.Msg {
		if clipboard == nil {
			return attachmentStagedMsg{err: fmt.Errorf("clipboard image access is unavailable")}
		}
		imageData, err := clipboard.ReadImage()
		if err != nil {
			return attachmentStagedMsg{err: err}
		}
		file, err := os.CreateTemp("", "whatsapp-terminal-clipboard-*.png")
		if err != nil {
			return attachmentStagedMsg{err: fmt.Errorf("create clipboard image file: %w", err)}
		}
		path := file.Name()
		if _, err := file.Write(imageData); err != nil {
			_ = file.Close()
			_ = os.Remove(path)
			return attachmentStagedMsg{err: fmt.Errorf("write clipboard image: %w", err)}
		}
		if err := file.Close(); err != nil {
			_ = os.Remove(path)
			return attachmentStagedMsg{err: fmt.Errorf("close clipboard image file: %w", err)}
		}
		return attachmentStagedMsg{
			attachment: stagedAttachment{token: token, path: path, kind: domain.MediaKindImage, temp: true},
			status:     "Clipboard image pasted into draft",
		}
	}
}

func stopVoiceRecordingCmd(recorder voiceRecorder, token string) tea.Cmd {
	return func() tea.Msg {
		if recorder == nil {
			return attachmentStagedMsg{err: fmt.Errorf("voice recorder is unavailable")}
		}
		result, err := recorder.Stop()
		if err != nil {
			return attachmentStagedMsg{err: err}
		}
		return attachmentStagedMsg{
			attachment: stagedAttachment{token: token, path: result.Path, kind: domain.MediaKindVoice, secs: result.Duration, temp: true},
			status:     "Voice note added to draft",
		}
	}
}

func sendStagedAttachmentsCmd(transport domain.Transport, chatJID string, attachments []stagedAttachment, draft string) tea.Cmd {
	return func() tea.Msg {
		// The draft text captions the first image/media attachment; voice
		// notes cannot carry captions. Whatever is left is sent as text.
		caption := strings.TrimSpace(draft)
		for _, attachment := range attachments {
			var err error
			switch attachment.kind {
			case domain.MediaKindVoice:
				err = transport.SendVoiceNote(context.Background(), chatJID, attachment.path, attachment.secs)
			case domain.MediaKindImage:
				err = transport.SendImage(context.Background(), chatJID, attachment.path, caption)
				caption = ""
			default:
				err = transport.SendMedia(context.Background(), chatJID, attachment.path, caption)
				caption = ""
			}
			if err != nil {
				return opResultMsg{err: err}
			}
		}
		if caption != "" {
			if err := transport.SendText(context.Background(), chatJID, caption); err != nil {
				return opResultMsg{err: err}
			}
		}
		status := "Attachment sent"
		if len(attachments) > 1 {
			status = fmt.Sprintf("%d attachments sent", len(attachments))
		}
		return opResultMsg{
			status:           status,
			chatJID:          chatJID,
			refresh:          true,
			clearComposer:    true,
			clearAttachments: true,
		}
	}
}

func requestHistoryCmd(transport domain.Transport, chatJID string, count int) tea.Cmd {
	return func() tea.Msg {
		err := transport.RequestHistory(context.Background(), chatJID, count)
		result := opResultMsg{err: err, historyRequest: true}
		if err == nil {
			result.status = "Requested older messages"
		}
		return result
	}
}

func downloadMediaCmd(transport domain.Transport, msg domain.Message, downloadDir string) tea.Cmd {
	return func() tea.Msg {
		path, err := transport.DownloadMedia(context.Background(), msg, downloadDir)
		if err != nil {
			return opResultMsg{err: err}
		}
		return opResultMsg{
			status:  fmt.Sprintf("Downloaded media to %s", filepath.Base(path)),
			chatJID: msg.ChatJID,
			refresh: true,
		}
	}
}

func clampSelection(selected, total int) int {
	if total == 0 {
		return 0
	}
	if selected < 0 {
		return 0
	}
	if selected >= total {
		return total - 1
	}
	return selected
}

// cycleSelection moves a list selection by delta, wrapping at both ends.
// Used by the small pop-up lists (file picker, path suggestions) where
// wrap-around navigation is expected.
func cycleSelection(selected, delta, total int) int {
	if total <= 0 {
		return 0
	}
	selected = (selected + delta) % total
	if selected < 0 {
		selected += total
	}
	return selected
}

func renderQRCode(code string) string {
	if code == "" {
		return ""
	}
	var out bytes.Buffer
	qrterminal.GenerateHalfBlock(code, qrterminal.M, &out)
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderHeader(title, subtitle string) string {
	width := max(40, m.width-2)
	brand := brandStyle.Render("WHATSAPP") + subtleStyle.Render(" · ") + titleStyle.Render("TERMINAL")
	dot, dotLabel := m.statusIndicator()
	section := strings.ToUpper(strings.TrimSpace(title))
	if section == "" {
		section = "INBOX"
	}

	rightParts := []string{}
	if dot != "" {
		rightParts = append(rightParts, dot+" "+mutedStyle.Render(dotLabel))
	}
	if subtitle != "" {
		rightParts = append(rightParts, slateStyle.Render(truncateText(subtitle, max(16, width/3))))
	}
	right := strings.Join(rightParts, mutedStyle.Render("  ·  "))

	// The header must stay on a single line: a wrapped header shifts every
	// panel below it. When space runs short, shed chrome in order — compact
	// the brand, drop the status label, drop the status dot — and clip the
	// section title only as a last resort, since identifying the open chat
	// matters more than branding or "live".
	budget := width - headerStyle.GetHorizontalPadding()
	needed := func() int {
		n := lipgloss.Width(brand) + 3 + 4 + ansi.StringWidth(section) // brand gap + "— " + " —"
		if right != "" {
			n += 1 + lipgloss.Width(right)
		}
		return n
	}
	if needed() > budget {
		brand = brandStyle.Render("WHATSAPP")
	}
	if needed() > budget && dot != "" {
		right = dot
	}
	if needed() > budget {
		right = ""
	}
	sectionMax := budget - lipgloss.Width(brand) - 3 - 4
	if right != "" {
		sectionMax -= 1 + lipgloss.Width(right)
	}
	section = truncateText(section, max(8, sectionMax))
	sectionLabel := subtleStyle.Render("— ") + sectionStyle.Render(section) + subtleStyle.Render(" —")

	leftCluster := brand + "   " + sectionLabel
	gap := max(1, budget-lipgloss.Width(leftCluster)-lipgloss.Width(right))
	topLine := headerStyle.Width(width).Render(leftCluster + strings.Repeat(" ", gap) + right)
	rule := hairlineStyle.Render(strings.Repeat("─", width))
	return lipgloss.JoinVertical(lipgloss.Left, "", topLine, rule)
}

func (m Model) statusIndicator() (string, string) {
	switch {
	case m.lastErr != "" && !m.ready:
		return statusDotErrStyle.Render("●"), "offline"
	case m.syncingRecent:
		return statusDotWarnStyle.Render(spinnerFrames[m.spinnerFrame%len(spinnerFrames)]), "syncing"
	case m.ready:
		return statusDotOnStyle.Render("●"), "live"
	default:
		return subtleStyle.Render("○"), "idle"
	}
}

func (m Model) renderFooter(help string) string {
	width := max(40, m.width-2)
	rule := hairlineStyle.Render(strings.Repeat("─", width))
	chips := renderChipHelp(help, width)
	// Always emit exactly three lines: rule, chips, status. Keeping the
	// height stable between frames is what lets Bubble Tea's diff renderer
	// fully overwrite the upper panels — otherwise a longer previous frame
	// (with an error) leaves stale cells when the error clears.
	statusLine := ""
	switch {
	case m.lastErr != "":
		statusLine = errorStyle.Render("✕ ") + slateStyle.Render(truncateText(m.lastErr, max(10, width-4)))
	case strings.TrimSpace(m.status) != "":
		statusLine = subtleStyle.Render(truncateText(m.status, max(10, width-2)))
	}
	return strings.Join([]string{
		rule,
		footerStyle.Width(width).Render(chips),
		footerStyle.Width(width).Render(statusLine),
	}, "\n")
}

// renderChipHelp parses a "key label  key label" string and renders chips:
// `[key] label   [key] label`. The key for each pair is everything up to the
// last space inside that pair; everything after the last space is the label.
// Chips are appended only while they fit within width; the rest are dropped.
func renderChipHelp(help string, width int) string {
	help = strings.TrimSpace(help)
	if help == "" {
		return ""
	}
	parts := strings.Split(help, "  ")
	sep := subtleStyle.Render("   ")
	var b strings.Builder
	used := 0
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		idx := strings.LastIndex(p, " ")
		key, label := p, ""
		if idx > 0 {
			key, label = p[:idx], p[idx+1:]
		}
		var chip strings.Builder
		chip.WriteString(subtleStyle.Render("["))
		chip.WriteString(chipKeyStyle.Render(key))
		chip.WriteString(subtleStyle.Render("]"))
		if label != "" {
			chip.WriteString(" ")
			chip.WriteString(chipLabelStyle.Render(label))
		}
		chipStr := chip.String()
		chipW := lipgloss.Width(chipStr)
		sepW := 0
		if i > 0 {
			sepW = lipgloss.Width(sep)
		}
		if used+sepW+chipW > width {
			break
		}
		if i > 0 {
			b.WriteString(sep)
			used += sepW
		}
		b.WriteString(chipStr)
		used += chipW
	}
	return b.String()
}

func (m Model) searchBarText(width int) string {
	width = max(8, width)
	if m.searching || m.search.Value() != "" {
		prompt := "Search: "
		cursor := ""
		if m.searching {
			cursor = "█"
		}
		valueWidth := max(0, width-lipgloss.Width(prompt)-lipgloss.Width(cursor))
		value := m.search.Value()
		if m.searching {
			// While typing the cursor sits at the end, so keep the tail
			// visible; otherwise long queries hide what is being typed.
			value = truncateTextHead(value, valueWidth)
		} else {
			value = truncateText(value, valueWidth)
		}
		return slateStyle.Render(prompt) + bodyStyle.Render(value) + chipKeyStyle.Render(cursor)
	}
	return slateStyle.Render("filter inbox  ") + chipKeyStyle.Render("/") + slateStyle.Render(" to focus")
}

func (m Model) chatListBody(width, height int) string {
	width = max(18, width)
	height = max(1, height)
	if len(m.chats) == 0 {
		if m.syncingRecent {
			lines := []string{
				smallCap("Syncing recent chats", width),
				slateStyle.Render(wrapText("Waiting for your phone to send the first batch. The inbox appears as soon as recent conversations arrive.", width)),
			}
			if strings.TrimSpace(m.status) != "" {
				lines = append(lines, "", subtleStyle.Render(wrapText("· "+m.status, width)))
			}
			return strings.Join(lines, "\n")
		}
		return slateStyle.Render(wrapText("No cached chats yet.", width)) + "\n\n" +
			subtleStyle.Render(wrapText("Use --demo for an offline dataset, or pair a live session to populate the cache.", width))
	}

	selected := clampSelection(m.selected, len(m.chats))
	visible := chatListVisibleRows(height, len(m.chats))
	start := clampChatListOffset(m.chatListOffset, selected, visible, len(m.chats))
	end := min(len(m.chats), start+visible)
	lines := make([]string, 0, end-start)
	for idx := start; idx < end; idx++ {
		lines = append(lines, renderChatItem(m.chats[idx], width, idx == selected, m.chatMentionNames))
	}
	return strings.Join(lines, "\n")
}

func (m *Model) keepSelectedChatVisible() {
	if len(m.chats) == 0 {
		m.selected = 0
		m.chatListOffset = 0
		return
	}
	m.selected = clampSelection(m.selected, len(m.chats))
	visible := chatListVisibleRows(m.chatListContentHeight(), len(m.chats))
	m.chatListOffset = clampChatListOffset(m.chatListOffset, m.selected, visible, len(m.chats))
}

func (m Model) chatListContentHeight() int {
	return paddedContentHeight(m.chatListLayout().listHeight)
}

func chatListVisibleRows(contentHeight, total int) int {
	if total <= 0 {
		return 0
	}
	return min(total, max(1, contentHeight/chatItemLineCount))
}

func clampChatListOffset(offset, selected, visible, total int) int {
	if total <= 0 || visible <= 0 {
		return 0
	}
	if selected < offset {
		offset = selected
	}
	if selected >= offset+visible {
		offset = selected - visible + 1
	}
	maxOffset := max(0, total-visible)
	if offset > maxOffset {
		offset = maxOffset
	}
	if offset < 0 {
		return 0
	}
	return offset
}

func renderChatItem(chat domain.ChatSummary, width int, selected bool, mentions map[string]string) string {
	width = max(20, width)
	railCol := "  "
	titleStyleApplied := slateStyle
	if selected {
		railCol = railStyle.Render("▌") + " "
		titleStyleApplied = selectedItemStyle
	}
	monogram := senderStyle(chat.JID).Render(chatMonogram(displayChatTitle(chat))) + " "
	// Leave a generous safety margin so styled segments do not wrap on
	// lipgloss's stricter visual-width measurement (combining marks, padding).
	contentWidth := width - 6 - 3 // rail/margins, then "XY " monogram

	unread := ""
	if chat.UnreadCount > 0 {
		count := chat.UnreadCount
		token := fmt.Sprintf("%d", count)
		if count > 99 {
			token = "99+"
		}
		// Subtle bracket frame replaces the prior bg-painted pill, which
		// lipgloss occasionally treated as a block and pushed onto its own
		// visual row. Plain styled text always stays inline.
		unread = subtleStyle.Render("·") + unreadPillStyle.Render(token)
	}
	// Top line: title (truncated) left, unread pill right.
	titleMax := contentWidth - lipgloss.Width(unread)
	if lipgloss.Width(unread) > 0 {
		titleMax--
	}
	title := truncateText(displayChatTitle(chat), max(4, titleMax))
	titleRendered := titleStyleApplied.Render(title)
	topGap := max(1, contentWidth-lipgloss.Width(titleRendered)-lipgloss.Width(unread))
	topLine := railCol + monogram + titleRendered + strings.Repeat(" ", topGap) + unread

	// Bottom line: preview left, relative time right.
	preview := collapseWhitespace(substituteMentions(chat.LastMessagePreview, mentions))
	if preview == "" {
		preview = "No messages yet"
	}
	if chat.LastSenderName != "" && chat.IsGroup {
		preview = displaySenderLabel(chat.LastSenderName) + ": " + preview
	}
	timestamp := ""
	if !chat.LastMessageAt.IsZero() {
		timestamp = formatRelativeTime(chat.LastMessageAt, time.Now())
	}
	timeRendered := subtleStyle.Render(timestamp)
	previewMax := contentWidth - lipgloss.Width(timeRendered) - 1
	if previewMax < 4 {
		previewMax = 4
	}
	preview = truncateText(preview, previewMax)
	previewRendered := mutedStyle.Render(preview)
	botGap := max(1, contentWidth-lipgloss.Width(previewRendered)-lipgloss.Width(timeRendered))
	botLine := "     " + previewRendered + strings.Repeat(" ", botGap) + timeRendered

	// Defensive clamp: every item must be exactly two visible lines. If any
	// upstream styled segment ever emits an embedded newline, drop the tail.
	return clampToLines(topLine+"\n"+botLine, 2)
}

// chatMonogram derives a fixed two-cell initial block from a chat title
// ("Sonu Asansol" -> "SA"), used as a colored poor-man's avatar.
func chatMonogram(title string) string {
	var initials []rune
	for _, field := range strings.Fields(title) {
		for _, r := range field {
			if unicode.IsLetter(r) || unicode.IsNumber(r) {
				initials = append(initials, unicode.ToUpper(r))
			}
			break
		}
		if len(initials) == 2 {
			break
		}
	}
	switch len(initials) {
	case 0:
		return "· "
	case 1:
		return string(initials) + " "
	default:
		return string(initials)
	}
}

func clampToLines(s string, n int) string {
	if n <= 0 {
		return ""
	}
	parts := strings.Split(s, "\n")
	if len(parts) <= n {
		return s
	}
	return strings.Join(parts[:n], "\n")
}

func formatRelativeTime(t, now time.Time) string {
	t = t.Local()
	now = now.Local()
	d := now.Sub(t)
	if d < 0 {
		return t.Format("15:04")
	}
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case sameDay(t, now):
		return t.Format("15:04")
	case sameDay(t.AddDate(0, 0, 1), now):
		return "yest"
	case d < 7*24*time.Hour:
		return t.Format("Mon")
	case t.Year() == now.Year():
		return t.Format("Jan 2")
	default:
		return t.Format("Jan '06")
	}
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

func (m Model) chatPreviewBody(width, height int) string {
	width = max(18, width)
	height = max(1, height)
	chat := m.currentChat()
	if chat == nil {
		return slateStyle.Render(wrapText("Select a chat to inspect it.", width))
	}

	title := truncateText(displayChatTitle(*chat), width)
	lastPreview := strings.TrimSpace(substituteMentions(chat.LastMessagePreview, m.chatMentionNames))
	if lastPreview == "" {
		lastPreview = "No messages yet"
	}
	fixedLines := 12
	if !chat.LastMessageAt.IsZero() {
		fixedLines++
	}
	if chat.LastSenderName != "" {
		fixedLines += 2
	}
	previewLines := min(6, max(1, height-fixedLines))
	preview := limitTextLines(wrapText(lastPreview, width), previewLines)
	lines := []string{
		titleStyle.Render(title),
		"",
		smallCap("Detail", width),
		metaRow("Type", chatType(chat.IsGroup), width),
		metaRow("Unread", fmt.Sprintf("%d", chat.UnreadCount), width),
	}
	if !chat.LastMessageAt.IsZero() {
		lines = append(lines, metaRow("Last", chat.LastMessageAt.Local().Format("Jan 2 15:04"), width))
	}
	lines = append(lines,
		"",
		smallCap("Latest preview", width),
		bodyStyle.Render(preview),
	)
	if chat.LastSenderName != "" {
		lines = append(lines, "", subtleStyle.Render(wrapText("— "+displaySenderLabel(chat.LastSenderName), width)))
	}
	lines = append(lines,
		"",
		smallCap("Actions", width),
		actionRow("↵", "open thread", width),
		actionRow("/", "filter inbox", width),
		actionRow("r", "reload cache", width),
	)
	return limitTextLines(strings.Join(lines, "\n"), height)
}

func smallCap(label string, width int) string {
	// Keep a right-side margin so styled Unicode rule rows never sit flush with
	// the panel edge. Lipgloss can wrap near-boundary styled runs, which leaves
	// stray vertical border fragments in no-alt-screen terminals.
	rule := strings.Repeat("─", max(0, width-lipgloss.Width(label)-smallCapRuleMargin))
	return sectionStyle.Render(label) + "  " + hairlineStyle.Render(rule)
}

func metaRow(label, value string, width int) string {
	limit := max(4, width-2-lipgloss.Width(label)-2)
	value = truncateText(value, limit)
	return subtleStyle.Render(label) + "  " + slateStyle.Render(value)
}

func actionRow(key, label string, width int) string {
	chip := subtleStyle.Render("[") + chipKeyStyle.Render(key) + subtleStyle.Render("]")
	rest := chipLabelStyle.Render(label)
	line := chip + " " + rest
	if lipgloss.Width(line) > width {
		return truncateText(line, width)
	}
	return line
}

// threadMessageBlocks flattens every cached message into display lines —
// blank separators between messages, date dividers at day boundaries — and
// reports the exclusive end line index of each message's block. The thread
// scroll offset is measured in these lines, so scrolling is smooth and long
// messages are fully reachable; the block ends let scroll math account for
// separators exactly.
func (m Model) threadMessageBlocks(width int) ([]string, []int) {
	width = max(18, width)
	now := time.Now()
	lines := make([]string, 0, len(m.messages)*3)
	ends := make([]int, len(m.messages))
	var previousDay time.Time
	for i, msg := range m.messages {
		if hiddenLegacyMessage(msg) {
			ends[i] = len(lines)
			continue
		}
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		if day := msg.Timestamp.Local(); i == 0 || !sameDay(day, previousDay) {
			lines = append(lines, dateSeparator(day, now, width), "")
			previousDay = day
		}
		selected := m.selecting && i == m.selectIndex
		lines = append(lines, strings.Split(renderThreadMessage(msg, width, m.mentionNames, selected), "\n")...)
		ends[i] = len(lines)
	}
	return lines, ends
}

func (m Model) threadMessageLines(width int) []string {
	lines, _ := m.threadMessageBlocks(width)
	return lines
}

// hiddenLegacyMessage reports message rows an older build stored for
// incoming reactions. They carry no usable content; hiding them at render
// keeps the store's never-delete-messages invariant intact.
func hiddenLegacyMessage(msg domain.Message) bool {
	return msg.Text == "[reaction]" && msg.MediaKind == domain.MediaKindNone
}

// threadBody renders the visible window of the message log: the viewport is
// anchored to the bottom (newest lines) and threadScroll lifts it up by
// whole lines.
func (m Model) threadBody(contentHeight, width int) string {
	if len(m.messages) == 0 {
		if m.threadHistoryPending {
			return slateStyle.Render("Requesting messages for this chat from your phone…")
		}
		return slateStyle.Render("No cached messages for this chat yet.")
	}
	lines := m.threadMessageLines(width)
	contentHeight = max(1, contentHeight)
	scroll := min(max(0, m.threadScroll), max(0, len(lines)-contentHeight))
	end := len(lines) - scroll
	start := max(0, end-contentHeight)
	return strings.Join(lines[start:end], "\n")
}

// mediaPickerBody renders the media picker overlay's content: a header and a
// windowed list of media items, one row each, with the selection highlighted.
// The window mirrors renderFilePicker: it keeps the selected row visible.
func (m Model) mediaPickerBody(contentHeight, width int) string {
	width = max(18, width)
	items := m.mediaMessages()
	lines := []string{smallCap(fmt.Sprintf("Media · %d items", len(items)), width)}
	if len(items) == 0 {
		lines = append(lines, subtleStyle.Render("  (no media)"))
		return strings.Join(lines, "\n")
	}
	selected := clampSelection(m.mediaPickerIndex, len(items))
	limit := max(1, contentHeight-2)
	if limit > len(items) {
		limit = len(items)
	}
	start := 0
	if selected >= limit {
		start = selected - limit + 1
	}
	end := min(len(items), start+limit)
	for idx := start; idx < end; idx++ {
		lines = append(lines, mediaPickerRow(items[idx], idx == selected, width))
	}
	return strings.Join(lines, "\n")
}

// mediaPickerRow renders one media picker entry: a selection rail, timestamp,
// kind, optional filename, size, duration, and a saved marker.
func mediaPickerRow(msg domain.Message, selected bool, width int) string {
	rail := "  "
	if selected {
		rail = railStyle.Render("▌ ")
	}
	parts := []string{
		timestampStyle.Render(msg.Timestamp.Local().Format("15:04 Jan 2")),
		chipLabelStyle.Render(string(msg.MediaKind)),
	}
	if msg.MediaFileName != "" {
		parts = append(parts, chipLabelStyle.Render(msg.MediaFileName))
	}
	if msg.MediaFileLength > 0 {
		parts = append(parts, chipLabelStyle.Render(humanFileSize(msg.MediaFileLength)))
	}
	if msg.MediaSeconds > 0 {
		parts = append(parts, chipLabelStyle.Render(fmt.Sprintf("%d:%02d", msg.MediaSeconds/60, msg.MediaSeconds%60)))
	}
	if msg.DownloadedPath != "" {
		parts = append(parts, chipKeyStyle.Render("✓"))
	}
	return rail + truncateText(strings.Join(parts, " "), max(12, width-2))
}

func (m Model) composerBody(width int) string {
	if m.composing {
		body := []string{m.renderComposeToolbar(width)}
		if m.filePickerOpen {
			body = append(body, m.renderFilePicker(width))
		}
		body = append(body, m.composer.View())
		if suggestions := m.renderPathSuggestions(width); suggestions != "" {
			body = append(body, suggestions)
		}
		return strings.Join(body, "\n")
	}
	parts := []string{
		subtleStyle.Render("[") + chipKeyStyle.Render("i") + subtleStyle.Render("]") + " " + chipLabelStyle.Render("compose"),
		subtleStyle.Render("[") + chipKeyStyle.Render("ctrl+o") + subtleStyle.Render("]") + " " + chipLabelStyle.Render("files"),
		subtleStyle.Render("[") + chipKeyStyle.Render("ctrl+v") + subtleStyle.Render("]") + " " + chipLabelStyle.Render("screenshot"),
		subtleStyle.Render("[") + chipKeyStyle.Render("alt+v") + subtleStyle.Render("]") + " " + chipLabelStyle.Render("voice"),
	}
	return strings.Join(parts, subtleStyle.Render("   "))
}

func (m *Model) resizeComposer(width, maxHeight int) {
	if width <= 0 {
		return
	}
	m.composer.SetWidth(max(12, width))
	inputWidth := max(8, width-len([]rune(m.composer.Prompt)))
	targetHeight := clampComposerHeight(composerHeightForDraft(m.composer.Value(), inputWidth), maxHeight)
	m.composer.SetHeight(targetHeight)
}

func (m *Model) nextImagePlaceholder() string {
	m.nextImageID++
	return fmt.Sprintf("[Image #%d]", m.nextImageID)
}

func (m *Model) nextVoicePlaceholder() string {
	m.nextVoiceID++
	return fmt.Sprintf("[Voice #%d]", m.nextVoiceID)
}

func (m *Model) clearPendingAttachments() {
	for _, attachment := range m.pendingAttachments {
		if attachment.temp && attachment.path != "" {
			_ = os.Remove(attachment.path)
		}
	}
	m.pendingAttachments = nil
	m.nextImageID = 0
	m.nextVoiceID = 0
}

func (m Model) renderComposeToolbar(width int) string {
	attach := toolbarButtonStyle.Render(" + ")
	voiceLabel := " ◉ mic "
	voiceStyle := toolbarButtonStyle
	if m.recordingVoice || m.stoppingVoice {
		voiceLabel = " ● rec "
		voiceStyle = toolbarActiveButtonStyle
	}
	voice := voiceStyle.Render(voiceLabel)
	hint := "↵ queue"
	if m.filePickerOpen {
		hint = "j/k move · ↵ select · h up · esc close"
	} else if m.recordingVoice {
		hint = "recording voice note…"
	}
	hintRendered := slateStyle.Render(truncateText(hint, max(10, width-lipgloss.Width(attach)-lipgloss.Width(voice)-4)))
	line := lipgloss.JoinHorizontal(lipgloss.Left, attach, " ", voice, "  ", hintRendered)
	return lipgloss.NewStyle().MaxWidth(max(12, width)).Render(line)
}

func (m Model) renderFilePicker(width int) string {
	width = max(18, width)
	lines := []string{
		smallCap("Files · "+truncateText(m.filePickerDir, max(8, width-10)), width),
	}
	if len(m.filePickerEntries) == 0 {
		lines = append(lines, subtleStyle.Render("  (empty)"))
		return strings.Join(lines, "\n")
	}
	limit := min(5, len(m.filePickerEntries))
	start := 0
	if m.filePickerSelected >= limit {
		start = m.filePickerSelected - limit + 1
	}
	end := min(len(m.filePickerEntries), start+limit)
	for idx := start; idx < end; idx++ {
		entry := m.filePickerEntries[idx]
		label := entry.name
		if entry.isDir {
			label += string(os.PathSeparator)
		}
		label = truncateText(label, width-2)
		if idx == m.filePickerSelected {
			lines = append(lines, railStyle.Render("▌")+" "+selectedItemStyle.Render(label))
		} else {
			lines = append(lines, "  "+mutedStyle.Render(label))
		}
	}
	return strings.Join(lines, "\n")
}

func (m *Model) openFilePicker() {
	m.filePickerOpen = true
	if strings.TrimSpace(m.filePickerDir) == "" {
		m.filePickerDir = defaultPickerDir()
	}
	m.refreshFilePicker()
}

func (m *Model) openParentPickerDir() {
	if !m.filePickerOpen {
		return
	}
	parent := filepath.Dir(m.filePickerDir)
	if parent == "." || parent == "" {
		parent = string(os.PathSeparator)
	}
	m.filePickerDir = parent
	m.refreshFilePicker()
}

func (m *Model) refreshFilePicker() {
	entries, err := listFilePickerEntries(m.filePickerDir)
	if err != nil {
		m.lastErr = err.Error()
		return
	}
	m.filePickerEntries = entries
	m.filePickerSelected = clampSelection(m.filePickerSelected, len(entries))
}

func (m Model) pickSelectedFileEntry() (tea.Model, tea.Cmd) {
	if len(m.filePickerEntries) == 0 {
		return m, nil
	}
	entry := m.filePickerEntries[m.filePickerSelected]
	if entry.isDir {
		m.filePickerDir = entry.path
		m.filePickerSelected = 0
		m.refreshFilePicker()
		return m, nil
	}
	kind := media.KindForPath(entry.path)
	token := media.AttachmentToken(filepath.Base(entry.path), kind)
	msg := attachmentStagedMsg{
		attachment: stagedAttachment{
			token: token,
			path:  entry.path,
			kind:  kind,
		},
		status: "Attachment added to draft",
	}
	m.filePickerOpen = false
	return m.Update(msg)
}

func (m Model) toggleVoiceRecording() (tea.Model, tea.Cmd) {
	if m.stoppingVoice {
		return m, nil
	}
	if m.recordingVoice {
		if time.Since(m.recordingSince) < 600*time.Millisecond {
			return m, nil
		}
		m.stoppingVoice = true
		m.recordingVoice = false
		return m, stopVoiceRecordingCmd(m.recorder, m.nextVoicePlaceholder())
	}
	if m.recorder == nil {
		m.lastErr = "voice recorder is unavailable"
		return m, nil
	}
	if err := m.recorder.Start(); err != nil {
		m.lastErr = err.Error()
		return m, nil
	}
	m.recordingVoice = true
	m.recordingSince = time.Now()
	m.lastErr = ""
	return m, nil
}

func (m *Model) clearPathSuggestions() {
	m.pathSuggestions = nil
	m.pathSuggestionIdx = 0
	m.pathSuggestionFocus = false
}

func (m *Model) refreshPathSuggestions() {
	m.suggestionsKind = "Paths"
	suggestions := filePathSuggestions(m.composer.Value(), maxPathSuggestions)
	if len(suggestions) == 0 {
		if suggestions = emojiSuggestions(m.composer.Value(), maxPathSuggestions); len(suggestions) > 0 {
			m.suggestionsKind = "Emoji"
		}
	}
	if len(suggestions) == 0 {
		if suggestions = m.mentionSuggestions(maxPathSuggestions); len(suggestions) > 0 {
			m.suggestionsKind = "Mentions"
		}
	}
	m.pathSuggestions = suggestions
	if len(m.pathSuggestions) == 0 {
		m.pathSuggestionIdx = 0
		m.pathSuggestionFocus = false
		return
	}
	m.pathSuggestionIdx = clampSelection(m.pathSuggestionIdx, len(m.pathSuggestions))
}

func (m *Model) applySelectedPathSuggestion() bool {
	if len(m.pathSuggestions) == 0 {
		return false
	}
	suggestion := m.pathSuggestions[m.pathSuggestionIdx]
	m.composer.SetValue(suggestion.replacement)
	if suggestion.mentionJID != "" {
		if m.draftMentions == nil {
			m.draftMentions = make(map[string]string)
		}
		m.draftMentions[suggestion.mentionName] = suggestion.mentionJID
	}
	m.refreshPathSuggestions()
	return true
}

// mentionSuggestions offers people to tag while the draft ends in an
// unfinished "@prefix" token. Candidates come from the loaded thread's
// senders, newest first, so group members who spoke recently rank higher.
func (m Model) mentionSuggestions(limit int) []pathSuggestion {
	draft := m.composer.Value()
	match := trailingMentionPrefix.FindStringSubmatchIndex(draft)
	if match == nil {
		return nil
	}
	head := draft[:match[3]]
	prefix := strings.ToLower(draft[match[4]:match[5]])
	seen := make(map[string]bool)
	var suggestions []pathSuggestion
	for i := len(m.messages) - 1; i >= 0; i-- {
		msg := m.messages[i]
		if msg.FromMe || msg.SenderJID == "" {
			continue
		}
		name := collapseWhitespace(msg.SenderName)
		// Skip masked placeholder names; tagging needs a real name to show.
		if name == "" || displaySenderLabel(name) != name {
			continue
		}
		if seen[name] || !strings.HasPrefix(strings.ToLower(name), prefix) {
			continue
		}
		seen[name] = true
		suggestions = append(suggestions, pathSuggestion{
			label:       "@" + name,
			replacement: head + "@" + name + " ",
			mentionName: name,
			mentionJID:  msg.SenderJID,
		})
		if len(suggestions) >= limit {
			break
		}
	}
	return suggestions
}

func (m Model) renderPathSuggestions(width int) string {
	if len(m.pathSuggestions) == 0 {
		return ""
	}
	var heading string
	switch {
	case m.pathSuggestionFocus:
		heading = m.suggestionsKind + " · j/k move · enter/tab apply · esc return"
	default:
		heading = m.suggestionsKind + " · tab focus list · keep typing to refine"
	}
	lines := []string{smallCap(heading, width)}
	for idx, suggestion := range m.pathSuggestions {
		label := truncateText(suggestion.label, max(12, width-2))
		if suggestion.isDir {
			label += string(os.PathSeparator)
		}
		if idx == m.pathSuggestionIdx {
			lines = append(lines, railStyle.Render("▌")+" "+selectedItemStyle.Render(label))
		} else {
			lines = append(lines, "  "+mutedStyle.Render(label))
		}
	}
	return strings.Join(lines, "\n")
}

// trailingMentionPrefix matches an unfinished "@name" token at the end of
// the draft — the trigger for mention suggestions. Names may contain spaces
// ("@Pranjal Ag…"), but never another "@".
var trailingMentionPrefix = regexp.MustCompile(`(^|\s)@([A-Za-z][\w .-]{0,24})$`)

// applyDraftMentions converts the "@Name" tags picked from the mention list
// into WhatsApp's "@<number>" wire format and returns the tagged JIDs for
// the message's context info. Tags the user deleted before sending are
// simply not present and drop out.
func applyDraftMentions(text string, mentions map[string]string) (string, []string) {
	if len(mentions) == 0 {
		return text, nil
	}
	names := make([]string, 0, len(mentions))
	for name := range mentions {
		names = append(names, name)
	}
	// Longest first so "@Pranjal Agarwal" is not clobbered by "@Pranjal".
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	var jids []string
	for _, name := range names {
		tag := "@" + name
		if !strings.Contains(text, tag) {
			continue
		}
		user := jidUserPart(mentions[name])
		if user == "" {
			continue
		}
		text = strings.ReplaceAll(text, tag, "@"+user)
		jids = append(jids, mentions[name])
	}
	return text, jids
}

func appendDraftToken(existing, token string) string {
	existing = strings.TrimSpace(existing)
	if existing == "" {
		return token
	}
	return existing + " " + token
}

func stripAttachmentTokens(draft string, attachments []stagedAttachment) string {
	cleaned := draft
	for _, attachment := range attachments {
		if attachment.token != "" {
			cleaned = strings.ReplaceAll(cleaned, attachment.token, " ")
		}
	}
	return strings.Join(strings.Fields(cleaned), " ")
}

func (m Model) splitWidths(totalWidth int) (int, int) {
	left := totalWidth / 3
	if left < 34 {
		left = 34
	}
	right := totalWidth - left - 1
	if right < 42 {
		right = 42
		left = totalWidth - right - 1
	}
	if left < 34 {
		left = 34
		right = totalWidth - left - 1
	}
	return left, right
}

// latestVoiceMessage finds the newest voice note or audio message that is
// already downloaded or still downloadable.
func latestVoiceMessage(messages []domain.Message) *domain.Message {
	for idx := len(messages) - 1; idx >= 0; idx-- {
		msg := messages[idx]
		if msg.MediaKind != domain.MediaKindVoice && msg.MediaKind != domain.MediaKindAudio {
			continue
		}
		if msg.DownloadedPath != "" || msg.MediaDirectPath != "" {
			return &msg
		}
	}
	return nil
}

// playVoiceCmd downloads the message's audio when it is not cached yet and
// starts playback in the background.
func playVoiceCmd(transport domain.Transport, player audioPlayer, msg domain.Message, downloadDir string) tea.Cmd {
	return func() tea.Msg {
		path := msg.DownloadedPath
		downloaded := false
		if path == "" {
			target, err := transport.DownloadMedia(context.Background(), msg, downloadDir)
			if err != nil {
				return opResultMsg{err: err}
			}
			path = target
			downloaded = true
		}
		if err := player.Play(path); err != nil {
			return opResultMsg{err: err}
		}
		label := media.StatusLabel(msg.MediaKind)
		if msg.MediaSeconds > 0 {
			label = fmt.Sprintf("%s (%d:%02d)", label, msg.MediaSeconds/60, msg.MediaSeconds%60)
		}
		return opResultMsg{
			status:  "Playing " + label + " — p to stop",
			chatJID: msg.ChatJID,
			refresh: downloaded,
		}
	}
}

func latestDownloadableMessage(messages []domain.Message) *domain.Message {
	for idx := len(messages) - 1; idx >= 0; idx-- {
		if messages[idx].MediaKind != domain.MediaKindNone && messages[idx].MediaDirectPath != "" {
			msg := messages[idx]
			return &msg
		}
	}
	return nil
}

func defaultDownloadDir() string {
	return filepath.Join(os.TempDir(), "whatsapp-terminal-downloads")
}

func defaultPickerDir() string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		return home
	}
	return "."
}

func listFilePickerEntries(dir string) ([]filePickerEntry, error) {
	if strings.TrimSpace(dir) == "" {
		dir = defaultPickerDir()
	}
	items, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read files in %s: %w", dir, err)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].IsDir() != items[j].IsDir() {
			return items[i].IsDir()
		}
		return strings.ToLower(items[i].Name()) < strings.ToLower(items[j].Name())
	})
	entries := make([]filePickerEntry, 0, len(items))
	for _, item := range items {
		entries = append(entries, filePickerEntry{
			name:  item.Name(),
			path:  filepath.Join(dir, item.Name()),
			isDir: item.IsDir(),
		})
	}
	return entries, nil
}

func filePathSuggestions(raw string, limit int) []pathSuggestion {
	query, ok := parsePathSuggestionQuery(raw)
	if !ok {
		return nil
	}
	suggestions, err := buildPathSuggestions(query, limit)
	if err != nil {
		return nil
	}
	return suggestions
}

func (m Model) threadToolbarHit(x, y int) string {
	layout := m.threadLayout()
	// The toolbar is the first content row inside the composer panel: one
	// past the panel's top border.
	toolbarY := layout.headerLines + layout.messageHeight + 1
	contentX := boxMutedStyle.GetPaddingLeft() + 1
	if y != toolbarY || x < contentX {
		return ""
	}
	relativeX := x - contentX
	switch {
	case relativeX <= 2:
		return "attach"
	case relativeX >= 4 && relativeX <= 10:
		return "voice"
	default:
		return ""
	}
}

type pathSuggestionQuery struct {
	command string
	typed   string
	caption string
	quoted  bool
}

func parsePathSuggestionQuery(raw string) (pathSuggestionQuery, bool) {
	input := strings.TrimSpace(raw)
	switch {
	case strings.HasPrefix(input, "/media"):
		return parsePathSuggestionRemainder("/media", strings.TrimSpace(strings.TrimPrefix(input, "/media")))
	case strings.HasPrefix(input, "/image"):
		return parsePathSuggestionRemainder("/image", strings.TrimSpace(strings.TrimPrefix(input, "/image")))
	default:
		return pathSuggestionQuery{}, false
	}
}

func parsePathSuggestionRemainder(command, remainder string) (pathSuggestionQuery, bool) {
	query := pathSuggestionQuery{command: command}
	parts := strings.SplitN(remainder, "::", 2)
	pathPart := strings.TrimSpace(parts[0])
	if len(parts) == 2 {
		query.caption = strings.TrimSpace(parts[1])
	}
	if strings.HasPrefix(pathPart, "\"") {
		query.quoted = true
		pathPart = strings.TrimPrefix(pathPart, "\"")
		if idx := strings.Index(pathPart, "\""); idx >= 0 {
			pathPart = pathPart[:idx]
		}
	}
	query.typed = pathPart
	return query, true
}

func buildPathSuggestions(query pathSuggestionQuery, limit int) ([]pathSuggestion, error) {
	basePath := expandUserPath(query.typed)
	lookupDir := basePath
	prefix := ""
	if basePath == "" {
		lookupDir = "."
	} else if !strings.HasSuffix(basePath, string(os.PathSeparator)) {
		lookupDir = filepath.Dir(basePath)
		prefix = filepath.Base(basePath)
	}
	if strings.TrimSpace(lookupDir) == "" {
		lookupDir = "."
	}

	entries, err := os.ReadDir(lookupDir)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})

	results := make([]pathSuggestion, 0, limit)
	for _, entry := range entries {
		if prefix != "" && !strings.HasPrefix(strings.ToLower(entry.Name()), strings.ToLower(prefix)) {
			continue
		}
		replacementPath := composeSuggestedPath(query.typed, lookupDir, entry.Name(), entry.IsDir())
		results = append(results, pathSuggestion{
			label:       entry.Name(),
			replacement: composeSuggestionValue(query, replacementPath, entry.IsDir()),
			isDir:       entry.IsDir(),
		})
		if len(results) >= limit {
			break
		}
	}
	return results, nil
}

func expandUserPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil && home != "" {
			if path == "~" {
				return home
			}
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}

func composeSuggestedPath(typed, lookupDir, entry string, isDir bool) string {
	var suggestion string
	switch {
	case typed == "":
		suggestion = entry
	case strings.HasPrefix(typed, "~/"):
		home, _ := os.UserHomeDir()
		full := filepath.Join(lookupDir, entry)
		if home != "" {
			rel, err := filepath.Rel(home, full)
			if err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
				suggestion = "~/" + rel
			}
		}
		if suggestion == "" {
			suggestion = full
		}
	case filepath.IsAbs(typed):
		suggestion = filepath.Join(lookupDir, entry)
	default:
		full := filepath.Join(lookupDir, entry)
		if rel, err := filepath.Rel(".", full); err == nil {
			suggestion = rel
		} else {
			suggestion = full
		}
	}
	if isDir && !strings.HasSuffix(suggestion, string(os.PathSeparator)) {
		suggestion += string(os.PathSeparator)
	}
	return suggestion
}

func composeSuggestionValue(query pathSuggestionQuery, path string, isDir bool) string {
	renderedPath := path
	if query.quoted || strings.ContainsRune(path, ' ') {
		renderedPath = fmt.Sprintf("\"%s", path)
		if !isDir {
			renderedPath += "\""
		}
	}
	result := fmt.Sprintf("%s %s", query.command, renderedPath)
	if query.caption != "" {
		result += " :: " + query.caption
	}
	return result
}

func chatType(isGroup bool) string {
	if isGroup {
		return "group"
	}
	return "direct"
}

// renderPanel renders content inside a fixed-size box. The box dimensions
// come from the window layout alone — content can never grow or shrink it.
// Every content line is hard-clipped to the panel's inner width (so lipgloss
// never re-wraps anything) and the line count is clamped and padded to the
// inner height.
func renderPanel(style lipgloss.Style, totalWidth, totalHeight int, content string) string {
	totalWidth = max(style.GetHorizontalFrameSize()+1, totalWidth)
	totalHeight = max(style.GetVerticalFrameSize()+1, totalHeight)
	innerWidth := totalWidth - style.GetHorizontalFrameSize()
	innerHeight := totalHeight - style.GetVerticalFrameSize()

	lines := strings.Split(content, "\n")
	if len(lines) > innerHeight {
		lines = lines[:innerHeight]
	}
	for i, line := range lines {
		if ansi.StringWidth(line) > innerWidth {
			lines[i] = ansi.Truncate(line, innerWidth, "")
		}
	}
	for len(lines) < innerHeight {
		lines = append(lines, "")
	}
	// Width excludes the border in lipgloss, so totalWidth-border yields a
	// text area of exactly innerWidth and a rendered block of totalWidth.
	return style.Width(totalWidth - style.GetHorizontalBorderSize()).Render(strings.Join(lines, "\n"))
}

func limitTextLines(text string, maxLines int) string {
	if maxLines <= 0 || text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) <= maxLines {
		return text
	}
	return strings.Join(lines[:maxLines], "\n")
}

func (m Model) fitFrame(view string) string {
	if m.width <= 0 || m.height <= 0 || view == "" {
		return view
	}
	// Leave the final terminal column untouched. Any line that reaches the final
	// column can trigger autowrap before Bubble Tea appends its clear-to-EOL
	// sequence, which desynchronizes no-alt-screen redraws while scrolling.
	targetWidth := max(1, m.width-1)
	lines := strings.Split(view, "\n")
	if len(lines) > m.height {
		lines = lines[:m.height]
	}
	for len(lines) < m.height {
		lines = append(lines, "")
	}
	for idx, line := range lines {
		lines[idx] = ansi.Truncate(line, targetWidth, "")
		if m.forceRepaint {
			lines[idx] += repaintMarker(m.frameNonce)
		}
	}
	return strings.Join(lines, "\n")
}

func repaintMarker(n int) string {
	if n%2 == 0 {
		return "\x1b[0m"
	}
	return "\x1b[00m"
}

// truncateText shortens text to the given display width, appending an
// ellipsis when anything was cut. It measures visual cells (not runes) so
// wide characters and emoji cannot overflow panels, and it is safe on
// styled input: ANSI escape sequences are preserved, never split.
func truncateText(text string, width int) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(text) <= width {
		return text
	}
	if width == 1 {
		return "…"
	}
	return ansi.Truncate(text, width, "…")
}

// truncateTextHead is truncateText's mirror image: it keeps the tail of the
// text and elides the head. Intended for plain (unstyled) strings such as the
// live search query.
func truncateTextHead(text string, width int) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(text) <= width {
		return text
	}
	if width == 1 {
		return "…"
	}
	runes := []rune(text)
	used := 0
	start := len(runes)
	for i := len(runes) - 1; i >= 0; i-- {
		w := ansi.StringWidth(string(runes[i]))
		if used+w > width-1 {
			break
		}
		used += w
		start = i
	}
	return "…" + string(runes[start:])
}

func wrapText(text string, width int) string {
	text = strings.TrimSpace(text)
	if text == "" || width <= 0 {
		return ""
	}
	var lines []string
	for _, paragraph := range strings.Split(text, "\n") {
		paragraph = strings.TrimSpace(paragraph)
		if paragraph == "" {
			lines = append(lines, "")
			continue
		}
		var current string
		for _, word := range strings.Fields(paragraph) {
			switch {
			case ansi.StringWidth(word) > width:
				// A single word wider than the panel (URLs, hashes) is
				// clipped — it must never leak through and re-wrap.
				if current != "" {
					lines = append(lines, current)
					current = ""
				}
				lines = append(lines, truncateText(word, width))
			case current == "":
				current = word
			case ansi.StringWidth(current)+1+ansi.StringWidth(word) <= width:
				current += " " + word
			default:
				lines = append(lines, current)
				current = word
			}
		}
		if current != "" {
			lines = append(lines, current)
		}
	}
	return strings.Join(lines, "\n")
}

func countRenderedLines(text string) int {
	if text == "" {
		return 0
	}
	return len(strings.Split(text, "\n"))
}

// composerHeightForDraft returns how many rows the textarea needs to show
// the draft and its cursor: the wrapped line count, plus one extra row when
// the last visual row is exactly full (the cursor then sits on the next row).
// An empty draft needs a single row — one prompt arrow, not three.
func composerHeightForDraft(text string, width int) int {
	if width <= 0 || text == "" {
		return 1
	}
	lines := strings.Split(text, "\n")
	total := 0
	for _, line := range lines {
		cells := ansi.StringWidth(line)
		if cells == 0 {
			total++
			continue
		}
		total += (cells-1)/width + 1
	}
	if last := ansi.StringWidth(lines[len(lines)-1]); last > 0 && last%width == 0 {
		total++
	}
	return max(1, total)
}

func clampComposerHeight(lines, maxHeight int) int {
	if maxHeight < 1 {
		maxHeight = 1
	}
	if lines < 1 {
		return 1
	}
	if lines > maxHeight {
		return maxHeight
	}
	return lines
}

// paddedContentHeight converts a panel's total height into its content-line
// budget. renderPanel hard-clips content, so the full inner height is usable.
func paddedContentHeight(totalHeight int) int {
	return max(1, totalHeight-boxStyle.GetVerticalFrameSize())
}

func parseComposeAction(raw string) (composeAction, error) {
	input := strings.TrimSpace(raw)
	if input == "" {
		return composeAction{}, fmt.Errorf("message cannot be empty")
	}
	if !strings.HasPrefix(input, "/image") && !strings.HasPrefix(input, "/media") {
		return composeAction{kind: composeActionText, text: input}, nil
	}
	kind := composeActionImage
	rest := strings.TrimSpace(strings.TrimPrefix(input, "/image"))
	if strings.HasPrefix(input, "/media") {
		kind = composeActionMedia
		rest = strings.TrimSpace(strings.TrimPrefix(input, "/media"))
	}
	if rest == "" {
		return composeAction{}, fmt.Errorf("usage: /media <path> :: optional caption")
	}
	path, caption, err := parseImageCommand(rest)
	if err != nil {
		return composeAction{}, err
	}
	return composeAction{kind: kind, path: path, caption: caption}, nil
}

func parseImageCommand(input string) (string, string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", "", fmt.Errorf("usage: /image <path> :: optional caption")
	}
	if strings.HasPrefix(input, "\"") {
		end := strings.Index(input[1:], "\"")
		if end < 0 {
			return "", "", fmt.Errorf("image path is missing a closing quote")
		}
		path := input[1 : end+1]
		remainder := strings.TrimSpace(input[end+2:])
		return path, strings.TrimSpace(strings.TrimPrefix(remainder, "::")), nil
	}
	parts := strings.SplitN(input, "::", 2)
	path := strings.TrimSpace(parts[0])
	caption := ""
	if len(parts) == 2 {
		caption = strings.TrimSpace(parts[1])
	}
	if path == "" {
		return "", "", fmt.Errorf("image path cannot be empty")
	}
	return path, caption, nil
}

// Theme-driven styles. Assigned by applyTheme; see internal/ui/theme.go.
var (
	currentTheme Theme

	titleStyle               lipgloss.Style
	brandStyle               lipgloss.Style
	mutedStyle               lipgloss.Style
	slateStyle               lipgloss.Style
	subtleStyle              lipgloss.Style
	hairlineStyle            lipgloss.Style
	errorStyle               lipgloss.Style
	selectedItemStyle        lipgloss.Style
	railStyle                lipgloss.Style
	sectionStyle             lipgloss.Style
	bodyStyle                lipgloss.Style
	peerNameStyle            lipgloss.Style
	youNameStyle             lipgloss.Style
	memberNameStyle          lipgloss.Style
	mentionStyle             lipgloss.Style
	monoStyle                lipgloss.Style
	senderPalette            []lipgloss.Style
	timestampStyle           lipgloss.Style
	receiptReadStyle         lipgloss.Style
	receiptDeliveredStyle    lipgloss.Style
	receiptSentStyle         lipgloss.Style
	unreadPillStyle          lipgloss.Style
	chipKeyStyle             lipgloss.Style
	chipLabelStyle           lipgloss.Style
	statusDotOnStyle         lipgloss.Style
	statusDotWarnStyle       lipgloss.Style
	statusDotErrStyle        lipgloss.Style
	toolbarButtonStyle       lipgloss.Style
	toolbarActiveButtonStyle lipgloss.Style
	boxStyle                 lipgloss.Style
	boxMutedStyle            lipgloss.Style
	qrBoxStyle               lipgloss.Style

	// Layout-only styles (theme-independent).
	headerStyle = lipgloss.NewStyle().Padding(0, 1)
	footerStyle = lipgloss.NewStyle().Padding(0, 1)
)

func init() {
	applyTheme(DefaultTheme())
}
