package tui

import (
	"math"
	"os"
	"time"

	ntsparkline "github.com/NimbleMarkets/ntcharts/sparkline"
	"github.com/mattwalters/lerp/internal/logfmt"
)

const (
	// pulseBucket is how long one fine bucket covers: three seconds, fine
	// enough for braille resolution across the lane pane to show bursts and
	// pauses without stepping on pollEvery (250ms), and an exact divisor of
	// the row's 15-second cell.
	//
	// pulseBuckets is how many buckets the ring holds: 300 buckets (15
	// minutes), matching the quarter-hour span the row reaches back over
	// and covering up to a 300-column pane with no allocation.
	pulseBucket  = 3 * time.Second
	pulseBuckets = 300

	// sparkBucket is how long one bucket covers in the work row and sparkCells
	// how many the row holds: fifteen seconds each, in cells narrow enough
	// that a run falling quiet shows within a bucket or two, and fifteen
	// minutes of them (60 cells).
	//
	// sparkMinCells is where a line stops being worth its columns — a row
	// with less room than that for history it has keeps its numbers and
	// drops the line.
	sparkBucket   = 15 * time.Second
	sparkCells    = 60
	sparkMinCells = 8

	// catchupChunks bounds how many chunks of history one poll reads while
	// the pulse is behind the log — behind only just after attaching, since
	// a live agent writes slower than one chunk a poll. Thirty-two chunks is
	// two megabytes: several times the log a run leaves after minutes of
	// work, so adoption usually catches up in one poll, and small enough
	// that a monstrous log costs a few polls instead of one long stall.
	catchupChunks = 32
)

// pulse is one lane's log read for what its work row says about the run in
// progress: how much activity it has carried lately, what it has spent, and
// the last call it made. All of it comes out of the log the pane already
// tails, so it degrades the way logfmt degrades — whatever a runner's adapter
// can count, it counts, and an unrecognized stream counts lines and reports
// no tokens at all. Nothing is persisted anywhere; the log is ephemeral
// evidence (SCOPE invariant 1) and so is this.
//
// This is display, not hang detection. SCOPE defers hang detection, and
// nothing here holds a threshold, names a state, or acts on a number: the
// operator reads the row and decides whether to eject.
type pulse struct {
	follower
	stream logfmt.Stream
	// heard is when the log last grew, taken from the file's own mtime — so
	// a row is right on the first poll, including for a run adopted from a
	// previous process, rather than only once a byte arrives. It is also
	// what tells a lane with a log from one whose runner has not made the
	// file yet, which is whether the row has a second line to draw at all.
	heard time.Time
	// cells is a ring of event counts, one per bucket; head is the bucket
	// now filling, at is when it opened, and seen is how many buckets the
	// reading covers — a young run draws a short line rather than a long
	// empty one, which would be the picture of a run that had died.
	cells [pulseBuckets]int
	head  int
	at    time.Time
	seen  int
	// hist is how much of the log predates this pulse, and consumed how much
	// it has read; together they say whether a chunk is history being caught
	// up on or the live stream. The two are read differently: a live event
	// happened just now and lands in the bucket now filling, while a
	// historical one happened whenever its line says (lastT carrying the
	// last dated line's time over the undated lines between, which follow
	// it closely), and one whose line says nothing has no place in the ring
	// at all — dating it to now would draw a burst that never happened.
	hist     int64
	consumed int64
	lastT    time.Time
	// tokens is what the run has spent, summed from the usage its log
	// reports; cost is the same sum over dollars, zero for a runner whose
	// stream never states one; tool and target are the last call it made,
	// which is the most concrete answer the log has to what it is doing.
	// History counts into all four the same as the live stream: the log
	// records the whole run, so a run adopted mid-way reports the run's own
	// total, not the tail of it this process happened to watch.
	tokens       int
	cost         float64
	tool, target string
	model        string
	// context is the worst live agent's latest context reading, zero until
	// the stream reports one. Unlike tokens and cost it is not summed: a
	// later reading simply replaces the one before it, the same as it does
	// inside logfmt's own decoder.
	context int
}

