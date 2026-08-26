package tui

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mattwalters/lerp/internal/loop"
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
	if err := o.Validate(); err != nil {
		return err
	}
	m := newModel(ctx, o)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	awaitPasses(m.passes, o.Events)
	return err
}

// awaitPasses blocks until no reconciliation pass is in flight, giving up
// after quitWait with a note: the operator should know the pass may still be
// mutating when the process exits.
//
// It keeps draining events while it waits. Nothing renders them any more —
// the screen is closed — but the loop emits into a buffered channel, and once
// the buffer fills the emitting pass blocks forever on a receiver that has
// gone. A pass with more to say than the buffer holds (a wave of adoptions or
// reaps, plus the queue and attention payloads) would then always take the
// full quitWait, so quit would hang for 30s and then claim the pass was slow.
// Discarding is the point: the events are screen detail, and the evidence a
// later lerp reads is on disk.
func awaitPasses(passes *sync.WaitGroup, events <-chan loop.Event) {
	done := make(chan struct{})
	go func() { passes.Wait(); close(done) }()
	// Outside the loop: a deadline re-armed on every drained event would let
	// a chatty pass push the bound out indefinitely.
	deadline := time.After(quitWait)
	for {
		select {
		case <-done:
			return
		case <-events:
		case <-deadline:
			fmt.Fprintf(os.Stderr, "lerp: gave up waiting for the in-flight pass after %s\n", quitWait)
			return
		}
	}
}
