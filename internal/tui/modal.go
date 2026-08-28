package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	overlay "github.com/rmhubbert/bubbletea-overlay"

	"github.com/mattwalters/lerp/internal/version"
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
// (helpOn, promoting, filtering, ejection, ejecting, upgradeOn), returning the
// rendered box or "".
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
	case m.upgradeOn:
		w, h := m.modalSize(76, 17)
		return m.upgradeModal(w, h)
	default:
		return ""
	}
}

// upgradeModal renders the upgrade instructions modal.
func (m model) upgradeModal(w, h int) string {
	width := padMain.inner(w)
	current := version.Version
	latest := clean(m.updateNotice.Latest)
	url := clean(m.updateNotice.URL)
	if url == "" && latest != "" {
		url = "https://github.com/mattwalters/lerp/releases/tag/" + latest
	}
	rows := []string{
		fmt.Sprintf("current %s · latest %s", clean(current), latest),
		"",
		styleFaint.Render("homebrew"),
		styleTicket.Render("brew upgrade lerp"),
		"",
		styleFaint.Render("go install"),
		styleTicket.Render(ansi.Wrap("go install github.com/mattwalters/lerp/cmd/lerp@latest", max(8, width), " ")),
		"",
		styleFaint.Render("install script"),
		styleTicket.Render(ansi.Wrap("curl -fsSL https://raw.githubusercontent.com/mattwalters/lerp/main/install.sh | sh", max(8, width), " ")),
	}
	if url != "" {
		rows = append(rows, "", styleFaint.Render("release notes"), styleTicket.Render(ansi.Wrap(url, max(8, width), " ")))
	}
	rows = append(rows, "", styleFaint.Render("esc dismisses this panel"))
	rows = strings.Split(strings.Join(rows, "\n"), "\n")
	return panelBox(styleTitleFocus.Render("upgrade"), true, w, h, rows, padMain, nil)
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
