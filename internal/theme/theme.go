package theme

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
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
// by being hard to read. Ticket is bold, so the ramp reads bold /
// normal / faint rather than normal / nearly-invisible.
var (
	// ColorFocus is terminal green: a muted, desaturated phosphor tone, not
	// the neon `#33FF33`-class hacker green — CRT-phosphor lineage without
	// the gnarliness. ColorRunning was already this family's other member,
	// which is why it moves to teal below rather than sitting two clicks
	// away in the same hue.
	ColorFocus = lipgloss.AdaptiveColor{Light: "#186B3E", Dark: "#6FCF97"}
	// ColorRunning moves to teal/cyan rather than staying green now that
	// ColorFocus is green too: "selected" and "alive" are the board's most
	// load-bearing distinction, and a hue split (green vs teal, ~45° apart)
	// holds under a glance in a way that pushing two greens apart in
	// lightness alone would not — a work row shows both at once, next to a
	// focused border, so the two states need to separate at a glance and
	// not just under a calibrated eye.
	ColorRunning      = lipgloss.AdaptiveColor{Light: "#0B6E85", Dark: "#22D3EE"}
	ColorProvisioning = lipgloss.AdaptiveColor{Light: "#9A5E07", Dark: "#F2B84B"}
	ColorAttention    = lipgloss.AdaptiveColor{Light: "#C4275B", Dark: "#F2618E"}
	// ColorFaint is neutralised rather than tinted: it draws every unfocused
	// panel border, sitting right beside the new focused-green one, so the
	// old violet cast (low-saturation, but a cast all the same) was the one
	// leftover a side-by-side comparison would still catch. True grey (no
	// hue at all) reads as neither colour and needs no design decision about
	// which one it leans toward. Picked at the same relative luminance as
	// the value it replaces, so every contrast number pinned elsewhere in
	// this file (faint against black/white, faint against ColorSelected's
	// band) still holds to the same precision.
	ColorFaint = lipgloss.AdaptiveColor{Light: "#6C6C6C", Dark: "#939393"}
	// ColorSelected is the band under the row the cursor is on. It is a
	// background, so it is not read — it is read *through*, by every colour
	// a row already carries. The tint is therefore the quietest one that
	// still finds itself across a panel, and it is priced against Faint,
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
	ColorSelected = lipgloss.CompleteAdaptiveColor{
		Light: lipgloss.CompleteColor{TrueColor: "#CCF0D8", ANSI256: "254"},
		Dark:  lipgloss.CompleteColor{TrueColor: "#17271C", ANSI256: "234"},
	}
	// ColorWordmark is the empty-board decoration (LERP-145), pinned below
	// ContrastFloor on purpose: WCAG exempts pure decoration from the
	// contrast rules outright, and this mark carries no information for
	// dimness to put at risk (rule 1 — decoration only, forever).
	// TestWordmarkIsExemptDecoration in theme_test.go is the carve-out
	// itself, scoped to this one name.
	//
	// Spelled out per profile like ColorSelected, and for the same reason:
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
	ColorWordmark = lipgloss.CompleteAdaptiveColor{
		Light: lipgloss.CompleteColor{TrueColor: "#C1CDC5", ANSI256: "251"},
		Dark:  lipgloss.CompleteColor{TrueColor: "#48514B", ANSI256: "239"},
	}
)

// ContrastFloor is the ratio every colour here has to clear against its
// backgrounds. WCAG asks 4.5:1 of text (1.4.3) and 3:1 of a graphic or a
// boundary that carries meaning (1.4.11); every colour in this palette
// renders text somewhere — faint draws the hint lines as well as the
// sparkline and the panel borders — so the stricter floor is the only one
// that binds, and one number covers both rules.
const ContrastFloor = 4.5

// Palette is every colour above in one list, so the contrast test can walk
// them. A colour added to the block above belongs here too, or nothing
// measures it — which the test checks, by name, against what this package
// declares.
var Palette = []struct {
	Name  string
	Color lipgloss.AdaptiveColor
}{
	{"ColorFocus", ColorFocus},
	{"ColorRunning", ColorRunning},
	{"ColorProvisioning", ColorProvisioning},
	{"ColorAttention", ColorAttention},
	{"ColorFaint", ColorFaint},
}

// BackgroundEnv lets an operator say which background their terminal has.
// lipgloss picks the Light or Dark variant from termenv's OSC 11 query, and
// a terminal that does not answer it — tmux, screen, plenty of ssh and CI
// terminals — falls back to black, so a light terminal silently gets the
// dark variants on white. This is the way out of that.
//
// An environment variable rather than a config key on purpose: lerp.toml
// describes the repo's pipeline, and this is one operator's terminal.
const BackgroundEnv = "LERP_BACKGROUND"

// UseBackground applies the override if the operator set one. Run calls it
// before it renders anything, so every way into the TUI gets it; main calls
// it at the top of startup as well, so a value lerp cannot read is refused
// before the board check and the clone lock rather than after them.
// Applying it twice is applying it once.
func UseBackground() error { return useBackground(os.Getenv(BackgroundEnv)) }

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
		return fmt.Errorf("%s=%q: want \"light\" or \"dark\"", BackgroundEnv, v)
	}
	return nil
}

var (
	Ticket       = lipgloss.NewStyle().Bold(true)
	Focus        = lipgloss.NewStyle().Foreground(ColorFocus)
	Running      = lipgloss.NewStyle().Foreground(ColorRunning)
	Provisioning = lipgloss.NewStyle().Foreground(ColorProvisioning)
	Attention    = lipgloss.NewStyle().Foreground(ColorAttention)
	Faint        = lipgloss.NewStyle().Foreground(ColorFaint)
	Err          = lipgloss.NewStyle().Foreground(ColorAttention)

	// Match marks the spans a search matched inside a row. Underlined
	// as well as coloured, the rule the state dots already follow: the mark
	// has to survive a 16-colour terminal and a colour-blind operator.
	Match = lipgloss.NewStyle().Foreground(ColorFocus).Underline(true)
	// Plain is a row's unstyled text — what highlight renders the spans
	// it did not match with, where the cell carries no style of its own.
	Plain = lipgloss.NewStyle()

	// Mark is the lerp mark in the status bar's corner. Weight, no
	// colour: the palette marks state, and the mark is the one thing on the
	// bar that reports nothing.
	Mark = lipgloss.NewStyle().Bold(true)

	// Wordmark is the empty-board decoration: ColorWordmark and nothing
	// else, no weight — bold is how Ticket and the mark's own splash
	// claim the operator's attention, and this is the one figure on screen
	// built to give none.
	Wordmark = lipgloss.NewStyle().Foreground(ColorWordmark)

	// Selected is the selection band and nothing else: no foreground,
	// so the row's own colours are what the operator still reads. The ▸
	// marker stays beside it — a terminal that renders the background
	// weakly, or not at all, would otherwise leave no cursor on screen.
	Selected = lipgloss.NewStyle().Background(ColorSelected)

	Border      = lipgloss.NewStyle().Foreground(ColorFaint)
	BorderFocus = lipgloss.NewStyle().Foreground(ColorFocus)
	TitleFocus  = lipgloss.NewStyle().Foreground(ColorFocus).Bold(true)
)
