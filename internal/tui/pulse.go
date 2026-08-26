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
	// them — about what a wide terminal's full-width row has the columns
	// for, and further back than the question ("has it been sitting there
	// for four minutes") is ever asked over.
	//
	// A row draws the tail of the ring that fits the width it is given (see
	// runLine), so the ring is one number for every layout rather than one
	// per panel, and a row narrower than the ring reaches less far back
	// rather than covering the same span more coarsely. sparkMinCells is
	// where a line stops being worth its columns — a row with less room
	// than that for history it has keeps its numbers and drops the line. A
	// run too young to fill it still draws what it has, the way window's
	// own short line does.
	sparkBucket   = 15 * time.Second
	sparkCells    = 60
	sparkMinCells = 8
)

// sparkBars is the ramp, lowest first. Index 0 is an empty bucket, so a
// stretch with no activity in it reads as a flat line along the bottom.
var sparkBars = []rune("▁▂▃▄▅▆▇█")

const (
	// unreadBucket stands in a window for a bucket that passed before this
	// process attached to the log — history no count exists for. Counts are
	// never negative, so it cannot collide with one.
	unreadBucket = -1
	// sparkUnread draws such a bucket. It sits off the ramp deliberately: a
	// bar of any height would be a count nobody made, and the floor bar is
	// already the reading for "watched, and nothing happened". This one says
	// only "older than I have watched it", and a dot is distinct from every
	// bar at a glance rather than by height.
	sparkUnread = '·'
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
	// now filling, at is when it opened, and seen is how many buckets have
	// existed at all — a young run draws a short line rather than a long
	// empty one, which would be the picture of a run that had died.
	cells [sparkCells]int
	head  int
	at    time.Time
	seen  int
	// unread is how many buckets passed between the run starting and this
	// pulse attaching, capped at the ring. They carry no counts — there are
	// none to be had — and window draws them ahead of the ring, so an
	// adopted run reads as older than the stretch it has been watched for.
	unread int
	// tokens is what the run has spent, summed from the usage its log
	// reports; tool and target are the last tool call it made, which is the
	// most concrete answer the log has to what it is doing. Both are of the
	// stretch this pulse has read: unread above says whether there is a
	// stretch before it, and the row says so too rather than passing a
	// partial total off as the run's.
	tokens       int
	tool, target string
}

// newPulse attaches at the end of the file. What is already in it happened at
// times the pulse cannot know, and counting it into the bucket now filling
// would draw a burst that never happened.
//
// started is when the run began, and only a run inherited from a previous
// process began before now (see readPulses). That span is history this pulse
// has no counts for and no way to get any, so it is marked unread rather than
// left out: a line that merely started short is the line of a run that just
// started, which is the one thing the row must not say about an hour-old
// agent.
func newPulse(path string, started, now time.Time) *pulse {
	p := &pulse{follower: newFollower(path, 0)}
	if !started.IsZero() {
		p.unread = min(sparkCells, max(0, int(now.Sub(started)/sparkBucket)))
	}
	return p
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
		//
		// The unread span survives: it says this pulse has no counts for
		// what came before, which a rewrite has just made true again of
		// everything it had. It is not a rescue — a rewrite costs the
		// reading either way, and a short span leaves a short line — it is
		// that the span is still the truth about the run after one.
		p.stream, p.cells, p.head, p.seen = logfmt.Stream{}, [sparkCells]int{}, 0, 0
		p.at = time.Time{}
		// The total and the last call went with the file: both are readings
		// of a log that no longer exists, and the one still on screen would
		// be a command from a run nobody can look at any more.
		p.tokens, p.tool, p.target = 0, "", ""
	}
	if mid {
		p.stream.SkipLine()
	}
	p.roll(now)
	for _, ev := range p.stream.Feed(b) {
		p.cells[p.head]++
		p.tokens += ev.Usage
		if ev.Kind == logfmt.KindToolCall {
			p.tool, p.target = ev.Tool, ev.Text
		}
	}
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
// is the order a sparkline draws, led by the run's unread span — the buckets
// that passed before this pulse attached. It is the whole history the ring
// holds; a row too narrow for all of it draws the tail, which is the recent
// end. It is short while a run is young: a line that has not had time to fall
// is not a line that has fallen, and a run picked up ten seconds ago must not
// read like one that has been quiet since the ring began.
func (p *pulse) window() []int {
	if p.heard.IsZero() {
		// No log behind the ring: it may not exist yet, or it may have been
		// deleted under a live agent, which invariant 1 allows. Neither is a
		// run that has gone quiet, and a flat line would say it was.
		return nil
	}
	// The unread span gives way to the ring as the ring fills: what this
	// pulse did watch is never dropped to keep saying it was not watching.
	u := min(p.unread, sparkCells-p.seen)
	out := make([]int, 0, u+p.seen)
	for i := 0; i < u; i++ {
		out = append(out, unreadBucket)
	}
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
// A long line makes that the ordinary reading rather than the rare one: a
// burst at the start of a run holds the scale for as long as it stays in the
// window, and steady work under it sits on the bar above the floor. Alive
// rather than how alive is what the row is for, with the call beside it.
//
// A bucket marked unread is drawn as itself and takes no part in the scale:
// it is a span nobody counted, not a quiet one, and letting it read as either
// a bar or the floor would be the fresh-start line the marking exists to
// prevent.
func sparkline(counts []int) string {
	hi := 0
	for _, c := range counts {
		hi = max(hi, c)
	}
	var b strings.Builder
	for _, c := range counts {
		if c == unreadBucket {
			b.WriteRune(sparkUnread)
			continue
		}
		bar := 0
		if c > 0 {
			bar = max(1, c*(len(sparkBars)-1)/hi)
		}
		b.WriteRune(sparkBars[bar])
	}
	return b.String()
}
