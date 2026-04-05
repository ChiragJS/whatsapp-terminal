package ui

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	qrterminal "github.com/mdp/qrterminal/v3"

	"github.com/chirag/whatsapp-terminal/internal/domain"
	appstore "github.com/chirag/whatsapp-terminal/internal/store"
)

const (
	chatListLimit        = 200
	defaultChatListLimit = 5
	messageLimit         = 200
	maxPathSuggestions   = 5
)

type viewMode int

const (
	viewChats viewMode = iota
	viewThread
)

type transportEventMsg struct {
	event domain.Event
}

type chatsLoadedMsg struct {
	chats []domain.ChatSummary
	err   error
}

type messagesLoadedMsg struct {
	chatJID  string
	messages []domain.Message
	err      error
}

type opResultMsg struct {
	err              error
	status           string
	chatJID          string
	refresh          bool
	clearComposer    bool
	clearAttachments bool
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
}

type filePickerEntry struct {
	name  string
	path  string
	isDir bool
}

type Model struct {
	repo       *appstore.Store
	transport  domain.Transport
	events     <-chan domain.Event
	fullRedraw bool

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
	selected       int
	currentChatID  string
	nextImageID    int
	nextVoiceID    int
	downloadDir    string
	recordingSince time.Time

	search    textinput.Model
	composer  textinput.Model
	clipboard clipboardReader
	sounder   sounder
	recorder  voiceRecorder

	chats                []domain.ChatSummary
	messages             []domain.Message
	pendingAttachments   []stagedAttachment
	pathSuggestions      []pathSuggestion
	pathSuggestionIdx    int
	pathSuggestionFocus  bool
	filePickerDir        string
	filePickerEntries    []filePickerEntry
	filePickerSelected   int
	threadHistoryPending bool
}

func NewModel(repo *appstore.Store, transport domain.Transport) Model {
	return NewModelWithOptions(repo, transport, newSystemClipboard(), newTerminalBell(), false)
}

func NewModelWithClipboard(repo *appstore.Store, transport domain.Transport, clipboard clipboardReader) Model {
	return NewModelWithOptions(repo, transport, clipboard, newTerminalBell(), false)
}

func NewModelWithOptions(repo *appstore.Store, transport domain.Transport, clipboard clipboardReader, sounder sounder, fullRedraw bool) Model {
	return NewModelWithRuntimeOptions(repo, transport, clipboard, sounder, newSystemVoiceRecorder(), defaultDownloadDir(), fullRedraw)
}

