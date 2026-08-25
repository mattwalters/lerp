package tui

import (
	"strings"
	"time"

	"github.com/mattwalters/lerp/internal/logfmt"
)

const (
	// sparkCells is how many buckets a row's sparkline draws and sparkBucket
	// how long each covers: two minutes of history, which is the span the
	// question behind this — "has it been sitting there for four minutes" —
	// is asked over, in cells narrow enough that a run falling quiet shows
	// within a poll or two.
	sparkCells  = 12
	sparkBucket = 10 * time.Second
)

// sparkBars is the ramp, lowest first. Index 0 is an empty bucket, so a
// stretch with no activity in it reads as a flat line along the bottom.
var sparkBars = []rune("▁▂▃▄▅▆▇█")

// pulse is one lane's log read for what its work row says about the run in
// progress: when the log last grew, and how much activity it has carried
// lately. Both come out of the log the pane already tails, so they degrade
// the way logfmt degrades — whatever a runner's adapter can count, it counts,
// and an unrecognized stream counts lines. Nothing is persisted anywhere; the
// log is ephemeral evidence (SCOPE invariant 1) and so is this.
//
// This is display, not hang detection. SCOPE defers hang detection, and
// nothing here holds a threshold, names a state, or acts on a number: the
// operator reads the row and decides whether to eject.
type pulse struct {
	follower
	stream logfmt.Stream
	// heard is when the log last grew, taken from the file's own mtime — so
	// a row is right on the first poll, including for a run adopted from a
	// previous process, rather than only once a byte arrives.
	heard time.Time
	// cells is a ring of event counts, one per bucket; head is the bucket
	// now filling and at is when it opened.
	cells [sparkCells]int
	head  int
	at    time.Time
}

// newPulse attaches at the end of the file. What is already in it happened at
// times the pulse cannot know, and counting it into the bucket now filling
// would draw a burst that never happened.
func newPulse(path string) *pulse {
	return &pulse{follower: newFollower(path, 0)}
}

// read takes one poll's worth of log. now is passed in rather than read here
// so a caller polling several lanes buckets them all against one clock.
func (p *pulse) read(now time.Time) {
	b, mid, reset := p.follower.next()
	if reset {
		p.stream = logfmt.Stream{}
	}
	if mid {
		p.stream.SkipLine()
	}
	if !p.mod.IsZero() {
		p.heard = p.mod
	}
	p.roll(now)
	p.cells[p.head] += len(p.stream.Feed(b))
}

// roll advances the ring to the bucket now falls in, zeroing the ones that
// passed. Time moves the ring even when no byte arrives, which is what lets a
// run that has gone quiet flatten its own line.
func (p *pulse) roll(now time.Time) {
	if p.at.IsZero() {
		p.at = now
		return
	}
	steps := int(now.Sub(p.at) / sparkBucket)
	if steps <= 0 {
		return
	}
	if steps >= sparkCells {
		p.cells, p.head, p.at = [sparkCells]int{}, 0, now
		return
	}
	for i := 0; i < steps; i++ {
		p.head = (p.head + 1) % sparkCells
		p.cells[p.head] = 0
	}
	p.at = p.at.Add(time.Duration(steps) * sparkBucket)
}

// window is the counts oldest first, which is the order a sparkline draws.
func (p *pulse) window() []int {
	out := make([]int, sparkCells)
	for i := range out {
		out[i] = p.cells[(p.head+1+i)%sparkCells]
	}
	return out
}

// sparkline draws counts as bars, the busiest bucket in the window at the top
// of the ramp and the rest scaled under it. The scale is the row's own, not a
// fixed rate, because the question is shape rather than magnitude — whether
// the agent is doing something and whether it stopped — and because what
// counts as busy differs by runner and by what the agent is doing. The cost
// is that bar heights do not compare between one row and the next; the flat
// line does, which is the reading the row is for.
//
// An empty bucket is always the floor bar and any activity at all clears it,
// so one event is visibly not none however busy the rest of the window is.
func sparkline(counts []int) string {
	hi := 0
	for _, c := range counts {
		hi = max(hi, c)
	}
	var b strings.Builder
	for _, c := range counts {
		bar := 0
		if c > 0 {
			bar = max(1, c*(len(sparkBars)-1)/hi)
		}
		b.WriteRune(sparkBars[bar])
	}
	return b.String()
}
