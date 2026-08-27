package tui

import (
	"fmt"
	"os"
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
//
// Every entry clears a contrast floor against the backgrounds a terminal
// is likely to have; theme_test.go measures it and fails when one stops.
// Retuning a colour means re-measuring it there — the floor is the test,
// not this comment.
//
// De-emphasis has a floor too: faint is faint by weight and position, not
// by being hard to read. styleTicket is bold, so the ramp reads bold /
// normal / faint rather than normal / nearly-invisible.
var (
	// colorFocus is terminal green: a muted, desaturated phosphor tone, not
	// the neon `#33FF33`-class hacker green — CRT-phosphor lineage without
	// the gnarliness. colorRunning was already this family's other member,
	// which is why it moves to teal below rather than sitting two clicks
	// away in the same hue.
	colorFocus = lipgloss.AdaptiveColor{Light: "#186B3E", Dark: "#6FCF97"}
	// colorRunning moves to teal/cyan rather than staying green now that
	// colorFocus is green too: "selected" and "alive" are the board's most
	// load-bearing distinction, and a hue split (green vs teal, ~45° apart)
	// holds under a glance in a way that pushing two greens apart in
	// lightness alone would not — a work row shows both at once, next to a
	// focused border, so the two states need to separate at a glance and
	// not just under a calibrated eye.
	colorRunning      = lipgloss.AdaptiveColor{Light: "#0B6E85", Dark: "#22D3EE"}
	colorProvisioning = lipgloss.AdaptiveColor{Light: "#9A5E07", Dark: "#F2B84B"}
	colorAttention    = lipgloss.AdaptiveColor{Light: "#C4275B", Dark: "#F2618E"}
	// colorFaint is neutralised rather than tinted: it draws every unfocused
	// panel border, sitting right beside the new focused-green one, so the
	// old violet cast (low-saturation, but a cast all the same) was the one
	// leftover a side-by-side comparison would still catch. True grey (no
	// hue at all) reads as neither colour and needs no design decision about
	// which one it leans toward. Picked at the same relative luminance as
	// the value it replaces, so every contrast number pinned elsewhere in
	// this file (faint against black/white, faint against colorSelected's
	// band) still holds to the same precision.
	colorFaint = lipgloss.AdaptiveColor{Light: "#6C6C6C", Dark: "#939393"}
	// colorSelected is the band under the row the cursor is on. It is a
	// background, so it is not read — it is read *through*, by every colour
	// a row already carries. The tint is therefore the quietest one that
	// still finds itself across a panel, and it is priced against styleFaint,
	// the combination with the least to spare: faint keeps 5.09:1 on the dark
	// band against 6.84:1 on black, and 4.26:1 on the light one against
	// 5.25:1 on white — both re-derived at the same design point the purple
	// held (same lightness step off black/white as before), just re-hued to
	// green. Adaptive, because the same tint that reads as a band on a dark
	// terminal is a smudge on a light one.
	//
	// It is the one colour here spelled out per profile rather than left to
	// lipgloss to degrade, because it is the only one used as a background,
	// where degrading is destructive rather than approximate: the 6×6×6 cube
	// has no quiet tint of this hue either — #17271C quantises to xterm 232,
	// a step of the grey ramp itself, but #CCF0D8 (the light variant) lands
	// on a pale cube green, and 16 colours has none at any hue, where the
	// light variant comes out a solid, full-brightness green bar. The
	// 256-colour pair is off the grey ramp instead, chosen to hold the design
	// point rather than the hue: the same step off the terminal's own
	// background, and faint left where it was. The 16-colour slots are empty
	// on purpose, which renders no band at all — ▸ is the cursor there, which
	// is what the marker is kept as the fallback for.
	colorSelected = lipgloss.CompleteAdaptiveColor{
		Light: lipgloss.CompleteColor{TrueColor: "#CCF0D8", ANSI256: "254"},
		Dark:  lipgloss.CompleteColor{TrueColor: "#17271C", ANSI256: "234"},
	}
	// colorWordmark is the empty-board decoration (LERP-145), pinned below
	// contrastFloor on purpose: WCAG exempts pure decoration from the
	// contrast rules outright, and this mark carries no information for
	// dimness to put at risk (rule 1 — decoration only, forever).
	// TestWordmarkIsExemptDecoration in theme_test.go is the carve-out
	// itself, scoped to this one name.
	//
	// Spelled out per profile like colorSelected, and for the same reason:
	// the truecolor value is tuned to sit just under the floor, and 16
	// colours has no slot that dim — termenv's nearest ANSI match for
	// #48514B is bright-black, which most terminals render around #7E7E7E,
	// well *above* the floor and exactly the "full-brightness wall of ASCII
	// art" wordmarkVisible exists to rule out. The ANSI slots are left empty
	// on purpose, which renders no colour at all on that profile —
	// wordmarkVisible reads that as "cannot dim it" and the panel falls back
	// to its plain empty-state text, the same as under NO_COLOR. ANSI256
	// degrades fine on its own (both variants' nearest 256 match stays under
	// the floor, even though the light variant's nearest is a pale cube
	// cyan rather than a grey), so it gets an explicit value rather than
	// also going without.
	colorWordmark = lipgloss.CompleteAdaptiveColor{
		Light: lipgloss.CompleteColor{TrueColor: "#C1CDC5", ANSI256: "251"},
		Dark:  lipgloss.CompleteColor{TrueColor: "#48514B", ANSI256: "239"},
	}
)

// contrastFloor is the ratio every colour here has to clear against its
// backgrounds. WCAG asks 4.5:1 of text (1.4.3) and 3:1 of a graphic or a
// boundary that carries meaning (1.4.11); every colour in this palette
// renders text somewhere — faint draws the hint lines as well as the
// sparkline and the panel borders — so the stricter floor is the only one
// that binds, and one number covers both rules.
const contrastFloor = 4.5

// palette is every colour above in one list, so the contrast test can walk
// them. A colour added to the block above belongs here too, or nothing
// measures it — which the test checks, by name, against what this package
// declares.
var palette = []struct {
	name  string
	color lipgloss.AdaptiveColor
}{
	{"colorFocus", colorFocus},
	{"colorRunning", colorRunning},
	{"colorProvisioning", colorProvisioning},
	{"colorAttention", colorAttention},
	{"colorFaint", colorFaint},
}

// backgroundEnv lets an operator say which background their terminal has.
// lipgloss picks the Light or Dark variant from termenv's OSC 11 query, and
// a terminal that does not answer it — tmux, screen, plenty of ssh and CI
// terminals — falls back to black, so a light terminal silently gets the
// dark variants on white. This is the way out of that.
//
// An environment variable rather than a config key on purpose: lerp.toml
// describes the repo's pipeline, and this is one operator's terminal.
const backgroundEnv = "LERP_BACKGROUND"

// UseBackground applies the override if the operator set one. Run calls it
// before it renders anything, so every way into the TUI gets it; main calls
// it at the top of startup as well, so a value lerp cannot read is refused
// before the board check and the clone lock rather than after them.
// Applying it twice is applying it once.
func UseBackground() error { return useBackground(os.Getenv(backgroundEnv)) }

// useBackground applies one value, read once at startup. Unset leaves
// detection alone; a value that is neither light nor dark is an error
// rather than a silent fall back to the guess the override exists to
// escape.
func useBackground(v string) error {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
	case "light":
		lipgloss.SetHasDarkBackground(false)
	case "dark":
		lipgloss.SetHasDarkBackground(true)
	default:
		return fmt.Errorf("%s=%q: want \"light\" or \"dark\"", backgroundEnv, v)
	}
	return nil
}

