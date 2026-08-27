package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The lerp mark, in the two sizes the TUI draws it at, so the tool has one
// wordmark rather than two that drift: markWord is the mark small — the
// plain word the status bar carries in its corner (see statusBar) — and
// markLines is the same word large, in ASCII, for the splash below. The
// corner mark landed first, as LERP-102; this is the size that came second,
// and taking the word from here rather than spelling it again is the whole
// of what keeps them one mark.
const markWord = "lerp"

var markLines = []string{
	` _`,
	`| | ___  _ _  _ __`,
	`| |/ -_)| '_|| '_ \`,
	`|_|\___||_|  | .__/`,
	`             |_|`,
}

// markBlock is the large mark as one block: the lines padded to a common
// width, so centring the figure never centres its lines against each other
// and shears the letters apart.
var markBlock = lipgloss.JoinVertical(lipgloss.Left, markLines...)

// splash is the screen lerp opens on, and the whole of it: the mark, a
// spinner under it, nothing else. It stands in for the board until the first
// pass has something to say (see splashing) — two empty panels and a status
// bar with nothing on it look the same whether lerp is working or wedged,
// and this is the first thing anyone ever sees of the tool.
//
// The spinner rides the same frame counter and the same frames as the status
// bar's heartbeat: one clock, and one shape for "lerp is working on it".
// The figure is fixed-size, where every other view here is built to the
// width it is given — so the block has to fit the smallest window View will
// draw a board in at all, which is what TestTheMarkFitsTheSmallestBoard
// holds it to. Below that size there is no splash to draw: the too-small
// screen has already taken the frame, and it is the actionable one.
func (m model) splash() string {
	spinner := styleRunning.Render(heartbeatFrames[m.frame%len(heartbeatFrames)])
	fig := lipgloss.JoinVertical(lipgloss.Center, markBlock, "", spinner)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, fig)
}

// wordmarkMargin is the clear space rule 3 (LERP-145) holds around the empty-
// board decoration before it is allowed on screen: it is decoration, so
// touching the inbox panel's own text or its border would read as a
// rendering glitch rather than as a watermark.
const wordmarkMargin = 4

// wordmarkFits reports whether a box w columns by ih rows has room for the
// mark plus its margin on every side. The mark is the one fixed-size figure
// drawn inside a panel here, so this is the guard that keeps it whole or off
// — never clipped, the way TestTheMarkFitsTheSmallestBoard already holds the
// splash to for the screen it owns outright.
func wordmarkFits(w, ih int) bool {
	return ih >= len(markLines)+2*wordmarkMargin && w >= lipgloss.Width(markBlock)+2*wordmarkMargin
}

// wordmarkPanel is the inbox panel's body when the board is empty and the
// mark fits (see model.boardEmpty): the same figure the splash draws, dimmed
// to colorWordmark's named exemption from the contrast floor, centred in the
// room the panel has. Static — no spinner, unlike the splash — because rule
// 4 reserves motion for the startup screen and this is a different element
// with a different job.
func wordmarkPanel(w, ih int) []string {
	block := lipgloss.Place(w, ih, lipgloss.Center, lipgloss.Center, styleWordmark.Render(markBlock))
	return strings.Split(block, "\n")
}

// wordmarkVisible reports whether r would actually dim the mark rather than
// draw it bare. NO_COLOR, and any profile termenv downgrades to no colour,
// turns every style in this package to plain text (TestNoColorLeavesTheTextBare)
// — harmless for the palette, which still carries real information at full
// brightness, but wrong for a figure whose only claim to being decoration is
// being too dim to read. Full-brightness ASCII art the width of the panel is
// not a watermark, it is a wall of characters, so this is asked alongside
// wordmarkFits rather than left to degrade like everything else here.
func wordmarkVisible(r *lipgloss.Renderer) bool {
	return styleWordmark.Renderer(r).Render("x") != "x"
}
