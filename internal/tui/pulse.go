package tui

import (
	"strings"
	"time"

	"github.com/mattwalters/lerp/internal/logfmt"
)

const (
	// sparkBucket is how long one bucket covers and sparkCells how many the
	// ring holds: fifteen seconds each, in cells narrow enough that a run
	// falling quiet shows within a bucket or two, and fifteen minutes of
	// them — as far back as the widest row has the columns to draw.
	//
	// A row draws the tail of that ring that fits the width it is given
	// (see runLine), so the ring is sized for the widest row rather than
	// for one layout, and a narrow panel costs history rather than
	// resolution. sparkMinCells is the narrowest line still worth drawing:
	// under that the row keeps its numbers and drops the line.
	sparkBucket   = 15 * time.Second
	sparkCells    = 60
	sparkMinCells = 8
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
	// now filling, at is when it opened, and seen is how many buckets have
	// existed at all — a young run draws a short line rather than a long
	// empty one, which would be the picture of a run that had died.
	cells [sparkCells]int
	head  int
	at    time.Time
	seen  int
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
	// The file's own time, or none at all: a log that is gone is not a log
	// that has fallen quiet, and reporting the last time it was there would
	// read as an agent still being watched.
	p.heard = p.mod
	if p.heard.IsZero() {
		// There is no file to read: a lane still provisioning is given its
		// log path before the runner creates it, and a log may be deleted
		// under a live agent. Rolling the ring against a file that is not
		// there would draw the flat line of a run that had stopped.
		return
	}
	if reset {
		// The file was rewritten under us, so the counts are a picture of a
		// log that is gone — and folding a whole new file into the bucket
		// now filling would draw one spike and flatten every real one beside
		// it. Start the reading over with the file.
		p.stream, p.cells, p.head, p.seen = logfmt.Stream{}, [sparkCells]int{}, 0, 0
		p.at = time.Time{}
	}
	if mid {
		p.stream.SkipLine()
	}
	p.roll(now)
	p.cells[p.head] += len(p.stream.Feed(b))
}

// roll advances the ring to the bucket now falls in, zeroing the ones that
// passed. Time moves the ring even when no byte arrives, which is what lets a
// run that has gone quiet flatten its own line.
func (p *pulse) roll(now time.Time) {
	if p.at.IsZero() {
		p.at, p.seen = now, 1
		return
	}
	steps := int(now.Sub(p.at) / sparkBucket)
	if steps <= 0 {
		return
	}
	p.seen = min(p.seen+steps, sparkCells)
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

// window is the counts of the buckets that have existed, oldest first, which
// is the order a sparkline draws. It is the whole history the ring holds; a
// row too narrow for all of it draws the tail, which is the recent end. It is
// short while a run is young: a line that has not had time to fall is not a
// line that has fallen, and a run picked up ten seconds ago must not read
// like one that stopped two minutes ago.
func (p *pulse) window() []int {
	if p.heard.IsZero() {
		// No log behind the ring: it may not exist yet, or it may have been
		// deleted under a live agent, which invariant 1 allows. Neither is a
		// run that has gone quiet, and a flat line would say it was.
		return nil
	}
	out := make([]int, 0, p.seen)
	for i := sparkCells - p.seen; i < sparkCells; i++ {
		out = append(out, p.cells[(p.head+1+i)%sparkCells])
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
