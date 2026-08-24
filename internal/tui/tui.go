package tui

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// quitWait bounds how long quitting waits for an in-flight reconciliation
// pass. A pass wedged on a hung network call must not hold the terminal
// hostage; past the bound the caller's cleanup proceeds and the pass finishes
// on its own — or dies with the process.
const quitWait = 30 * time.Second

// Run opens the TUI and drives the loop until the operator quits. ctx is the
// loop's context, deliberately independent of the program's lifetime:
// quitting closes the screen, stops future ticks, and waits — bounded by
// quitWait — for a pass already in flight to finish, so the caller may
// release the clone lock and close the loop log the moment Run returns. The
// agents themselves are never touched: they are their own process groups with
// run evidence on disk, so the next lerp adopts them (SCOPE invariant 3 —
// everything is safe to kill, including lerp).
func Run(ctx context.Context, o Options) error {
	if err := o.validate(); err != nil {
		return err
	}
	m := newModel(ctx, o)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	awaitPasses(m.passes)
	return err
}

// awaitPasses blocks until no reconciliation pass is in flight, giving up
// after quitWait with a note: the operator should know the pass may still be
// mutating when the process exits.
func awaitPasses(passes *sync.WaitGroup) {
	done := make(chan struct{})
	go func() { passes.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(quitWait):
		fmt.Fprintf(os.Stderr, "lerp: gave up waiting for the in-flight pass after %s\n", quitWait)
	}
}