func NewModelWithRuntimeOptions(repo *appstore.Store, transport domain.Transport, clipboard clipboardReader, sounder sounder, recorder voiceRecorder, downloadDir string, fullRedraw bool) Model {
	if clipboard == nil {
		clipboard = newSystemClipboard()
	}
	if sounder == nil {
		sounder = newTerminalBell()
	}
	if recorder == nil {
		recorder = newSystemVoiceRecorder()
	}
	if strings.TrimSpace(downloadDir) == "" {
		downloadDir = defaultDownloadDir()
	}
	pickerDir := defaultPickerDir()
	search := textinput.New()
	search.Placeholder = "Search chats (/)"
	search.Prompt = "Search: "
	search.CharLimit = 128

	composer := textinput.New()
	composer.Placeholder = "Type a message (i)"
	composer.Prompt = "> "
	composer.CharLimit = 4096

	return Model{
		repo:          repo,
		transport:     transport,
		events:        transport.Events(),
		mode:          viewChats,
		status:        "Starting WhatsApp terminal...",
		search:        search,
		composer:      composer,
		clipboard:     clipboard,
		sounder:       sounder,
		recorder:      recorder,
		downloadDir:   downloadDir,
		filePickerDir: pickerDir,
		fullRedraw:    fullRedraw,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(tea.ClearScreen, waitForTransportEvent(m.events), loadChatsCmd(m.repo, ""))
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		return m, m.redrawCmd()
	case transportEventMsg:
		m = m.applyTransportEvent(msg.event)
		cmds := []tea.Cmd{waitForTransportEvent(m.events), loadChatsCmd(m.repo, m.search.Value())}
		if m.currentChatID != "" {
			cmds = append(cmds, loadMessagesCmd(m.repo, m.currentChatID))
		}
		if msg.event.Notify && m.sounder != nil {
			_ = m.sounder.Bell()
		}
		cmds = append([]tea.Cmd{m.redrawCmd()}, cmds...)
		return m, tea.Batch(cmds...)
	case chatsLoadedMsg:
		if msg.err != nil {
			m.lastErr = msg.err.Error()
			return m, m.redrawCmd()
		}
		if len(msg.chats) > 0 {
			m.syncingRecent = false
		}
		selectedJID := m.selectedChatJID()
		m.chats = msg.chats
		m.selected = clampSelection(m.selected, len(m.chats))
		if selectedJID != "" {
			for idx, chat := range m.chats {
				if chat.JID == selectedJID {
					m.selected = idx
					break
				}
			}
		}
		return m, m.redrawCmd()
	case messagesLoadedMsg:
		if msg.err != nil {
			m.lastErr = msg.err.Error()
			return m, m.redrawCmd()
		}
		if msg.chatJID == m.currentChatID {
			m.messages = msg.messages
			if len(msg.messages) == 0 && m.mode == viewThread && !m.threadHistoryPending {
				m.threadHistoryPending = true
				m.status = "Requesting messages for this chat..."
				return m, tea.Batch(m.redrawCmd(), requestHistoryCmd(m.transport, msg.chatJID, 50))
			}
			if len(msg.messages) > 0 {
				m.threadHistoryPending = false
			}
		}
		return m, m.redrawCmd()
	case opResultMsg:
		if msg.err != nil {
			m.lastErr = msg.err.Error()
		}
		if msg.status != "" {
			m.status = msg.status
		}
		m.stoppingVoice = false
		if msg.err == nil {
			if msg.clearComposer {
				m.composer.SetValue("")
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
				m.redrawCmd(),
				loadChatsCmd(m.repo, m.search.Value()),
				loadMessagesCmd(m.repo, msg.chatJID),
			)
		}
		return m, m.redrawCmd()
	case attachmentStagedMsg:
		if msg.err != nil {
			m.lastErr = msg.err.Error()
			return m, m.redrawCmd()
		}
		m.pendingAttachments = append(m.pendingAttachments, msg.attachment)
		m.composer.SetValue(appendDraftToken(m.composer.Value(), msg.attachment.token))
		if msg.status != "" {
			m.status = msg.status
		}
		m.recordingVoice = false
		m.stoppingVoice = false
		m.refreshPathSuggestions()
		return m, m.redrawCmd()
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			if m.recordingVoice && m.recorder != nil {
				_ = m.recorder.Cancel()
			}
			m.clearPendingAttachments()
			return m, tea.Quit
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

	if m.qrCode != "" {
		return m.renderPairing()
	}

	switch m.mode {
	case viewThread:
		return m.renderThread()
	default:
		return m.renderChatList()
	}
}

func (m Model) updateChatList(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.searching {
			switch msg.String() {
			case "esc":
				m.searching = false
				m.search.Blur()
				m.search.SetValue("")
				return m, tea.Batch(m.redrawCmd(), loadChatsCmd(m.repo, ""))
			case "enter":
				m.searching = false
				m.search.Blur()
				return m, m.redrawCmd()
			}
			var cmd tea.Cmd
			m.search, cmd = m.search.Update(msg)
			return m, tea.Batch(m.redrawCmd(), cmd, loadChatsCmd(m.repo, m.search.Value()))
		}

		switch msg.String() {
		case "/":
			m.searching = true
			m.search.Focus()
			return m, m.redrawCmd()
		case "j", "down":
			m.selected = clampSelection(m.selected+1, len(m.chats))
			return m, nil
		case "k", "up":
			m.selected = clampSelection(m.selected-1, len(m.chats))
			return m, nil
		case "r":
			return m, tea.Batch(m.redrawCmd(), loadChatsCmd(m.repo, m.search.Value()))
		case "enter":
			chat := m.currentChat()
			if chat == nil {
				return m, nil
			}
			m.mode = viewThread
			m.currentChatID = chat.JID
			m.messages = nil
			m.threadHistoryPending = false
			return m, tea.Batch(
				m.redrawCmd(),
				resetUnreadCmd(m.repo, chat.JID),
				loadMessagesCmd(m.repo, chat.JID),
				loadChatsCmd(m.repo, m.search.Value()),
			)
		}
	}
	return m, nil
}

