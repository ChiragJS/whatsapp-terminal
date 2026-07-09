package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// canOpenKeymapEditor reports whether the keybinding editor may be opened. It
// is reachable from the chat list and thread navigation, but not while typing
// (search/compose) or inside another modal overlay.
func (m Model) canOpenKeymapEditor() bool {
	return !m.searching && !m.composing && !m.selecting && !m.mediaPickerOpen && !m.filePickerOpen
}

func (m *Model) openKeymapEditor() {
	m.keymapEditorOpen = true
	m.keymapCapture = false
	m.keymapEditorIndex = clampSelection(m.keymapEditorIndex, len(actionSpecs))
}

// updateKeymapEditor handles all key input while the editor overlay is open.
func (m Model) updateKeymapEditor(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.keymapCapture {
		return m.captureKeymapBinding(msg)
	}
	switch msg.String() {
	case "esc", "ctrl+k":
		m.keymapEditorOpen = false
		return m, nil
	case "j", "down":
		m.keymapEditorIndex = clampSelection(m.keymapEditorIndex+1, len(actionSpecs))
		return m, nil
	case "k", "up":
		m.keymapEditorIndex = clampSelection(m.keymapEditorIndex-1, len(actionSpecs))
		return m, nil
	case "enter":
		m.keymapCapture = true
		m.status = "Press the new key… esc cancels"
		return m, nil
	case "x":
		return m.resetKeymapAction()
	}
	return m, nil
}

// captureKeymapBinding consumes the next key press as the new binding for the
// selected action. Reserved keys and conflicts are rejected; an accepted key
// replaces the action's binding, is persisted immediately, and rebuilds the
// dispatch index.
func (m Model) captureKeymapBinding(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "esc" {
		m.keymapCapture = false
		m.status = "Rebind canceled"
		return m, nil
	}
	spec := actionSpecs[clampSelection(m.keymapEditorIndex, len(actionSpecs))]
	if reservedKeys()[key] {
		m.keymapCapture = false
		m.lastErr = fmt.Sprintf("%q is reserved and cannot be bound", key)
		return m, nil
	}
	if label, clash := m.bindingConflict(spec.context, key, spec.action); clash {
		m.keymapCapture = false
		m.lastErr = fmt.Sprintf("%q already bound to %s", key, label)
		return m, nil
	}
	m.keymap[spec.action] = []string{key}
	m.keyIndex = buildKeymapIndex(m.keymap)
	m.keymapCapture = false
	m.persistKeymap()
	m.status = fmt.Sprintf("%q bound to %s", key, spec.label)
	return m, nil
}

// resetKeymapAction restores the selected action to its default binding and
// persists the change. Defaults that now collide with another action's
// current (possibly rebound) key are dropped rather than silently
// overwriting m.keymap with a self-conflicting state that buildKeymapIndex
// would then resolve inconsistently.
func (m Model) resetKeymapAction() (tea.Model, tea.Cmd) {
	spec := actionSpecs[clampSelection(m.keymapEditorIndex, len(actionSpecs))]
	var kept []string
	var dropped string
	for _, key := range spec.defaults {
		if label, clash := m.bindingConflict(spec.context, key, spec.action); clash {
			dropped = fmt.Sprintf("%q already bound to %s", key, label)
			continue
		}
		kept = append(kept, key)
	}
	m.keymap[spec.action] = kept
	m.keyIndex = buildKeymapIndex(m.keymap)
	m.persistKeymap()
	switch {
	case len(kept) == 0:
		m.lastErr = fmt.Sprintf("%s default %s, left unbound", spec.label, dropped)
	case dropped != "":
		m.status = fmt.Sprintf("%s reset to default (%s)", spec.label, dropped)
	default:
		m.status = fmt.Sprintf("%s reset to default", spec.label)
	}
	return m, nil
}

// persistKeymap writes the current keymap and advances keymapModTime so our
// own write does not trigger the live-reload "reloaded" status.
func (m *Model) persistKeymap() {
	if err := SaveKeymap(m.dataDir, m.keymap); err != nil {
		m.lastErr = "save keybindings: " + err.Error()
		return
	}
	if t, ok := keymapFileInfo(m.dataDir); ok {
		m.keymapModTime = t
	}
}

// bindingConflict reports the label of an action already using key in a
// conflicting scope (same context, or either side global), excluding self.
func (m Model) bindingConflict(ctx keyContext, key string, self Action) (string, bool) {
	for _, spec := range actionSpecs {
		if spec.action == self {
			continue
		}
		relevant := spec.context == ctx || spec.context == contextGlobal || ctx == contextGlobal
		if !relevant {
			continue
		}
		for _, k := range m.keymap[spec.action] {
			if k == key {
				return spec.label, true
			}
		}
	}
	return "", false
}

// renderKeymapEditor draws the full-screen keybinding editor overlay.
func (m Model) renderKeymapEditor() string {
	header := m.renderHeader("Keybindings", "")
	footer := m.renderFooter("enter rebind  x reset  j/k move  esc close")
	contentWidth := max(48, m.width-2)
	bodyHeight := max(8, m.height-countRenderedLines(header)-countRenderedLines(footer)-1)
	innerWidth := contentWidth - boxStyle.GetHorizontalFrameSize()
	body := m.keymapEditorBody(paddedContentHeight(bodyHeight), innerWidth)
	panel := renderPanel(boxStyle, contentWidth, bodyHeight, body)
	return lipgloss.JoinVertical(lipgloss.Left, header, panel, footer)
}

// keymapEditorBody renders the grouped action list, windowed around the
// current selection like the media picker.
func (m Model) keymapEditorBody(height, width int) string {
	width = max(24, width)
	var rows []string
	selectedRow := 0
	lastCtx := keyContext("")
	for i, spec := range actionSpecs {
		if spec.context != lastCtx {
			if lastCtx != "" {
				rows = append(rows, "")
			}
			rows = append(rows, smallCap(contextTitles[spec.context], width))
			lastCtx = spec.context
		}
		selected := i == m.keymapEditorIndex
		if selected {
			selectedRow = len(rows)
		}
		rows = append(rows, m.keymapEditorRow(spec, selected, width))
	}

	height = max(3, height)
	if len(rows) <= height {
		return strings.Join(rows, "\n")
	}
	start := selectedRow - height/2
	if start < 0 {
		start = 0
	}
	if start+height > len(rows) {
		start = len(rows) - height
	}
	return strings.Join(rows[start:start+height], "\n")
}

func (m Model) keymapEditorRow(spec actionSpec, selected bool, width int) string {
	rail := "  "
	labelStyle := chipLabelStyle
	if selected {
		rail = railStyle.Render("▌ ")
		labelStyle = selectedItemStyle
	}
	label := labelStyle.Render(spec.label)
	if selected && m.keymapCapture {
		return rail + label + "  " + slateStyle.Render("press a key…  esc cancels")
	}
	keys := chipKeyStyle.Render(strings.Join(m.keymap[spec.action], " "))
	marker := ""
	if isDefaultBinding(m.keymap[spec.action], spec.defaults) {
		marker = " " + subtleStyle.Render("(default)")
	}
	line := rail + label + "  " + keys + marker
	if lipgloss.Width(line) > width {
		return truncateText(line, width)
	}
	return line
}
