package tui

import "github.com/charmbracelet/lipgloss"

// The lerp mark, in the two sizes the TUI draws it at. One definition, so
// the tool has one wordmark rather than two that drift: markWord is the mark
// small — the plain word, which is what a status bar's corner has room for —
// and markLines is the same word large, in ASCII, for the splash below.
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
func (m model) splash() string {
	mark := markBlock
	// The figure is fixed-size, where every other view here is built to the
	// width it is given. A window with no room for it gets the small mark
	// instead: half a wordmark reads as a rendering bug, which is the one
	// thing this screen exists to rule out.
	if m.width < lipgloss.Width(markBlock) || m.height < lipgloss.Height(markBlock)+2 {
		mark = markWord
	}
	spinner := styleRunning.Render(heartbeatFrames[m.frame%len(heartbeatFrames)])
	fig := lipgloss.JoinVertical(lipgloss.Center, mark, "", spinner)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, fig)
}