func (m Model) updateThread(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.MouseMsg:
		if !m.composing {
			return m, nil
		}
		if msg.Action != tea.MouseActionRelease || msg.Button != tea.MouseButtonLeft {
			return m, nil
		}
		switch m.threadToolbarHit(msg.X, msg.Y) {
		case "attach":
			m.openFilePicker()
			return m, m.redrawCmd()
		case "voice":
			return m.toggleVoiceRecording()
		}
	case tea.KeyMsg:
		if m.composing {
			if m.filePickerOpen {
				switch msg.String() {
				case "esc":
					m.filePickerOpen = false
					return m, m.redrawCmd()
				case "j", "down":
					m.filePickerSelected = clampSelection(m.filePickerSelected+1, len(m.filePickerEntries))
					return m, m.redrawCmd()
				case "k", "up":
					m.filePickerSelected--
					if m.filePickerSelected < 0 {
						m.filePickerSelected = max(0, len(m.filePickerEntries)-1)
					}
					return m, m.redrawCmd()
				case "h", "backspace":
					m.openParentPickerDir()
					return m, m.redrawCmd()
				case "enter":
					return m.pickSelectedFileEntry()
				}
				return m, nil
			}
			switch msg.String() {
			case "esc":
				if m.pathSuggestionFocus {
					m.pathSuggestionFocus = false
					return m, m.redrawCmd()
				}
				m.composing = false
				m.composer.Blur()
				if m.recordingVoice && m.recorder != nil {
					_ = m.recorder.Cancel()
					m.recordingVoice = false
				}
				m.clearPendingAttachments()
				m.clearPathSuggestions()
				m.filePickerOpen = false
				return m, m.redrawCmd()
			case "tab":
				if len(m.pathSuggestions) > 0 {
					if !m.pathSuggestionFocus {
						m.pathSuggestionFocus = true
						return m, m.redrawCmd()
					}
					if m.applySelectedPathSuggestion() {
						return m, m.redrawCmd()
					}
				}
			case "+", "ctrl+o":
				m.openFilePicker()
				return m, m.redrawCmd()
			case "ctrl+n":
				if len(m.pathSuggestions) > 0 {
					m.pathSuggestionIdx = clampSelection(m.pathSuggestionIdx+1, len(m.pathSuggestions))
					return m, m.redrawCmd()
				}
			case "ctrl+p":
				if len(m.pathSuggestions) > 0 {
					m.pathSuggestionIdx--
					if m.pathSuggestionIdx < 0 {
						m.pathSuggestionIdx = len(m.pathSuggestions) - 1
					}
					return m, m.redrawCmd()
				}
			case "j", "down":
				if m.pathSuggestionFocus && len(m.pathSuggestions) > 0 {
					m.pathSuggestionIdx = clampSelection(m.pathSuggestionIdx+1, len(m.pathSuggestions))
					return m, m.redrawCmd()
				}
			case "k", "up":
				if m.pathSuggestionFocus && len(m.pathSuggestions) > 0 {
					m.pathSuggestionIdx--
					if m.pathSuggestionIdx < 0 {
						m.pathSuggestionIdx = len(m.pathSuggestions) - 1
					}
					return m, m.redrawCmd()
				}
			case "ctrl+v":
				return m, tea.Batch(m.redrawCmd(), stageClipboardImageCmd(m.clipboard, m.nextImagePlaceholder()))
			case "alt+v":
				return m.toggleVoiceRecording()
			case "enter":
				if m.pathSuggestionFocus && len(m.pathSuggestions) > 0 {
					if m.applySelectedPathSuggestion() {
						return m, m.redrawCmd()
					}
				}
				if len(m.pendingAttachments) > 0 {
					chatID := m.currentChatID
					attachments := append([]stagedAttachment(nil), m.pendingAttachments...)
					caption := stripAttachmentTokens(m.composer.Value(), attachments)
					return m, sendStagedAttachmentsCmd(m.transport, chatID, attachments, caption)
				}
				action, err := parseComposeAction(m.composer.Value())
				if err != nil {
					m.lastErr = err.Error()
					return m, nil
				}
				chatID := m.currentChatID
				switch action.kind {
				case composeActionImage:
					return m, sendImageCmd(m.transport, chatID, action.path, action.caption)
				case composeActionMedia:
					return m, sendMediaCmd(m.transport, chatID, action.path, action.caption)
				default:
					return m, sendTextCmd(m.transport, chatID, action.text)
				}
			}
			var cmd tea.Cmd
			m.composer, cmd = m.composer.Update(msg)
			m.refreshPathSuggestions()
			if len(m.pathSuggestions) == 0 {
				m.pathSuggestionFocus = false
			}
			return m, tea.Batch(m.redrawCmd(), cmd)
		}

		switch msg.String() {
		case "esc":
			m.mode = viewChats
			m.currentChatID = ""
			m.messages = nil
			m.threadHistoryPending = false
			m.composing = false
			m.composer.Blur()
			if m.recordingVoice && m.recorder != nil {
				_ = m.recorder.Cancel()
				m.recordingVoice = false
			}
			m.clearPendingAttachments()
			m.clearPathSuggestions()
			return m, tea.Batch(m.redrawCmd(), loadChatsCmd(m.repo, m.search.Value()))
		case "i", "tab":
			m.composing = true
			m.refreshPathSuggestions()
			return m, tea.Batch(m.redrawCmd(), m.composer.Focus())
		case "u":
			if m.currentChatID == "" {
				return m, nil
			}
			return m, requestHistoryCmd(m.transport, m.currentChatID, 50)
		case "d":
			if m.currentChatID == "" {
				return m, nil
			}
			latest := latestDownloadableMessage(m.messages)
			if latest == nil {
				m.lastErr = "no downloadable media found in this thread"
				return m, m.redrawCmd()
			}
			return m, downloadMediaCmd(m.transport, *latest, m.downloadDir)
		}
	}
	return m, nil
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
	header := m.renderHeader("Pair your phone", "Scan the QR code from WhatsApp")
	status := mutedStyle.Render("Open WhatsApp on your phone, choose Linked Devices, then scan the code below.")
	if m.lastErr != "" {
		status += "\n" + errorStyle.Render(m.lastErr)
	}
	qr := qrBoxStyle.Render(m.qrCode)
	content := lipgloss.JoinVertical(
		lipgloss.Center,
		status,
		"",
		qr,
		"",
		mutedStyle.Render("Press q to quit."),
	)
	return lipgloss.JoinVertical(lipgloss.Left, header, lipgloss.Place(m.width, max(10, m.height-4), lipgloss.Center, lipgloss.Top, content))
}

