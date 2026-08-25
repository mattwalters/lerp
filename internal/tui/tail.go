package tui

import (
	"bytes"
	"os"
	"time"
)

const (
	// tailScrollback bounds how much of a lane's log the board keeps in
	// memory. The firehose is ephemeral by design (SCOPE invariant 7): the
	// operator wants the recent tail, not an archive.
	tailScrollback = 64 * 1024
	// tailChunk bounds one poll's read, so a burst of agent output costs a
	// few frames instead of stalling one.
	tailChunk = 64 * 1024
)

// follower reads one log file by polling: each call hands back the bytes
// appended since the last one. Polling is the design — no watcher machinery
// for a file that one other local process appends to.
//
// The file disappearing is not an error. A run's log is deleted with its run
// record, and an empty path means there is no log at all; in both cases the
// follower simply reports nothing new.
type follower struct {
	path   string
	back   int64  // bytes of history the first read attaches with
	offset int64  // next byte to read; negative until the first read
	chunk  []byte // scratch for reads, reused across polls
	// mod is the file's modification time as of the last poll, zero while
	// the file cannot be read. It is the file's own answer to when it last
	// grew, which a reader that attached a moment ago has no other way to
	// know.
	mod time.Time
}

func newFollower(path string, back int64) follower {
	return follower{path: path, back: back, offset: -1}
}

// next returns the bytes appended since the last call, nil when the file has
// not grown. The slice is the scratch buffer, which the next call overwrites:
// a caller that keeps the bytes must copy them.
//
// mid reports that the read starts partway through a line — a reader that
// attaches into the middle of a file finds whatever the writer was in the
// middle of — and reset that the file shrank,
// so it was truncated and rewritten and whatever a caller built from it
// belongs to the old file. A file the same size is assumed unchanged: log
// files are append-only per run and paths are never reused, so a same-size
// rewrite cannot happen.
func (f *follower) next() (b []byte, mid, reset bool) {
	if f.path == "" {
		return nil, false, false
	}
	// Stat first: most polls find nothing new, and every poll paying an open
	// for that answer would be waste.
	info, err := os.Stat(f.path)
	if err != nil {
		return nil, false, false
	}
	f.mod = info.ModTime()
	size := info.Size()
	switch {
	case f.offset < 0:
		f.offset = max(0, size-f.back)
		// Attaching at a line boundary is not attaching mid-line, and a
		// reader that attaches at the end of a log is normally at one:
		// costing it its next whole line would be a real event lost.
		mid = f.offset > 0 && !f.closesLine(f.offset)
	case size < f.offset:
		f.offset, reset = 0, true
	}
	if size <= f.offset {
		return nil, mid, reset
	}
	file, err := os.Open(f.path)
	if err != nil {
		return nil, mid, reset
	}
	defer file.Close()
	if f.chunk == nil {
		f.chunk = make([]byte, tailChunk)
	}
	n, _ := file.ReadAt(f.chunk[:min(size-f.offset, tailChunk)], f.offset)
	if n == 0 {
		return nil, mid, reset
	}
	f.offset += int64(n)
	return f.chunk[:n], mid, reset
}

// closesLine reports whether the byte before off ends a line — how a reader
// about to attach at off tells a line it will see whole from the tail of one
// somebody else started. One read, once, when the follower attaches.
func (f *follower) closesLine(off int64) bool {
	file, err := os.Open(f.path)
	if err != nil {
		return false
	}
	defer file.Close()
	var b [1]byte
	if _, err := file.ReadAt(b[:], off-1); err != nil {
		return false
	}
	return b[0] == '\n'
}

// tail follows one log file for the main pane: whatever was appended since
// last time, kept as a bounded scrollback.
type tail struct {
	follower
	buf  []byte
	view logView // the same bytes, decoded as agent activity
}

func newTail(path string) tail {
	return tail{follower: newFollower(path, tailScrollback)}
}

// read pulls newly appended bytes into the scrollback and reports whether the
// buffer changed.
func (t *tail) read() bool {
	b, mid, reset := t.follower.next()
	if reset {
		t.buf, t.view = t.buf[:0], logView{}
	}
	if mid {
		// Attaching to a log already being written lands mid-line.
		t.view.skipLine()
	}
	if len(b) == 0 {
		return false
	}
	t.buf = append(t.buf, b...)
	t.trim()
	// The decoder sees every byte the tail reads, once, as it arrives: it is
	// the one thing here that cannot be rebuilt from the trimmed scrollback.
	t.view.feed(b)
	return true
}

// trim drops the oldest bytes once the scrollback overflows, cutting at a
// line boundary so the top of the pane is never half a line.
func (t *tail) trim() {
	if len(t.buf) <= tailScrollback {
		return
	}
	cut := len(t.buf) - tailScrollback
	if i := bytes.IndexByte(t.buf[cut:], '\n'); i >= 0 {
		cut += i + 1
	}
	n := copy(t.buf, t.buf[cut:])
	t.buf = t.buf[:n]
}

// content is the raw scrollback, byte for byte as the runner wrote it — what
// the raw toggle shows, and the floor everything else is measured against.
func (t *tail) content() string {
	return string(t.buf)
}

// rendered is the same log read as agent activity, laid out for a pane this
// wide.
func (t *tail) rendered(width int) string {
	return t.view.render(width)
}