// newPulse attaches at the start of the file and reads the run's whole log,
// history included: the log records what the run did and — where the runner
// dates its lines — when, so a run adopted from a previous process draws the
// history it actually has. A log that does not date its lines contributes its
// history to the totals but not to the ring, and the line grows from the
// attach the way a fresh run's does: short, never a shape nobody measured.
func newPulse(path string) *pulse {
	p := &pulse{follower: newFollower(path, math.MaxInt64)}
	// What the file holds now is history; what arrives after is the live
	// stream. A log that is not there yet has no history at all.
	if info, err := os.Stat(path); err == nil {
		p.hist = info.Size()
	}
	return p
}

// read takes one poll's worth of log — or, while the pulse is still behind
// the history it attached over, up to catchupChunks of it, so adoption
// converges in a poll or two instead of trickling in. now is passed in rather
// than read here so a caller polling several lanes buckets them all against
// one clock.
func (p *pulse) read(now time.Time) {
	for i := 0; i < catchupChunks; i++ {
		b, mid, reset := p.follower.next()
		// The file's own time, or none at all: a log that is gone is not a
		// log that has fallen quiet, and reporting the last time it was
		// there would read as an agent still being watched.
		p.heard = p.mod
		if p.heard.IsZero() {
			// There is no file to read: a lane still provisioning is given
			// its log path before the runner creates it, and a log may be
			// deleted under a live agent. Rolling the ring against a file
			// that is not there would draw the flat line of a run that had
			// stopped.
			return
		}
		if reset {
			// The file was rewritten under us, so the counts are a picture
			// of a log that is gone. Start the reading over with the file —
			// all of which is new, none of it history.
			p.stream, p.cells, p.head, p.seen = logfmt.Stream{}, [pulseBuckets]int{}, 0, 0
			p.at, p.lastT = time.Time{}, time.Time{}
			p.hist, p.consumed = 0, 0
			// The totals and the last call went with the file: all are
			// readings of a log that no longer exists, and the one still on
			// screen would be a command from a run nobody can look at any
			// more.
			p.tokens, p.cost, p.tool, p.target, p.context, p.model = 0, 0, "", "", 0, ""
		}
		if mid {
			p.stream.SkipLine()
		}
		p.roll(now)
		history := p.consumed < p.hist
		p.consumed += int64(len(b))
		for _, ev := range p.stream.Feed(b) {
			p.place(ev, history)
			p.tokens += ev.Usage
			p.cost += ev.Cost
			if ev.Context > 0 {
				p.context = ev.Context
			}
			if ev.Model != "" {
				p.model = ev.Model
			}
			if ev.Kind == logfmt.KindToolCall {
				p.tool, p.target = ev.Tool, ev.Text
			}
		}
		if len(b) == 0 || !history {
			return
		}
	}
}

// place counts one event into the ring. A live event happened just now, in
// the bucket now filling. A historical one happened when its line says — an
// undated line between dated ones is read at the last dated time, which it
// follows closely — and history from before the log's first dated line, or
// from a log that dates nothing, is left out of the ring entirely: the
// totals still carry it, but a bar needs a time and there is none to be had.
//
// A historical event extends the reading back to where it happened even when
// it is too old for the ring to hold a count: an adopted run whose log went
// quiet twenty minutes ago must draw the long flat line of a run that has
// been quiet, not the short line of one that just started.
func (p *pulse) place(ev logfmt.Event, history bool) {
	if !history {
		p.cells[p.head]++
		return
	}
	t := ev.Time
	if t.IsZero() {
		t = p.lastT
	} else {
		p.lastT = t
	}
	if t.IsZero() {
		return
	}
	back := 0
	if d := p.at.Sub(t); d > 0 {
		back = 1 + int((d-time.Nanosecond)/pulseBucket)
	}
	p.seen = max(p.seen, min(back, pulseBuckets-1)+1)
	if back >= pulseBuckets {
		return
	}
	p.cells[((p.head-back)%pulseBuckets+pulseBuckets)%pulseBuckets]++
}

