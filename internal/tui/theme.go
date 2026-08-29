package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/mattwalters/lerp/internal/theme"
)

var (
	colorFocus        = theme.ColorFocus
	colorRunning      = theme.ColorRunning
	colorProvisioning = theme.ColorProvisioning
	colorAttention    = theme.ColorAttention
	colorFaint        = theme.ColorFaint
	colorSelected     = theme.ColorSelected
	colorWordmark     = theme.ColorWordmark
)

var (
	styleTicket       = theme.Ticket
	styleFocus        = theme.Focus
	styleRunning      = theme.Running
	styleProvisioning = theme.Provisioning
	styleAttention    = theme.Attention
	styleFaint        = theme.Faint
	styleErr          = theme.Err
	styleMatch        = theme.Match
	stylePlain        = theme.Plain
	styleMark         = theme.Mark
	styleWordmark     = theme.Wordmark
	styleSelected     = theme.Selected
	styleBorder       = theme.Border
	styleBorderFocus  = theme.BorderFocus
	styleTitleFocus   = theme.TitleFocus
)

// UseBackground applies the background override.
func UseBackground() error { return theme.UseBackground() }

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

// moreMarker is the "⋯ n more" line standing in for rows cut by a window.
func moreMarker(n int) string {
	return styleFaint.Render(fmt.Sprintf("⋯ %d more", n))
}

// fitRows cuts a row list down to a body of ih lines, standing in for what
// it dropped with a faint "⋯ n more" — so a deep list can never push a panel
// out of shape.
func fitRows(rows []string, ih int) []string {
	if over := len(rows) - ih; over > 0 && ih >= 1 {
		return append(append([]string{}, rows[:ih-1]...), moreMarker(over+1))
	}
	return rows
}

// scrollbar is the main pane's position indicator: which of a panel's ih
// body rows the viewport's thumb covers, drawn over the right border in
// place of its usual edge. nil draws a plain edge — every panelBox caller
// but the main pane itself, and a main pane whose content is no taller than
// the pane holding it (see scrollThumb).
type scrollbar struct{ top, len int }

// scrollThumbGlyph fills the thumb's rows. A solid block against the
// border's own line character is a shape difference as well as a colour
// one — the palette's own rule (see theme.go's comment on colour), so the
// track still reads against the thumb with no colour at all.
const scrollThumbGlyph = "█"

// scrollThumb is the proportion math behind the scrollbar: which rows of a
// height-row track a thumb should cover for a total-line document scrolled
// to yOffset. ok is false when the document fits the track outright — there
// is nothing to scroll, so nothing to point at, and a thumb spanning every
// row would read as full when it means something else entirely.
//
// The thumb's length is proportional to the fraction of the document the
// track can show at once, floored at one row so a long document never loses
// the thumb altogether. Its position is yOffset scaled by the room the thumb
// has to move in (height-len) over the room the content has to scroll in
// (total-height) — zero at the top and exactly height-len, flush with the
// bottom, when yOffset is the viewport's own maximum. Both edges land
// exactly because the scaling divides out to the untouched numerator there;
// nothing about it depends on rounding.
func scrollThumb(total, height, yOffset int) (sb scrollbar, ok bool) {
	if height <= 0 || total <= height {
		return scrollbar{}, false
	}
	length := max(1, height*height/total)
	maxOffset := total - height
	yOffset = clampIndex(yOffset, maxOffset+1)
	top := yOffset * (height - length) / maxOffset
	return scrollbar{top: top, len: length}, true
}

// panelBox draws one bordered panel with its title set into the top border —
// the cockpit's whole chrome. rows are already-styled lines, each truncated
// (ANSI-aware) to the content width left by pad. sb overrides the right
// border with a scrollbar thumb on the rows it covers; nil draws the plain
// edge every panel but the main pane draws.
//
// Focus is drawn as weight as well as colour: the focused panel takes the
// heavy box, so which panel the keys are talking to still reads on a
// 16-color terminal, the same rule the state dots follow.
func panelBox(title string, focused bool, w, h int, rows []string, pad padding, sb *scrollbar) string {
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
		right := bd.Right
		if sb != nil && i >= sb.top && i < sb.top+sb.len {
			right = scrollThumbGlyph
		}
		b.WriteString(border.Render(bd.Left) + lp + padTo(row, cw) + rp + border.Render(right))
		b.WriteString("\n")
	}
	b.WriteString(border.Render(bd.BottomLeft + strings.Repeat(bd.Bottom, bw) + bd.BottomRight))
	return b.String()
}

