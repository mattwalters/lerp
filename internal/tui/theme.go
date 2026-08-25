package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// The palette: one accent, three semantic states, a faint ramp. Adaptive
// pairs keep light terminals legible without a theme system. Color marks
// state, never decoration — and every state also has a shape or a label
// (a filled dot against a spinner frame, "running", "provisioning"), so
// the screen still reads on a 16-color terminal or to a color-blind
// operator.
var (
	colorFocus        = lipgloss.AdaptiveColor{Light: "#6E4BC7", Dark: "#A78BFA"}
	colorRunning      = lipgloss.AdaptiveColor{Light: "#14855F", Dark: "#3DDC97"}
	colorProvisioning = lipgloss.AdaptiveColor{Light: "#A16207", Dark: "#F2B84B"}
	colorAttention    = lipgloss.AdaptiveColor{Light: "#C4275B", Dark: "#F2618E"}
	colorFaint        = lipgloss.AdaptiveColor{Light: "#847E92", Dark: "#6B6684"}
	colorBadgeText    = lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#1A1725"}
)

var (
	styleTicket       = lipgloss.NewStyle().Bold(true)
	styleFocus        = lipgloss.NewStyle().Foreground(colorFocus)
	styleRunning      = lipgloss.NewStyle().Foreground(colorRunning)
	styleProvisioning = lipgloss.NewStyle().Foreground(colorProvisioning)
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

// padding is the breathing room between a panel's border and its rows.
// Horizontal padding costs two columns per panel and the inbox table is
// already truncating titles, so it is asymmetric on purpose: the main pane
// is padded both sides, because prose pressed against a box edge reads
// badly, and the list panels take a left pad only — their right edge stays
// the truncation point it already was.
type padding struct{ left, right int }

var (
	padList = padding{left: 1}
	padMain = padding{left: 1, right: 1}
)

// inner is the width a panel w columns wide has left for its rows.
func (p padding) inner(w int) int { return w - 2 - p.left - p.right }

// fitRows cuts a row list down to a body of ih lines, standing in for what
// it dropped with a faint "⋯ n more" — so a deep list can never push a panel
// out of shape.
func fitRows(rows []string, ih int) []string {
	if over := len(rows) - ih; over > 0 && ih >= 1 {
		return append(append([]string{}, rows[:ih-1]...),
			styleFaint.Render(fmt.Sprintf("⋯ %d more", over+1)))
	}
	return rows
}

// panelBox draws one bordered panel with its title set into the top border —
// the cockpit's whole chrome. rows are already-styled lines, each truncated
// (ANSI-aware) to the content width left by pad.
//
// Focus is drawn as weight as well as colour: the focused panel takes the
// heavy box, so which panel the keys are talking to still reads on a
// 16-color terminal, the same rule the state dots follow.
func panelBox(title string, focused bool, w, h int, rows []string, pad padding) string {
	bw, ih := w-2, h-2
	cw := bw - pad.left - pad.right
	if cw < 1 || ih < 1 {
		return ""
	}
	border, bd := styleBorder, lipgloss.RoundedBorder()
	if focused {
		border, bd = styleBorderFocus, lipgloss.ThickBorder()
	}
	rows = fitRows(rows, ih)

	var b strings.Builder
	t := " " + title + " "
	if lipgloss.Width(t) > bw-2 {
		t = ansi.Truncate(t, max(0, bw-2), "… ")
	}
	b.WriteString(border.Render(bd.TopLeft+bd.Top) + t)
	b.WriteString(border.Render(strings.Repeat(bd.Top, max(0, bw-1-lipgloss.Width(t))) + bd.TopRight))
	b.WriteString("\n")
	lp, rp := strings.Repeat(" ", pad.left), strings.Repeat(" ", pad.right)
	for i := 0; i < ih; i++ {
		row := ""
		if i < len(rows) {
			row = ansi.Truncate(rows[i], cw, "…")
		}
		fill := strings.Repeat(" ", max(0, cw-lipgloss.Width(row)))
		b.WriteString(border.Render(bd.Left) + lp + row + fill + rp + border.Render(bd.Right))
		b.WriteString("\n")
	}
	b.WriteString(border.Render(bd.BottomLeft + strings.Repeat(bd.Bottom, bw) + bd.BottomRight))
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
