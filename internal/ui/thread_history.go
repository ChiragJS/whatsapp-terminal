package ui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/chirag/whatsapp-terminal/internal/domain"
)

// scrollThread moves the thread viewport by delta display lines (positive =
// toward older messages). Nearing the oldest cached line triggers a
// background load of older history.
func (m Model) scrollThread(delta int) (tea.Model, tea.Cmd) {
	if delta == 0 || m.currentChatID == "" {
		return m, nil
	}

	stateChanged := false
	if len(m.messages) > 0 {
		previousScroll := m.threadScroll
		m.threadScroll = min(max(0, m.threadScroll+delta), m.maxThreadScroll())
		stateChanged = previousScroll != m.threadScroll
	}

	var cmd tea.Cmd
	if delta > 0 && (len(m.messages) == 0 || m.threadNearOldestBoundary()) {
		m, cmd = m.loadOlderThreadMessages()
		stateChanged = stateChanged || cmd != nil
	}
	if !stateChanged {
		return m, nil
	}
	return m, cmd
}

// maxThreadScroll is the highest line offset the viewport can reach: total
// rendered message lines minus the visible window.
func (m Model) maxThreadScroll() int {
	layout := m.threadLayout()
	lines := m.threadMessageLines(layout.contentWidth - boxStyle.GetHorizontalFrameSize())
	return max(0, len(lines)-paddedContentHeight(layout.messageHeight))
}

func (m Model) loadOlderThreadMessages() (Model, tea.Cmd) {
	if m.currentChatID == "" || m.threadLoadingOlder || m.threadHistoryPending {
		return m, nil
	}

	limit := m.messageLoadLimit()
	if len(m.messages) > 0 && len(m.messages) >= limit {
		m.threadMessageLimit = limit + messagePageSize
		m.threadLoadingOlder = true
		m.status = "Loading older cached messages..."
		return m, loadMessagesCmd(m.repo, m.currentChatID, m.threadMessageLimit)
	}

	m.threadHistoryPending = true
	m.status = "Requesting older messages..."
	return m, requestHistoryCmd(m.transport, m.currentChatID, historyRequestCount)
}

func (m Model) messageLoadLimit() int {
	if m.threadMessageLimit > 0 {
		return m.threadMessageLimit
	}
	return messageLimit
}

func (m Model) threadNearOldestBoundary() bool {
	if len(m.messages) == 0 {
		return true
	}
	return m.threadScroll >= max(0, m.maxThreadScroll()-threadPrefetchMargin)
}

func oldestMessageID(messages []domain.Message) string {
	if len(messages) == 0 {
		return ""
	}
	return messages[0].ID
}

func newestMessageID(messages []domain.Message) string {
	if len(messages) == 0 {
		return ""
	}
	return messages[len(messages)-1].ID
}
