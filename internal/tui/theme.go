package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// The palette: one accent, four semantic states, a faint ramp. Adaptive
// pairs keep light terminals legible without a theme system. Color marks
// state, never decoration — and every state also has a shape or a label
// (● ◍ ○, "adopted", "idle"), so the screen still reads on a 16-color
// terminal or to a color-blind operator.
var (
	colorFocus        = lipgloss.AdaptiveColor{Light: "#6E4BC7", Dark: "#A78BFA"}
	colorRunning      = lipgloss.AdaptiveColor{Light: "#14855F", Dark: "#3DDC97"}
	colorProvisioning = lipgloss.AdaptiveColor{Light: "#A16207", Dark: "#F2B84B"}
	colorAdopted      = lipgloss.AdaptiveColor{Light: "#2B6CB0", Dark: "#6CA4F8"}
	colorAttention    = lipgloss.AdaptiveColor{Light: "#C4275B", Dark: "#F2618E"}
	colorFaint        = lipgloss.AdaptiveColor{Light: "#847E92", Dark: "#6B6684"}
	colorBadgeText    = lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#1A1725"}
)

var (
	styleTicket       = lipgloss.NewStyle().Bold(true)
	styleFocus        = lipgloss.NewStyle().Foreground(colorFocus)
	styleRunning      = lipgloss.NewStyle().Foreground(colorRunning)
	styleProvisioning = lipgloss.NewStyle().Foreground(colorProvisioning)
	styleAdopted      = lipgloss.NewStyle().Foreground(colorAdopted)
	styleAttention    = lipgloss.NewStyle().Foreground(colorAttention)
	styleFaint        = lipgloss.NewStyle().Foreground(colorFaint)
	styleErr          = lipgloss.NewStyle().Foreground(colorAttention)

	styleBorder      = lipgloss.NewStyle().Foreground(colorFaint)
	styleBorderFocus = lipgloss.NewStyle().Foreground(colorFocus)
	styleTitleFocus  = lipgloss.NewStyle().Foreground(colorFocus).Bold(true)
)

// heartbeatFrames animate the status bar while a pass is in flight and the
// provisioning dot while a workspace is being prepared. The frames come from
// bubbles' spinner set; advancing them rides the existing 250ms poll instead
// of the spinner component's own tick stream — one clock for everything.
var heartbeatFrames = spinner.MiniDot.Frames

// panelBox draws one bordered panel with its title set into the top border —
// the cockpit's whole chrome. rows are already-styled lines; each is
// truncated (ANSI-aware) to the inner width, and a row overflow is cut with
// a faint "⋯ n more" so a deep list can never push the panel out of shape.
func panelBox(title string, focused bool, w, h int, rows []string) string {
	if w < 4 || h < 2 {
		return ""
	}
	iw, ih := w-2, h-2
	border := styleBorder
	if focused {
		border = styleBorderFocus
	}
	if over := len(rows) - ih; over > 0 && ih >= 1 {
		rows = append(append([]string{}, rows[:ih-1]...),
			styleFaint.Render(fmt.Sprintf("⋯ %d more", over+1)))
	}

	var b strings.Builder
	t := " " + title + " "
	if lipgloss.Width(t) > iw-2 {
		t = ansi.Truncate(t, max(0, iw-2), "… ")
	}
	b.WriteString(border.Render("╭─") + t)
	b.WriteString(border.Render(strings.Repeat("─", max(0, iw-1-lipgloss.Width(t))) + "╮"))
	b.WriteString("\n")
	for i := 0; i < ih; i++ {
		row := ""
		if i < len(rows) {
			row = ansi.Truncate(rows[i], iw, "…")
		}
		pad := strings.Repeat(" ", max(0, iw-lipgloss.Width(row)))
		b.WriteString(border.Render("│") + row + pad + border.Render("│"))
		b.WriteString("\n")
	}
	b.WriteString(border.Render("╰" + strings.Repeat("─", iw) + "╯"))
	return b.String()
}

// splitRow lays one list row out as left content and a right-hand column
// that survives a narrow panel: the left is truncated (ANSI-aware) to
// whatever the right does not need, so the trailing fact — a clock, a
// priority — is never the thing that falls off the edge.
func splitRow(left, right string, width int) string {
	leftMax := width - lipgloss.Width(right)
	if right != "" {
		leftMax--
	}
	left = ansi.Truncate(left, max(0, leftMax), "…")
	if right == "" {
		return left
	}
	pad := strings.Repeat(" ", max(0, leftMax-lipgloss.Width(left)))
	return left + pad + " " + right
}

// windowRows slides rows so the row at sel stays visible within ih lines,
// standing in for the spans cut at either edge with a faint "⋯ n more".
// panelBox's own cut covers unfocused panels; this is the focused variant,
// so a selection can never walk off the rendered rows.
func windowRows(rows []string, sel, ih int) []string {
	if ih < 2 || len(rows) <= ih {
		return rows
	}
	sel = clampIndex(sel, len(rows))
	more := func(n int) string { return styleFaint.Render(fmt.Sprintf("⋯ %d more", n)) }
	if sel < ih-1 {
		return append(append([]string{}, rows[:ih-1]...), more(len(rows)-(ih-1)))
	}
	if lo := len(rows) - (ih - 1); sel >= lo {
		return append([]string{more(lo)}, rows[lo:]...)
	}
	hi := sel + 1
	lo := hi - max(1, ih-2)
	out := append([]string{more(lo)}, rows[lo:hi]...)
	if len(out) < ih {
		out = append(out, more(len(rows)-hi))
	}
	return out
}
