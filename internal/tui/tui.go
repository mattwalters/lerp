package tui

import (
	"context"
	"fmt"
	"io"
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
	// Before anything renders: the palette's light and dark variants are
	// chosen per render from what lipgloss believes the background is, and
	// this is the operator's say in that belief (see theme.go).
	if err := UseBackground(); err != nil {
		return err
	}
	if err := o.Validate(); err != nil {
		return err
	}
	m := newModel(ctx, o)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithFPS(30))
	_, err := p.Run()
	awaitPasses(os.Stderr, m.passes, o.Events, quitWait)
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
// bound is quitWait; it and the writer are arguments only so a test can hold
// the give-up path to a duration it can afford to wait out.
func awaitPasses(w io.Writer, passes *sync.WaitGroup, events <-chan loop.Event, bound time.Duration) {
	done := make(chan struct{})
	go func() { passes.Wait(); close(done) }()
	// Armed once, outside the loop. Re-arming it on every drained event would
	// let a pass with a steady stream to emit push the bound out forever —
	// an unbounded stall, which is worse than the 30s one this fixes.
	deadline := time.After(bound)
	for {
		select {
		case <-done:
			return
		case _, ok := <-events:
			// A closed channel is ready forever, so re-selecting on it would
			// spin a core until the deadline. Nothing closes this one today;
			// waitEvent guards the same case. A nil channel blocks, which is
			// what the wait wants once there is nothing left to drain.
			if !ok {
				events = nil
			}
		case <-deadline:
			fmt.Fprintf(w, "lerp: gave up waiting for the in-flight pass after %s\n", bound)
			return
		}
	}
}
