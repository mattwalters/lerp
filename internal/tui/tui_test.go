package tui

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mattwalters/lerp/internal/loop"
)

// A pass that emits more events than the channel buffers must still be able
// to finish once the screen is closed. Nothing reads the channel after the
// program exits, so without a drain the pass blocks in emit, awaitPasses
// waits out the whole bound, and quitting hangs the terminal for 30s.
func TestAwaitPassesDrainsEventsSoAFullChannelCannotWedgeAPass(t *testing.T) {
	// Deliberately smaller than the pass is about to emit — the production
	// buffer is 64 and the wedge is the same shape at any size.
	events := make(chan loop.Event, 2)
	var passes sync.WaitGroup
	var finished atomic.Bool
	passes.Add(1)
	go func() {
		defer passes.Done()
		for i := 0; i < 10; i++ {
			events <- loop.Event{Type: loop.EventQueues}
		}
		finished.Store(true)
	}()

	returned := make(chan struct{})
	go func() {
		awaitPasses(io.Discard, &passes, events, quitWait)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("awaitPasses did not return: the pass is wedged emitting into a channel nobody drains")
	}
	// Returning is not enough: draining must not become a way to leave
	// without waiting. Quitting never severs a pass mid-mutation, so the
	// pass has to have run to the end first.
	if !finished.Load() {
		t.Fatal("awaitPasses returned while the pass was still in flight")
	}
}

// The bound is absolute. A pass with a steady stream to emit gets drained,
// but draining must not re-arm the deadline event by event — that would turn
// the stall this fixes into one with no end at all.
func TestAwaitPassesGivesUpOnTheBoundNoMatterHowChattyThePassIs(t *testing.T) {
	const bound = 50 * time.Millisecond
	events := make(chan loop.Event)
	stop := make(chan struct{})
	defer close(stop)
	var passes sync.WaitGroup
	passes.Add(1)
	// Never finishes while the test is watching: this is the wedged pass the
	// bound exists for, and it always has one more event to offer.
	go func() {
		defer passes.Done()
		for {
			select {
			case events <- loop.Event{Type: loop.EventQueues}:
			case <-stop:
				return
			}
		}
	}()

	var note bytes.Buffer
	returned := make(chan struct{})
	go func() {
		awaitPasses(&note, &passes, events, bound)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatalf("awaitPasses ran way past its %s bound: a drained event is pushing the deadline out", bound)
	}
	if got := note.String(); !strings.Contains(got, "gave up waiting") {
		t.Fatalf("awaitPasses gave up without telling the operator the pass may still be mutating; wrote %q", got)
	}
}