func (m Model) renderChatList() string {
	contentWidth := max(48, m.width-2)
	header := m.renderHeader("Inbox", "")
	footer := m.renderFooter("j/k move  enter open  / search  r refresh  q quit")
	bodyHeight := max(8, m.height-countRenderedLines(header)-countRenderedLines(footer)-1)
	if contentWidth < 68 {
		searchHeight := 3
		previewHeight := max(8, bodyHeight/3)
		listHeight := max(8, bodyHeight-searchHeight-previewHeight)
		searchBar := renderPanel(boxMutedStyle, contentWidth, searchHeight, m.searchBarText())
		chatList := renderPanel(boxStyle, contentWidth, listHeight, m.chatListBody(contentWidth-boxStyle.GetHorizontalFrameSize(), paddedContentHeight(listHeight)))
		preview := renderPanel(boxStyle, contentWidth, previewHeight, m.chatPreviewBody(contentWidth-boxStyle.GetHorizontalFrameSize()))
		return lipgloss.JoinVertical(lipgloss.Left, header, searchBar, chatList, preview, footer)
	}

	leftWidth, rightWidth := m.splitWidths(contentWidth)
	searchHeight := 3
	listHeight := max(8, bodyHeight-searchHeight)
	searchBar := renderPanel(boxMutedStyle, leftWidth, searchHeight, m.searchBarText())
	chatList := renderPanel(boxStyle, leftWidth, listHeight, m.chatListBody(leftWidth-boxStyle.GetHorizontalFrameSize(), paddedContentHeight(listHeight)))
	preview := renderPanel(boxStyle, rightWidth, bodyHeight, m.chatPreviewBody(rightWidth-boxStyle.GetHorizontalFrameSize()))
	leftColumn := lipgloss.JoinVertical(lipgloss.Left, searchBar, chatList)
	body := lipgloss.JoinHorizontal(lipgloss.Top, leftColumn, lipgloss.NewStyle().Width(1).Render(""), preview)
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m Model) renderThread() string {
	contentWidth := max(48, m.width-2)
	header := m.renderHeader(m.threadTitle(), "")
	var help string
	if m.composing {
		help = "enter send  esc cancel  + files  alt+v voice  ctrl+v paste image  u history  d download  q quit"
	} else {
		help = "esc back  i compose  u load older messages  d download latest media  q quit"
	}
	footer := m.renderFooter(help)
	bodyHeight := max(8, m.height-countRenderedLines(header)-countRenderedLines(footer)-1)
	composerContent := m.composerBody(contentWidth - boxMutedStyle.GetHorizontalFrameSize())
	composerHeight := max(3, countRenderedLines(composerContent)+boxMutedStyle.GetVerticalFrameSize())
	messageHeight := max(8, bodyHeight-composerHeight)
	threadContentHeight := paddedContentHeight(messageHeight)
	messages := renderPanel(boxStyle, contentWidth, messageHeight, m.threadBody(threadContentHeight, contentWidth-boxStyle.GetHorizontalFrameSize()))
	composer := renderPanel(boxMutedStyle, contentWidth, composerHeight, composerContent)
	return lipgloss.JoinVertical(lipgloss.Left, header, messages, composer, footer)
}

