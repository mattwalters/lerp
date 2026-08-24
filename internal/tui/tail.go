package tui

import (
	"bytes"
	"os"
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

// tail follows one log file by polling: read whatever was appended since last
// time, keep a bounded scrollback. Polling is the design — no watcher
// machinery for a file that one other local process appends to.
//
// The file disappearing is not an error. A run's log is deleted with its run
// record, and an empty path means the selected lane has no log at all; in
// both cases the buffer simply stops growing.
type tail struct {
	path   string
	offset int64 // next byte to read; negative until the first read
	buf    []byte
}

func newTail(path string) tail {
	return tail{path: path, offset: -1}
}

// read pulls newly appended bytes into the scrollback and reports whether the
// buffer changed. The first read attaches near the end of an existing file; a
// file that shrank was truncated and rewritten, so the tail starts over.
func (t *tail) read() bool {
	if t.path == "" {
		return false
	}
	f, err := os.Open(t.path)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false
	}
	size := info.Size()
	switch {
	case t.offset < 0:
		t.offset = max(0, size-tailScrollback)
	case size < t.offset:
		t.offset = 0
		t.buf = nil
	}
	if size <= t.offset {
		return false
	}
	chunk := make([]byte, min(size-t.offset, tailChunk))
	n, _ := f.ReadAt(chunk, t.offset)
	if n == 0 {
		return false
	}
	t.offset += int64(n)
	t.buf = append(t.buf, chunk[:n]...)
	t.trim()
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
	t.buf = append([]byte(nil), t.buf[cut:]...)
}

func (t *tail) content() string {
	return string(t.buf)
}
