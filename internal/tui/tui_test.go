package tui

import (
	"sync"
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
	passes.Add(1)
	go func() {
		defer passes.Done()
		for i := 0; i < 10; i++ {
			events <- loop.Event{Type: loop.EventQueues}
		}
	}()

	returned := make(chan struct{})
	go func() { awaitPasses(&passes, events); close(returned) }()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatalf("awaitPasses did not return: the pass is wedged emitting into a channel nobody drains")
	}
}

// The wait ends when the pass does, without waiting on anything else.
func TestAwaitPassesReturnsWhenThePassFinishes(t *testing.T) {
	events := make(chan loop.Event, 1)
	var passes sync.WaitGroup
	passes.Add(1)
	go func() {
		time.Sleep(10 * time.Millisecond)
		passes.Done()
	}()

	start := time.Now()
	awaitPasses(&passes, events)
	if elapsed := time.Since(start); elapsed >= quitWait {
		t.Fatalf("awaitPasses took %s; it should return as soon as the pass is done", elapsed)
	}
}
