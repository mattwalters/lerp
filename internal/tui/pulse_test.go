package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func appendLog(t *testing.T, path, data string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(data); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// The count is of decoded events, not bytes: a runner logfmt understands is
// counted by what its agent did, and one it does not is counted by lines.
func TestPulseCountsDecodedEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	appendLog(t, path, `{"type":"system","subtype":"init","model":"claude-opus-5","session_id":"abc"}`+"\n")

	now := time.Now()
	p := newPulse(path)
	p.read(now)
	// Three lines, one of them a tool result the pane draws under its call:
	// three events, because three things happened.
	appendLog(t, path,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/a/b.go"}}]}}`+"\n"+
			`{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}`+"\n"+
			`{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}`+"\n")
	p.read(now)

	got := p.window()
	if got[len(got)-1] != 3 {
		t.Fatalf("newest bucket = %d, want 3: %v", got[len(got)-1], got)
	}
	for _, c := range got[:len(got)-1] {
		if c != 0 {
			t.Fatalf("activity landed outside the bucket it arrived in: %v", got)
		}
	}
}

// A half-written line is not an event yet. The pane holds it back and so does
// the count, or a burst would be counted once as a fragment and again whole.
func TestPulseWaitsForAWholeLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	appendLog(t, path, "first line\n")
	now := time.Now()
	p := newPulse(path)
	p.read(now)

	appendLog(t, path, "half a ")
	p.read(now)
	if got := p.window()[sparkCells-1]; got != 0 {
		t.Fatalf("a half-written line counted as %d events", got)
	}
	appendLog(t, path, "line\n")
	p.read(now)
	if got := p.window()[sparkCells-1]; got != 1 {
		t.Fatalf("the finished line counted as %d events, want 1", got)
	}
}

// Attaching reads the file's end: what a run wrote before this process
// started watching happened at times the pulse cannot know, and dating it to
// now would draw a burst that never happened.
func TestPulseAttachesAtTheEndOfAnExistingLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	appendLog(t, path, strings.Repeat("an hour of work\n", 200))

	p := newPulse(path)
	p.read(time.Now())
	for _, c := range p.window() {
		if c != 0 {
			t.Fatalf("history it never saw was counted as activity: %v", p.window())
		}
	}
}

// Time moves the ring, not just bytes: a run that falls quiet flattens its
// own line while nothing at all is being read.
func TestPulseQuietGoesFlat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	appendLog(t, path, "x\n")
	start := time.Now()
	p := newPulse(path)
	p.read(start)
	appendLog(t, path, "busy\nbusy\n")
	p.read(start)

	p.read(start.Add(2 * sparkBucket))
	got := p.window()
	if got[sparkCells-1] != 0 || got[sparkCells-2] != 0 {
		t.Fatalf("two quiet buckets still show activity: %v", got)
	}
	if got[sparkCells-3] == 0 {
		t.Fatalf("the busy bucket did not slide back with the clock: %v", got)
	}
	if bars := sparkline(got); !strings.HasSuffix(bars, "▁▁") {
		t.Fatalf("a quiet stretch does not read flat: %q", bars)
	}

	// Quiet for longer than the whole window leaves nothing to draw.
	p.read(start.Add(time.Hour))
	if bars := sparkline(p.window()); bars != strings.Repeat("▁", sparkCells) {
		t.Fatalf("a long-dead run does not read flat: %q", bars)
	}
}

// Last-heard-from is the file's own mtime, so it is right on the first poll —
// including for a run adopted from a previous process, where waiting for a
// byte of our own could mean waiting forever.
func TestPulseHeardIsTheFilesOwnTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	appendLog(t, path, "wrote this a while ago\n")
	old := time.Now().Add(-7 * time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	p := newPulse(path)
	p.read(time.Now())
	if p.heard.IsZero() || p.heard.Sub(old).Abs() > time.Second {
		t.Fatalf("heard = %v, want the file's mtime %v", p.heard, old)
	}
}

// A log that is not there yet is not a run that has gone quiet: the row shows
// no reading rather than a made-up one.
func TestPulseWithoutAFileHasNothingToSay(t *testing.T) {
	p := newPulse(filepath.Join(t.TempDir(), "not-yet"))
	p.read(time.Now())
	if !p.heard.IsZero() {
		t.Fatalf("a missing log claimed it was last heard at %v", p.heard)
	}
	if bars := sparkline(p.window()); bars != strings.Repeat("▁", sparkCells) {
		t.Fatalf("a missing log drew activity: %q", bars)
	}
}

func TestSparkline(t *testing.T) {
	tests := []struct {
		name   string
		counts []int
		want   string
	}{
		{"nothing at all", []int{0, 0, 0}, "▁▁▁"},
		{"the busiest bucket tops the ramp", []int{0, 4, 0}, "▁█▁"},
		// The scale is the row's own busiest bucket, so a steady rate reads
		// as a level line: the shape is the signal, and bar heights are not
		// comparable between one row and the next.
		{"a steady rate reads level", []int{4, 4, 4}, "███"},
		{"the rest scale under the peak", []int{1, 5, 9}, "▂▄█"},
		{"one event never reads as none", []int{1, 0, 20}, "▂▁█"},
		{"no window, no line", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sparkline(tc.counts); got != tc.want {
				t.Fatalf("sparkline(%v) = %q, want %q", tc.counts, got, tc.want)
			}
		})
	}
}
