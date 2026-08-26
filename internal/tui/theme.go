package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
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
	colorFocus        = lipgloss.AdaptiveColor{Light: "#6E4BC7", Dark: "#A78BFA"}
	colorRunning      = lipgloss.AdaptiveColor{Light: "#127A57", Dark: "#3DDC97"}
	colorProvisioning = lipgloss.AdaptiveColor{Light: "#9A5E07", Dark: "#F2B84B"}
	colorAttention    = lipgloss.AdaptiveColor{Light: "#C4275B", Dark: "#F2618E"}
	colorFaint        = lipgloss.AdaptiveColor{Light: "#6E697C", Dark: "#9490A9"}
	// colorSelected is the band under the row the cursor is on. It is a
	// background, so it is not read — it is read *through*, by every colour
	// a row already carries. The tint is therefore the quietest one that
	// still finds itself across a panel, and it is priced against styleFaint,
	// the combination with the least to spare: faint keeps 2.84:1 on the dark
	// band against 3.86:1 on black, and 3.14:1 on the light one against
	// 3.90:1 on white. Adaptive, because the same tint that reads as a band
	// on a dark terminal is a smudge on a light one.
	colorSelected = lipgloss.AdaptiveColor{Light: "#E9E4F7", Dark: "#272138"}
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
		b.WriteString(border.Render(bd.Left) + lp + padTo(row, cw) + rp + border.Render(bd.Right))
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
// bandOpen is the sequence that turns the selection background on, and ""
// on a profile that has no band to draw. Two of them do not: an uncoloured
// one has nothing to turn on, and a 16-colour one has nothing quiet enough —
// lipgloss quantises this tint to a solid ANSI blue or magenta, which is a
// bar across the row rather than a band under it, and it takes the row's
// faint cells down to about 1.3:1 with it. ▸ is the cursor on those, which
// is what it is the fallback for.
func bandOpen() string {
	if lipgloss.ColorProfile() > termenv.ANSI256 {
		return ""
	}
	return strings.TrimSuffix(styleSelected.Render(""), ansiReset)
}

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
	more := func(n int) string { return styleFaint.Render(fmt.Sprintf("⋯ %d more", n)) }
	if end <= ih-1 {
		return append(append([]string{}, rows[:ih-1]...), more(len(rows)-(ih-1)))
	}
	if lo := len(rows) - (ih - 1); at >= lo {
		lo = skipContinuation(cur, lo)
		return append([]string{more(lo)}, rows[lo:]...)
	}
	// The window ends just past the selected row, so a row drawing several
	// lines keeps all of them. Where it cannot — a body of three lines has
	// one to spend — it starts at the row instead: the line the cursor is on
	// is the one that has to be there.
	n := max(1, ih-2)
	lo := skipContinuation(cur, max(0, min(at, end-n)))
	hi := min(len(rows), lo+n)
	out := append([]string{more(lo)}, rows[lo:hi]...)
	if len(out) < ih {
		out = append(out, more(len(rows)-hi))
	}
	return out
}
