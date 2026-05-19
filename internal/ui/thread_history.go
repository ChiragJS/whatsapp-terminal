package ui

import (
	"github.com/charmbracelet/bubbletea"

	"github.com/chirag/whatsapp-terminal/internal/domain"
)

func (m Model) scrollThread(delta int) (tea.Model, tea.Cmd) {
	if delta == 0 || m.currentChatID == "" {
		return m, nil
	}

	stateChanged := false
	if len(m.messages) > 0 {
		previousScroll := m.threadScroll
		m.threadScroll = clampThreadScroll(m.threadScroll+delta, len(m.messages))
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
	return m, batchCommands(m.redrawCmd(), cmd)
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
	return m.threadScroll >= max(0, len(m.messages)-threadPrefetchMargin)
}

func clampThreadScroll(scroll, total int) int {
	if total <= 1 {
		return 0
	}
	if scroll < 0 {
		return 0
	}
	maxScroll := total - 1
	if scroll > maxScroll {
		return maxScroll
	}
	return scroll
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
