package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	overlay "github.com/rmhubbert/bubbletea-overlay"
)

// modalSize calculates outer dimensions for a modal box holding content of
// contentW columns and contentH rows. The box is capped at the body it floats
// over (min(content+chrome, bodyW-4) by min(content+2, bodyH-2)), floored at a
// border-row-border 3. The cap keeps Composite from falling back to replacing
// the background entirely.
func (m model) modalSize(contentW, contentH int) (w, h int) {
	bodyW := m.width
	bodyH := max(4, m.height-1)
	w = max(3, min(contentW+4, bodyW-4))
	h = max(3, min(contentH+2, bodyH-2))
	return w, h
}

// promoteContentSize computes the natural inner dimensions for the promote picker.
func (m model) promoteContentSize() (w, h int) {
	targets := m.promoteTargets
	title, first := "promote", ""
	if len(targets) == 1 {
		title += " " + targets[0].ticket
		first = targets[0].ticket
	} else {
		title = fmt.Sprintf("%s %d tickets", title, len(targets))
		first = fmt.Sprintf("%d tickets selected", len(targets))
	}
	w = max(lipgloss.Width(title)+2, lipgloss.Width(first))
	for _, status := range m.promoteStatuses {
		w = max(w, lipgloss.Width(status)+2)
	}
	h = len(m.promoteStatuses) + 2
	return w, h
}

// helpContentSize computes the natural inner dimensions for the help cheat sheet.
func (m model) helpContentSize() (w, h int) {
	text := m.helpText()
	lines := strings.Split(text, "\n")
	w = 0
	for _, l := range lines {
		w = max(w, lipgloss.Width(l))
	}
	return w, len(lines)
}

// modalContent dispatches to the active modal renderer in priority order
// (helpOn, promoting, filtering, ejection, ejecting), returning the rendered box or "".
func (m model) modalContent() string {
	switch {
	case m.helpOn:
		w, h := m.modalSize(m.helpContentSize())
		return panelBox(styleTitleFocus.Render("help"), true, w, h,
			strings.Split(m.helpVp.View(), "\n"), padMain, m.helpScrollbar(h))
	case m.promoting:
		w, h := m.modalSize(m.promoteContentSize())
		return m.promotePicker(w, h)
	case m.filtering:
		w, h := m.modalSize(m.filterContentSize())
		return m.filterPicker(w, h)
	case m.ejection != nil:
		w, h := m.modalSize(76, 12)
		return m.ejectResult(*m.ejection, w, h)
	case m.ejecting:
		w, h := m.modalSize(76, 7)
		return m.ejectConfirm(m.ejectRow, w, h)
	default:
		return ""
	}
}

// composeModal composites the active modal box over the rendered body,
// centered horizontally and vertically.
func (m model) composeModal(body string) string {
	fg := m.modalContent()
	if fg == "" {
		return body
	}
	return overlay.Composite(fg, body, overlay.Center, overlay.Center, 0, 0)
}

// helpScrollbar is the position indicator over the help modal's viewport.
func (m model) helpScrollbar(h int) *scrollbar {
	sb, ok := scrollThumb(m.helpVp.TotalLineCount(), h-2, m.helpVp.YOffset)
	if !ok {
		return nil
	}
	return &sb
}