var (
	styleTicket       = lipgloss.NewStyle().Bold(true)
	styleFocus        = lipgloss.NewStyle().Foreground(colorFocus)
	styleRunning      = lipgloss.NewStyle().Foreground(colorRunning)
	styleProvisioning = lipgloss.NewStyle().Foreground(colorProvisioning)
	styleAttention    = lipgloss.NewStyle().Foreground(colorAttention)
	styleFaint        = lipgloss.NewStyle().Foreground(colorFaint)
	styleErr          = lipgloss.NewStyle().Foreground(colorAttention)

	// styleMatch marks the spans a search matched inside a row. Underlined
	// as well as coloured, the rule the state dots already follow: the mark
	// has to survive a 16-colour terminal and a colour-blind operator.
	styleMatch = lipgloss.NewStyle().Foreground(colorFocus).Underline(true)
	// stylePlain is a row's unstyled text — what highlight renders the spans
	// it did not match with, where the cell carries no style of its own.
	stylePlain = lipgloss.NewStyle()

	// styleMark is the lerp mark in the status bar's corner. Weight, no
	// colour: the palette marks state, and the mark is the one thing on the
	// bar that reports nothing.
	styleMark = lipgloss.NewStyle().Bold(true)

	// styleWordmark is the empty-board decoration: colorWordmark and nothing
	// else, no weight — bold is how styleTicket and the mark's own splash
	// claim the operator's attention, and this is the one figure on screen
	// built to give none.
	styleWordmark = lipgloss.NewStyle().Foreground(colorWordmark)

	// styleSelected is the selection band and nothing else: no foreground,
	// so the row's own colours are what the operator still reads. The ▸
	// marker stays beside it — a terminal that renders the background
	// weakly, or not at all, would otherwise leave no cursor on screen.
	styleSelected = lipgloss.NewStyle().Background(colorSelected)

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
