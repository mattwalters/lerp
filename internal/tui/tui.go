package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
)

// Run opens the TUI and drives the loop until the operator quits. ctx is the
// loop's context, deliberately independent of the program's lifetime: quitting
// stops the ticking and closes the screen, and nothing more. The agents are
// their own process groups with run evidence on disk, so the next lerp adopts
// them (SCOPE invariant 3 — everything is safe to kill, including lerp).
func Run(ctx context.Context, o Options) error {
	if err := o.validate(); err != nil {
		return err
	}
	p := tea.NewProgram(newModel(ctx, o), tea.WithAltScreen())
	_, err := p.Run()
	return err
}
