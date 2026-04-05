package ui

import (
	"bytes"
	"context"
	"fmt"
	"os"
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
	err    error
	status string
}

type composeActionType int

const (
	composeActionText composeActionType = iota
	composeActionImage
)

type composeAction struct {
	kind    composeActionType
	text    string
	path    string
	caption string
}

type Model struct {
	repo       *appstore.Store
	transport  domain.Transport
	events     <-chan domain.Event
	fullRedraw bool

	mode          viewMode
	width         int
	height        int
	status        string
	lastErr       string
	qrCode        string
	ready         bool
	searching     bool
	composing     bool
	selected      int
	currentChatID string

	search    textinput.Model
	composer  textinput.Model
	clipboard clipboardReader

	chats    []domain.ChatSummary
	messages []domain.Message
}

func NewModel(repo *appstore.Store, transport domain.Transport) Model {
	return NewModelWithOptions(repo, transport, newSystemClipboard(), false)
}

func NewModelWithClipboard(repo *appstore.Store, transport domain.Transport, clipboard clipboardReader) Model {
	return NewModelWithOptions(repo, transport, clipboard, false)
}

func NewModelWithOptions(repo *appstore.Store, transport domain.Transport, clipboard clipboardReader, fullRedraw bool) Model {
	if clipboard == nil {
		clipboard = newSystemClipboard()
	}
	search := textinput.New()
	search.Placeholder = "Search chats (/)"
	search.Prompt = "Search: "
	search.CharLimit = 128

	composer := textinput.New()
	composer.Placeholder = "Type a message (i)"
	composer.Prompt = "> "
	composer.CharLimit = 4096

	return Model{
		repo:       repo,
		transport:  transport,
		events:     transport.Events(),
		mode:       viewChats,
		status:     "Starting WhatsApp terminal...",
		search:     search,
		composer:   composer,
		clipboard:  clipboard,
		fullRedraw: fullRedraw,
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
		cmds = append([]tea.Cmd{m.redrawCmd()}, cmds...)
		return m, tea.Batch(cmds...)
	case chatsLoadedMsg:
		if msg.err != nil {
			m.lastErr = msg.err.Error()
			return m, m.redrawCmd()
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
		}
		return m, m.redrawCmd()
	case opResultMsg:
		if msg.err != nil {
			m.lastErr = msg.err.Error()
		}
		if msg.status != "" {
			m.status = msg.status
		}
		return m, m.redrawCmd()
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
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
	case tea.KeyMsg:
		if m.composing {
			switch msg.String() {
			case "esc":
				m.composing = false
				m.composer.Blur()
				return m, m.redrawCmd()
			case "ctrl+v":
				return m, tea.Batch(m.redrawCmd(), pasteClipboardImageCmd(m.clipboard, m.transport, m.currentChatID))
			case "enter":
				action, err := parseComposeAction(m.composer.Value())
				if err != nil {
					m.lastErr = err.Error()
					return m, nil
				}
				chatID := m.currentChatID
				m.composer.SetValue("")
				if action.kind == composeActionImage {
					return m, sendImageCmd(m.transport, chatID, action.path, action.caption)
				}
				return m, sendTextCmd(m.transport, chatID, action.text)
			}
			var cmd tea.Cmd
			m.composer, cmd = m.composer.Update(msg)
			return m, tea.Batch(m.redrawCmd(), cmd)
		}

		switch msg.String() {
		case "esc":
			m.mode = viewChats
			m.currentChatID = ""
			m.messages = nil
			m.composing = false
			m.composer.Blur()
			return m, tea.Batch(m.redrawCmd(), loadChatsCmd(m.repo, m.search.Value()))
		case "i", "tab":
			m.composing = true
			return m, tea.Batch(m.redrawCmd(), m.composer.Focus())
		case "u":
			if m.currentChatID == "" {
				return m, nil
			}
			return m, requestHistoryCmd(m.transport, m.currentChatID, 50)
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
	header := m.renderHeader("Inbox", "Search and open chats")
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
	header := m.renderHeader(m.threadTitle(), m.status)
	composerHeight := 3
	if m.composing {
		composerHeight = 4
	}
	help := "esc back  i compose  u load older messages  q quit"
	if m.composing {
		help = "enter send  esc cancel compose  ctrl+v clipboard image  u history  q quit"
	}
	footer := m.renderFooter(help)
	bodyHeight := max(8, m.height-countRenderedLines(header)-countRenderedLines(footer)-1)
	messageHeight := max(8, bodyHeight-composerHeight)
	threadContentHeight := paddedContentHeight(messageHeight)
	messages := renderPanel(boxStyle, contentWidth, messageHeight, m.threadBody(threadContentHeight, contentWidth-boxStyle.GetHorizontalFrameSize()))
	composer := renderPanel(boxMutedStyle, contentWidth, composerHeight, m.composerBody())
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
		return opResultMsg{err: err, status: "Message sent"}
	}
}

func sendImageCmd(transport domain.Transport, chatJID, path, caption string) tea.Cmd {
	return func() tea.Msg {
		err := transport.SendImage(context.Background(), chatJID, path, caption)
		return opResultMsg{err: err, status: "Image sent"}
	}
}

func pasteClipboardImageCmd(clipboard clipboardReader, transport domain.Transport, chatJID string) tea.Cmd {
	return func() tea.Msg {
		if clipboard == nil {
			return opResultMsg{err: fmt.Errorf("clipboard image access is unavailable")}
		}
		imageData, err := clipboard.ReadImage()
		if err != nil {
			return opResultMsg{err: err}
		}
		file, err := os.CreateTemp("", "whatsapp-terminal-clipboard-*.png")
		if err != nil {
			return opResultMsg{err: fmt.Errorf("create clipboard image file: %w", err)}
		}
		path := file.Name()
		defer os.Remove(path)
		if _, err := file.Write(imageData); err != nil {
			_ = file.Close()
			return opResultMsg{err: fmt.Errorf("write clipboard image: %w", err)}
		}
		if err := file.Close(); err != nil {
			return opResultMsg{err: fmt.Errorf("close clipboard image file: %w", err)}
		}
		err = transport.SendImage(context.Background(), chatJID, path, "")
		return opResultMsg{err: err, status: "Clipboard image sent"}
	}
}

func requestHistoryCmd(transport domain.Transport, chatJID string, count int) tea.Cmd {
	return func() tea.Msg {
		err := transport.RequestHistory(context.Background(), chatJID, count)
		return opResultMsg{err: err}
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

func visibleMessages(messages []domain.Message, limit int) []domain.Message {
	if limit <= 0 || len(messages) <= limit {
		return messages
	}
	return messages[len(messages)-limit:]
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

func renderError(err string) string {
	if err == "" {
		return ""
	}
	return errorStyle.Render(err)
}

func (m Model) renderHeader(title, subtitle string) string {
	left := lipgloss.JoinVertical(lipgloss.Left, titleStyle.Render("WhatsApp Terminal"), mutedStyle.Render(title))
	badgeWidth := max(16, min(32, m.width/3))
	statusText := truncateText(strings.ToUpper(m.status), badgeWidth-2)
	rightLines := []string{statusBadgeStyle.Render(statusText)}
	if subtitle != "" && subtitle != m.status {
		rightLines = append(rightLines, mutedStyle.Render(truncateText(subtitle, max(16, m.width/3))))
	}
	right := lipgloss.JoinVertical(lipgloss.Right, rightLines...)
	width := max(40, m.width-2)
	return headerStyle.Width(width).Render(lipgloss.JoinHorizontal(lipgloss.Top, left, lipgloss.NewStyle().Width(max(1, width-lipgloss.Width(left)-lipgloss.Width(right)-2)).Render(""), right))
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
		rendered = append(rendered, renderedMessage{text: text, lines: countRenderedLines(text)})
	}
	if len(rendered) == 0 {
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

func (m Model) composerBody() string {
	if m.composing {
		return m.composer.View()
	}
	return mutedStyle.Render("Press i to compose. Use Ctrl+V for a clipboard screenshot or /image <path> :: optional caption.")
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
	if !strings.HasPrefix(input, "/image") {
		return composeAction{kind: composeActionText, text: input}, nil
	}
	rest := strings.TrimSpace(strings.TrimPrefix(input, "/image"))
	if rest == "" {
		return composeAction{}, fmt.Errorf("usage: /image <path> :: optional caption")
	}
	path, caption, err := parseImageCommand(rest)
	if err != nil {
		return composeAction{}, err
	}
	return composeAction{kind: composeActionImage, path: path, caption: caption}, nil
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
	titleStyle        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("69"))
	statusStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	mutedStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	errorStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("204"))
	itemStyle         = lipgloss.NewStyle()
	selectedItemStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Background(lipgloss.Color("62"))
	metaStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("109"))
	senderStyle       = lipgloss.NewStyle().Bold(true)
	bodyStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	boxStyle          = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("62")).Padding(1, 2)
	boxMutedStyle     = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("240")).Padding(0, 2)
	qrBoxStyle        = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("62")).Padding(1, 2)
	headerStyle       = lipgloss.NewStyle().Padding(0, 1)
	footerStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Padding(0, 1)
	statusBadgeStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Background(lipgloss.Color("34")).Padding(0, 1)
)