// roll advances the ring to the bucket now falls in, zeroing the ones that
// passed. Time moves the ring even when no byte arrives, which is what lets a
// run that has gone quiet flatten its own line.
func (p *pulse) roll(now time.Time) {
	if p.at.IsZero() {
		p.at, p.seen = now, 1
		return
	}
	steps := int(now.Sub(p.at) / pulseBucket)
	if steps <= 0 {
		return
	}
	p.seen = min(p.seen+steps, pulseBuckets)
	if steps >= pulseBuckets {
		p.cells, p.head, p.at = [pulseBuckets]int{}, 0, now
		return
	}
	for i := 0; i < steps; i++ {
		p.head = (p.head + 1) % pulseBuckets
		p.cells[p.head] = 0
	}
	p.at = p.at.Add(time.Duration(steps) * pulseBucket)
}

// timedBucket is one window bucket with the time it covers.
type timedBucket struct {
	at    time.Time
	count int
}

// timedWindow is window() with each bucket dated: bucket i of a window of n
// closed at p.at - (n-1-i)*pulseBucket.
func (p *pulse) timedWindow() []timedBucket {
	w := p.window()
	if len(w) == 0 {
		return nil
	}
	out := make([]timedBucket, len(w))
	for i, c := range w {
		t := p.at.Add(-time.Duration(len(w)-1-i) * pulseBucket)
		out[i] = timedBucket{at: t, count: c}
	}
	return out
}

// window is the counts of the buckets the reading covers, oldest first, which
// is the order a sparkline draws. It is the whole history the ring holds; a
// row too narrow for all of it draws the tail, which is the recent end. It is
// short while the reading is: a line that has not had time to fall is not a
// line that has fallen, and a run picked up ten seconds ago whose log dates
// nothing must not read like one that has been quiet since the ring began.
func (p *pulse) window() []int {
	if p.heard.IsZero() {
		// No log behind the ring: it may not exist yet, or it may have been
		// deleted under a live agent, which invariant 1 allows. Neither is a
		// run that has gone quiet, and a flat line would say it was.
		return nil
	}
	out := make([]int, 0, p.seen)
	for i := pulseBuckets - p.seen; i < pulseBuckets; i++ {
		out = append(out, p.cells[(p.head+1+i)%pulseBuckets])
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
// A long line makes that the ordinary reading rather than the rare one: a
// burst at the start of a run holds the scale for as long as it stays in the
// window, and steady work under it sits on the bar above the floor. Alive
// rather than how alive is what the row is for, with the call beside it.
func sparkline(counts []int) string {
	if len(counts) == 0 {
		return ""
	}
	hi := 0
	for _, c := range counts {
		hi = max(hi, c)
	}
	data := make([]float64, len(counts))
	for i, c := range counts {
		if c == 0 {
			data[i] = 1.0 / 8
		} else {
			data[i] = max(2.0/8, float64(c)/float64(hi))
		}
	}

	s := ntsparkline.New(len(counts), 1)
	s.SetMax(1)
	s.PushAll(data)
	s.Draw()
	return s.View()
}

// downsample aggregates fine pulse buckets into 15-second sparkline cells.
// The most recent fine buckets form the rightmost spark cell, so the recent
// edge is always whole.
func downsample(counts []int) []int {
	if len(counts) == 0 {
		return nil
	}
	ratio := int(sparkBucket / pulseBucket)
	if ratio <= 1 {
		return counts
	}
	cells := (len(counts) + ratio - 1) / ratio
	out := make([]int, cells)
	for i := 0; i < cells; i++ {
		end := len(counts) - (cells-1-i)*ratio
		start := max(0, end-ratio)
		sum := 0
		for _, c := range counts[start:end] {
			sum += c
		}
		out[i] = sum
	}
	return out
}