func (m Model) redrawCmd() tea.Cmd {
	if !m.fullRedraw {
		return nil
	}
	return tea.ClearScreen
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
		return chat.Title
	}
	for _, chat := range m.chats {
		if chat.JID == m.currentChatID {
			return chat.Title
		}
	}
	return m.currentChatID
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

func loadChatsCmd(repo *appstore.Store, query string) tea.Cmd {
	return func() tea.Msg {
		limit := chatListLimit
		if strings.TrimSpace(query) == "" {
			limit = defaultChatListLimit
		}
		chats, err := repo.ListChats(context.Background(), query, limit)
		return chatsLoadedMsg{chats: chats, err: err}
	}
}

func loadMessagesCmd(repo *appstore.Store, chatJID string) tea.Cmd {
	return func() tea.Msg {
		messages, err := repo.ListMessages(context.Background(), chatJID, messageLimit)
		return messagesLoadedMsg{chatJID: chatJID, messages: messages, err: err}
	}
}

func resetUnreadCmd(repo *appstore.Store, chatJID string) tea.Cmd {
	return func() tea.Msg {
		err := repo.ResetUnread(context.Background(), chatJID)
		return opResultMsg{err: err}
	}
}

func sendTextCmd(transport domain.Transport, chatJID, text string) tea.Cmd {
	return func() tea.Msg {
		err := transport.SendText(context.Background(), chatJID, text)
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
			attachment: stagedAttachment{token: token, path: path, kind: domain.MediaKindImage},
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
			attachment: stagedAttachment{token: token, path: result.Path, kind: domain.MediaKindVoice, secs: result.Duration},
			status:     "Voice note added to draft",
		}
	}
}

