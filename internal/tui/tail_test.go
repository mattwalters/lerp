package tui

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLog(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTailFollowsAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	writeLog(t, path, []byte("first\n"))

	tl := newTail(path)
	if !tl.read() {
		t.Fatal("first read reported no change")
	}
	if got := tl.content(); got != "first\n" {
		t.Fatalf("content = %q, want %q", got, "first\n")
	}
	if tl.read() {
		t.Fatal("read with nothing appended reported a change")
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("second\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if !tl.read() {
		t.Fatal("read after append reported no change")
	}
	if got := tl.content(); got != "first\nsecond\n" {
		t.Fatalf("content = %q, want %q", got, "first\nsecond\n")
	}
}

func TestTailAttachesNearTheEndOfABigLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	big := bytes.Repeat([]byte("noise\n"), tailScrollback/2)
	writeLog(t, path, append(big, []byte("the recent part\n")...))

	tl := newTail(path)
	tl.read()
	content := tl.content()
	if len(content) > tailScrollback {
		t.Fatalf("scrollback holds %d bytes, cap is %d", len(content), tailScrollback)
	}
	if !strings.HasSuffix(content, "the recent part\n") {
		t.Fatalf("content does not end with the file's tail: %q", content[len(content)-40:])
	}
}

func TestTailRestartsOnTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	writeLog(t, path, []byte("old contents that will vanish\n"))
	tl := newTail(path)
	tl.read()

	writeLog(t, path, []byte("new\n"))
	if !tl.read() {
		t.Fatal("read after truncation reported no change")
	}
	if got := tl.content(); got != "new\n" {
		t.Fatalf("content = %q, want %q", got, "new\n")
	}
}

func TestTailToleratesAMissingFile(t *testing.T) {
	tl := newTail(filepath.Join(t.TempDir(), "not-yet"))
	if tl.read() {
		t.Fatal("read of a missing file reported a change")
	}
	if tl.content() != "" {
		t.Fatal("missing file produced content")
	}
	empty := newTail("")
	if empty.read() {
		t.Fatal("read with no path reported a change")
	}
}

func TestTailTrimsAtLineBoundaries(t *testing.T) {
	line := strings.Repeat("x", 99) + "\n"
	tl := tail{buf: []byte("fragment" + strings.Repeat(line, tailScrollback/100+1))}
	tl.trim()
	if len(tl.buf) > tailScrollback {
		t.Fatalf("scrollback holds %d bytes, cap is %d", len(tl.buf), tailScrollback)
	}
	// Every kept line must be whole: the first one is a full 99-byte line,
	// never the tail of a longer fragment.
	first, _, _ := strings.Cut(tl.content(), "\n")
	if len(first) != 99 {
		t.Fatalf("first kept line has %d bytes, want a whole 99-byte line", len(first))
	}
}

// Attaching partway into a file lands mid-line only when it actually lands
// mid-line. Dropping to the next newline regardless would cost a reader that
// attached at the end of a finished line the whole next line — for the pulse,
// a real event; for the pane, a row that was never garbled to begin with.
func TestFollowerSkipsAFragmentAndOnlyAFragment(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name    string
		already string
		wantMid bool
	}{
		{"attached on a line boundary", "a whole line\n", false},
		{"attached mid-line", "half a line", true},
		{"attached at the very start", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+".log")
			writeLog(t, path, []byte(tc.already))
			f := newFollower(path, 0)
			_, mid, _ := f.next()
			if mid != tc.wantMid {
				t.Fatalf("attaching after %q reported mid=%v, want %v", tc.already, mid, tc.wantMid)
			}
			appendLog(t, path, "the next line\n")
			b, _, _ := f.next()
			if string(b) != "the next line\n" {
				t.Fatalf("read %q, want the appended line", b)
			}
		})
	}
}
