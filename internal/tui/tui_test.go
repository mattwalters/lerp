package tui

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mattwalters/lerp/internal/loop"
)

// A pass that emits more events than the channel buffers must still be able
// to finish once the screen is closed. Nothing reads the channel after the
// program exits, so without a drain the pass blocks in emit, awaitPasses
// waits out the whole quitWait, and quitting hangs the terminal for 30s.
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
	go func() { awaitPasses(&passes, events); close(returned) }()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatalf("awaitPasses did not return: the pass is wedged emitting into a channel nobody drains")
	}
	// Returning is not enough: draining must not become a way to leave
	// without waiting. The pass has to have run to the end first.
	if !finished.Load() {
		t.Fatal("awaitPasses returned while the pass was still in flight")
	}
}

// The wait ends when the pass does, and not before: quitting must never sever
// a pass mid-mutation.
func TestAwaitPassesWaitsForThePassToFinish(t *testing.T) {
	events := make(chan loop.Event, 1)
	var passes sync.WaitGroup
	var finished atomic.Bool
	passes.Add(1)
	go func() {
		time.Sleep(10 * time.Millisecond)
		finished.Store(true)
		passes.Done()
	}()

	start := time.Now()
	awaitPasses(&passes, events)
	if !finished.Load() {
		t.Fatal("awaitPasses returned before the pass was done")
	}
	if elapsed := time.Since(start); elapsed >= quitWait {
		t.Fatalf("awaitPasses took %s; it should return as soon as the pass is done", elapsed)
	}
}

// A channel closed under it — nothing does that today, but waitEvent already
// treats closure as possible — must not turn the wait into a spin: a closed
// channel is ready forever, and re-selecting on it would burn a core until
// the deadline, which is the stall LERP-84 is about wearing another hat.
func TestAwaitPassesSurvivesAClosedEventsChannel(t *testing.T) {
	events := make(chan loop.Event)
	close(events)
	var passes sync.WaitGroup
	var finished atomic.Bool
	passes.Add(1)
	go func() {
		time.Sleep(10 * time.Millisecond)
		finished.Store(true)
		passes.Done()
	}()

	awaitPasses(&passes, events)
	if !finished.Load() {
		t.Fatal("awaitPasses returned before the pass was done")
	}
}