func sendStagedAttachmentsCmd(transport domain.Transport, chatJID string, attachments []stagedAttachment, draft string) tea.Cmd {
	return func() tea.Msg {
		captionConsumed := false
		for _, attachment := range attachments {
			switch attachment.kind {
			case domain.MediaKindVoice:
				if err := transport.SendVoiceNote(context.Background(), chatJID, attachment.path, attachment.secs); err != nil {
					return opResultMsg{err: err}
				}
			case domain.MediaKindImage:
				imageCaption := ""
				if !captionConsumed {
					imageCaption = draft
					captionConsumed = imageCaption != ""
				}
				if err := transport.SendImage(context.Background(), chatJID, attachment.path, imageCaption); err != nil {
					return opResultMsg{err: err}
				}
			default:
				mediaCaption := ""
				if !captionConsumed {
					mediaCaption = draft
					captionConsumed = mediaCaption != ""
				}
				if err := transport.SendMedia(context.Background(), chatJID, attachment.path, mediaCaption); err != nil {
					return opResultMsg{err: err}
				}
			}
		}
		if remaining := strings.TrimSpace(draft); remaining != "" && !captionConsumed {
			if err := transport.SendText(context.Background(), chatJID, remaining); err != nil {
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
		return opResultMsg{err: err}
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

func suffix(input string) string {
	if input == "" {
		return ""
	}
	return " · " + input
}

func receiptSuffix(msg domain.Message) string {
	if !msg.FromMe {
		return ""
	}
	switch msg.Receipt {
	case domain.ReceiptStateRead:
		return "  " + mutedStyle.Render("✓✓ read")
	case domain.ReceiptStateDelivered:
		return "  " + mutedStyle.Render("✓✓ delivered")
	case domain.ReceiptStateSent:
		return "  " + mutedStyle.Render("✓ sent")
	default:
		return ""
	}
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
	left := lipgloss.JoinVertical(lipgloss.Left, titleStyle.Render("WhatsApp Terminal"), mutedStyle.Render(title))
	width := max(40, m.width-2)
	if subtitle != "" {
		right := mutedStyle.Render(truncateText(subtitle, max(16, m.width/3)))
		return headerStyle.Width(width).Render(lipgloss.JoinHorizontal(lipgloss.Top, left, lipgloss.NewStyle().Width(max(1, width-lipgloss.Width(left)-lipgloss.Width(right)-2)).Render(""), right))
	}
	return headerStyle.Width(width).Render(left)
}

func (m Model) renderFooter(help string) string {
	lines := []string{mutedStyle.Render(help)}
	if m.lastErr != "" {
		lines = append(lines, errorStyle.Render(m.lastErr))
	}
	return footerStyle.Width(max(40, m.width-2)).Render(strings.Join(lines, "\n"))
}

func (m Model) searchBarText() string {
	if m.searching || m.search.Value() != "" {
		return m.search.View()
	}
	return mutedStyle.Render("Press / to search chats by name or JID")
}

func (m Model) chatListBody(width, height int) string {
	width = max(18, width)
	height = max(1, height)
	if len(m.chats) == 0 {
		if m.syncingRecent {
			lines := []string{
				metaStyle.Render("Syncing recent chats"),
				mutedStyle.Render(wrapText("Waiting for your phone to send the first recent chat batch. The inbox will appear as soon as a few recent conversations arrive.", width)),
			}
			if strings.TrimSpace(m.status) != "" {
				lines = append(lines, "", mutedStyle.Render(wrapText("Status: "+m.status, width)))
			}
			return strings.Join(lines, "\n")
		}
		return wrapText("No cached chats yet.\n\nUse --demo for an offline dataset, or pair a live session to populate the cache.", width)
	}

	type renderedChat struct {
		text  string
		lines int
	}
	rendered := make([]renderedChat, 0, len(m.chats))
	for idx, chat := range m.chats {
		prefix := "  "
		style := itemStyle
		if idx == m.selected {
			prefix = "› "
			style = selectedItemStyle
		}
		timestamp := ""
		if !chat.LastMessageAt.IsZero() {
			timestamp = chat.LastMessageAt.Local().Format("02 Jan 15:04")
		}
		unread := ""
		if chat.UnreadCount > 0 {
			unread = fmt.Sprintf(" [%d]", chat.UnreadCount)
		}
		subtitle := strings.TrimSpace(chat.LastMessagePreview)
		if subtitle == "" {
			subtitle = "No messages yet"
		}
		titleWidth := max(10, width-len(prefix))
		titleLine := truncateText(chat.Title+unread, titleWidth)
		subtitleLine := truncateText(subtitle+suffix(timestamp), max(12, width-3))
		line := fmt.Sprintf("%s%s\n   %s", prefix, titleLine, subtitleLine)
		text := style.Width(width).MaxWidth(width).Render(line)
		rendered = append(rendered, renderedChat{text: text, lines: countRenderedLines(text)})
	}

	selected := clampSelection(m.selected, len(rendered))
	start, end := selected, selected+1
	used := rendered[selected].lines
	left, right := selected-1, selected+1
	for {
		added := false
		if left >= 0 {
			extra := rendered[left].lines + 1
			if used+extra <= height {
				start = left
				used += extra
				left--
				added = true
			}
		}
		if right < len(rendered) {
			extra := rendered[right].lines + 1
			if used+extra <= height {
				end = right + 1
				used += extra
				right++
				added = true
			}
		}
		if !added {
			break
		}
	}

	lines := make([]string, 0, end-start)
	for idx := start; idx < end; idx++ {
		lines = append(lines, rendered[idx].text)
	}
	return strings.Join(lines, "\n\n")
}

func (m Model) chatPreviewBody(width int) string {
	width = max(18, width)
	chat := m.currentChat()
	if chat == nil {
		return wrapText("Select a chat to inspect it.", width)
	}

	title := truncateText(chat.Title, width)
	jid := truncateText(chat.JID, width)
	lastPreview := strings.TrimSpace(chat.LastMessagePreview)
	if lastPreview == "" {
		lastPreview = "No messages yet"
	}
	lines := []string{
		titleStyle.Render(title),
		mutedStyle.Render(jid),
		"",
		fmt.Sprintf("Unread: %d", chat.UnreadCount),
		fmt.Sprintf("Type: %s", chatType(chat.IsGroup)),
	}
	if !chat.LastMessageAt.IsZero() {
		lines = append(lines, fmt.Sprintf("Last activity: %s", chat.LastMessageAt.Local().Format("Mon 02 Jan 15:04")))
	}
	lines = append(lines,
		"",
		metaStyle.Render("Latest preview"),
		bodyStyle.Render(wrapText(lastPreview, width)),
	)
	if chat.LastSenderName != "" {
		lines = append(lines, mutedStyle.Render(wrapText("Last sender: "+chat.LastSenderName, width)))
	}
	lines = append(lines,
		"",
		metaStyle.Render("Actions"),
		mutedStyle.Render(wrapText("Enter opens the thread", width)),
		mutedStyle.Render(wrapText("/ filters the list", width)),
		mutedStyle.Render(wrapText("r reloads cached chats", width)),
	)
	return strings.Join(lines, "\n")
}

func (m Model) threadBody(messageHeight, width int) string {
	width = max(18, width)
	type renderedMessage struct {
		text  string
		lines int
	}
	rendered := make([]renderedMessage, 0, len(m.messages))
	for _, msg := range m.messages {
		name := msg.SenderName
		if name == "" {
			name = msg.SenderJID
		}
		if msg.FromMe {
			name = "You"
		}
		header := truncateText(fmt.Sprintf("%s  %s", msg.Timestamp.Local().Format(time.Kitchen), name), width)
		body := wrapText(msg.Text, width)
		text := fmt.Sprintf("%s\n%s%s", metaStyle.Render(header), bodyStyle.Render(body), receiptSuffix(msg))
		if msg.DownloadedPath != "" {
			text += "\n" + mutedStyle.Render("saved: "+filepath.Base(msg.DownloadedPath))
		}
		rendered = append(rendered, renderedMessage{text: text, lines: countRenderedLines(text)})
	}
	if len(rendered) == 0 {
		if m.threadHistoryPending {
			return mutedStyle.Render("Requesting messages for this chat from your phone...")
		}
		return mutedStyle.Render("No cached messages for this chat yet.")
	}

	selected := make([]string, 0, len(rendered))
	used := 0
	for idx := len(rendered) - 1; idx >= 0; idx-- {
		extra := rendered[idx].lines
		if len(selected) > 0 {
			extra += 1
		}
		if used+extra > messageHeight && len(selected) > 0 {
			break
		}
		selected = append(selected, rendered[idx].text)
		used += extra
	}
	for left, right := 0, len(selected)-1; left < right; left, right = left+1, right-1 {
		selected[left], selected[right] = selected[right], selected[left]
	}
	return strings.Join(selected, "\n\n")
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
	return mutedStyle.Render("Press i to compose. Use + for files, Ctrl+V for a screenshot, or Alt+V for a voice note.")
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
		if attachment.path != "" {
			_ = os.Remove(attachment.path)
		}
	}
	m.pendingAttachments = nil
	m.nextImageID = 0
	m.nextVoiceID = 0
}

func (m Model) renderComposeToolbar(width int) string {
	attach := toolbarButtonStyle.Render("+")
	voiceLabel := "mic"
	voiceStyle := toolbarButtonStyle
	if m.recordingVoice || m.stoppingVoice {
		voiceLabel = "play"
		voiceStyle = toolbarActiveButtonStyle
	}
	voice := voiceStyle.Render(voiceLabel)
	hint := "enter to queue message"
	if m.filePickerOpen {
		hint = "j/k move  enter select  h up  esc close"
	} else if m.recordingVoice {
		hint = "recording voice note"
	}
	line := lipgloss.JoinHorizontal(lipgloss.Left, attach, " ", voice, " ", mutedStyle.Render(truncateText(hint, max(10, width-12))))
	return lipgloss.NewStyle().MaxWidth(max(12, width)).Render(line)
}

func (m Model) renderFilePicker(width int) string {
	width = max(18, width)
	lines := []string{
		metaStyle.Render(truncateText("Files · "+m.filePickerDir, width)),
	}
	if len(m.filePickerEntries) == 0 {
		lines = append(lines, mutedStyle.Render("No files here"))
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
		line := "  " + truncateText(label, width-2)
		style := mutedStyle
		if idx == m.filePickerSelected {
			line = "› " + truncateText(label, width-2)
			style = selectedItemStyle
		}
		lines = append(lines, style.Render(line))
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
		return m, m.redrawCmd()
	}
	token := attachmentTokenForPath(entry.path, mediaKindForPath(entry.path))
	msg := attachmentStagedMsg{
		attachment: stagedAttachment{
			token: token,
			path:  entry.path,
			kind:  mediaKindForPath(entry.path),
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
			return m, m.redrawCmd()
		}
		m.stoppingVoice = true
		m.recordingVoice = false
		return m, tea.Batch(m.redrawCmd(), stopVoiceRecordingCmd(m.recorder, m.nextVoicePlaceholder()))
	}
	if m.recorder == nil {
		m.lastErr = "voice recorder is unavailable"
		return m, m.redrawCmd()
	}
	if err := m.recorder.Start(); err != nil {
		m.lastErr = err.Error()
		return m, m.redrawCmd()
	}
	m.recordingVoice = true
	m.recordingSince = time.Now()
	m.lastErr = ""
	return m, m.redrawCmd()
}

func (m *Model) clearPathSuggestions() {
	m.pathSuggestions = nil
	m.pathSuggestionIdx = 0
	m.pathSuggestionFocus = false
}

func (m *Model) refreshPathSuggestions() {
	suggestions := filePathSuggestions(m.composer.Value(), maxPathSuggestions)
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
	m.composer.SetValue(m.pathSuggestions[m.pathSuggestionIdx].replacement)
	m.refreshPathSuggestions()
	return true
}

func (m Model) renderPathSuggestions(width int) string {
	if len(m.pathSuggestions) == 0 {
		return ""
	}
	lines := []string{metaStyle.Render("Paths · tab apply · ctrl+n / ctrl+p navigate")}
	for idx, suggestion := range m.pathSuggestions {
		label := truncateText(suggestion.label, max(12, width))
		if suggestion.isDir {
			label += string(os.PathSeparator)
		}
		line := "  " + label
		style := mutedStyle
		if idx == m.pathSuggestionIdx {
			line = "› " + label
			style = selectedItemStyle
		}
		lines = append(lines, style.Render(line))
	}
	if m.pathSuggestionFocus {
		lines[0] = metaStyle.Render("Paths · j/k move · enter/tab apply · esc return")
	} else {
		lines[0] = metaStyle.Render("Paths · tab focus list · keep typing to refine")
	}
	return strings.Join(lines, "\n")
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

func mediaKindForPath(path string) domain.MediaKind {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return domain.MediaKindImage
	case ".mp4", ".mov", ".mkv", ".webm":
		return domain.MediaKindVideo
	case ".mp3", ".wav", ".m4a", ".aac", ".ogg", ".opus":
		return domain.MediaKindAudio
	default:
		return domain.MediaKindDocument
	}
}

func attachmentTokenForPath(path string, kind domain.MediaKind) string {
	name := filepath.Base(path)
	switch kind {
	case domain.MediaKindImage:
		return "[Image: " + name + "]"
	case domain.MediaKindVideo:
		return "[Video: " + name + "]"
	case domain.MediaKindAudio:
		return "[Audio: " + name + "]"
	default:
		return "[File: " + name + "]"
	}
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
	contentWidth := max(48, m.width-2)
	header := m.renderHeader(m.threadTitle(), "")
	help := "esc back  i compose  u load older messages  d download latest media  q quit"
	if m.composing {
		help = "enter send  esc cancel  + files  alt+v voice  ctrl+v paste image  u history  d download  q quit"
	}
	footer := m.renderFooter(help)
	bodyHeight := max(8, m.height-countRenderedLines(header)-countRenderedLines(footer)-1)
	composerContent := m.composerBody(contentWidth - boxMutedStyle.GetHorizontalFrameSize())
	composerHeight := max(3, countRenderedLines(composerContent)+boxMutedStyle.GetVerticalFrameSize())
	messageHeight := max(8, bodyHeight-composerHeight)
	composerTop := countRenderedLines(header) + messageHeight
	toolbarY := composerTop + 1
	contentX := boxMutedStyle.GetPaddingLeft() + 1
	if y != toolbarY || x < contentX {
		return ""
	}
	relativeX := x - contentX
	switch {
	case relativeX >= 0 && relativeX <= 2:
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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func renderPanel(style lipgloss.Style, totalWidth, totalHeight int, content string) string {
	totalWidth = max(style.GetHorizontalFrameSize()+1, totalWidth)
	totalHeight = max(style.GetVerticalFrameSize()+1, totalHeight)
	return style.Width(totalWidth - style.GetHorizontalFrameSize()).Height(totalHeight - style.GetVerticalFrameSize()).Render(content)
}

func truncateText(text string, width int) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	if width <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	if width == 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
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
			if current == "" {
				current = word
				continue
			}
			if len([]rune(current))+1+len([]rune(word)) <= width {
				current += " " + word
				continue
			}
			lines = append(lines, current)
			if len([]rune(word)) > width {
				lines = append(lines, truncateText(word, width))
				current = ""
				continue
			}
			current = word
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

func paddedContentHeight(totalHeight int) int {
	return max(1, totalHeight-boxStyle.GetVerticalFrameSize()-2)
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

var (
	titleStyle               = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("69"))
	mutedStyle               = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	errorStyle               = lipgloss.NewStyle().Foreground(lipgloss.Color("204"))
	itemStyle                = lipgloss.NewStyle()
	selectedItemStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Background(lipgloss.Color("62"))
	metaStyle                = lipgloss.NewStyle().Foreground(lipgloss.Color("109"))
	bodyStyle                = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	toolbarButtonStyle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252")).Background(lipgloss.Color("238")).Padding(0, 1)
	toolbarActiveButtonStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Background(lipgloss.Color("34")).Padding(0, 1)
	boxStyle                 = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("62")).Padding(1, 2)
	boxMutedStyle            = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("240")).Padding(0, 2)
	qrBoxStyle               = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("62")).Padding(1, 2)
	headerStyle              = lipgloss.NewStyle().Padding(0, 1)
	footerStyle              = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Padding(0, 1)
)