// ansiReset is the sequence every span lipgloss renders ends in. It turns
// off the background as well as the foreground, which is why selectRow
// cannot simply wrap a row that already carries spans.
const ansiReset = "\x1b[0m"

// padTo fills a rendered string out to w columns. The string already carries
// ANSI, so its width is measured and never counted — one spelling of that
// arithmetic for every caller that lays something out against a width.
func padTo(s string, w int) string {
	return s + strings.Repeat(" ", max(0, w-lipgloss.Width(s)))
}

// bandOpen is the sequence that turns the selection band on, and "" on a
// profile that draws no band. Which profiles those are is colorSelected's
// own declaration to make and not a second rule here: the slots it leaves
// empty render nothing, so this is only ever asking what the style rendered.
func bandOpen() string {
	return strings.TrimSuffix(styleSelected.Render(""), ansiReset)
}

// selectRow lays the selection band under one line of the row the cursor is
// on, out to the panel's inner width so the whole line reads as one object.
//
// A row is a run of styled spans and every span ends in a full reset, so a
// background wrapped around the outside would stop at the first one — the
// row would be tinted up to its first faint or coloured cell and bare after
// it. The band is re-opened after each reset instead, which tints a string
// that is already ANSI without rebuilding the spans that made it.
//
// Where bandOpen draws no band the row comes back padded but untinted, and
// marker's ▸ is the cursor, as it was.
func selectRow(row string, width int) string {
	row = padTo(row, width)
	open := bandOpen()
	if open == "" {
		return row
	}
	tinted := open + strings.ReplaceAll(row, ansiReset, ansiReset+open)
	// The row's own last span leaves a re-open with nothing after it; drop
	// it rather than emit a band over zero columns.
	return strings.TrimSuffix(tinted, ansiReset+open) + ansiReset
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
	return padTo(left, leftMax) + " " + right
}

// cursor is what the focus window needs to cut a list between its rows
// rather than through one: where the selection sits among the rendered lines
// and how many lines that row draws, plus which lines continue the row above
// them. A work row a lane holds is two lines, and a window that keeps only
// one of them either cuts the run's own reading off or leaves it under
// another ticket's name, reading as that ticket's.
//
// at is -1 when the panel has nothing to select, and cont may be nil for a
// list whose rows are all one line, which is every list but work's.
type cursor struct {
	at   int
	span int
	cont []bool
}

// continues reports whether the line at i finishes a row that starts above
// it — so a window opening there would strand it.
func (c cursor) continues(i int) bool {
	return i < len(c.cont) && c.cont[i]
}

// skipContinuation moves a window's top edge down off a line that finishes
// the row above it. The line costs less than the misreading: a run's clock
// under the name of the ticket that happens to be above it is a wrong fact,
// where dropping it is only a shorter list.
func skipContinuation(cur cursor, lo int) int {
	for cur.continues(lo) {
		lo++
	}
	return lo
}

// windowRows slides rows so the selected row stays visible within ih lines,
// standing in for the spans cut at either edge with a faint "⋯ n more".
// panelBox's own cut covers unfocused panels; this is the focused variant,
// so a selection can never walk off the rendered rows.
func windowRows(rows []string, cur cursor, ih int) []string {
	if ih < 2 || len(rows) <= ih {
		return rows
	}
	at := clampIndex(cur.at, len(rows))
	end := min(at+max(1, cur.span), len(rows)) // one past the row's last line
	if end <= ih-1 {
		return append(append([]string{}, rows[:ih-1]...), moreMarker(len(rows)-(ih-1)))
	}
	if lo := len(rows) - (ih - 1); at >= lo {
		lo = skipContinuation(cur, lo)
		return append([]string{moreMarker(lo)}, rows[lo:]...)
	}
	// The window ends just past the selected row, so a row drawing several
	// lines keeps all of them. Where it cannot — a body of three lines has
	// one to spend — it starts at the row instead: the line the cursor is on
	// is the one that has to be there.
	n := max(1, ih-2)
	lo := skipContinuation(cur, max(0, min(at, end-n)))
	hi := min(len(rows), lo+n)
	out := append([]string{moreMarker(lo)}, rows[lo:hi]...)
	if len(out) < ih {
		out = append(out, moreMarker(len(rows)-hi))
	}
	return out
}
