package tui

import "github.com/charmbracelet/lipgloss"

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
