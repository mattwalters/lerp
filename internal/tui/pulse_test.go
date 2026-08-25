package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newest is the bucket now filling, which is where a poll's events land.
func newest(t *testing.T, p *pulse) int {
	t.Helper()
	w := p.window()
	if len(w) == 0 {
		t.Fatal("the window has no buckets at all")
	}
	return w[len(w)-1]
}

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
	if got := newest(t, p); got != 0 {
		t.Fatalf("a half-written line counted as %d events", got)
	}
	appendLog(t, path, "line\n")
	p.read(now)
	if got := newest(t, p); got != 1 {
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
	if len(got) != 3 {
		t.Fatalf("window = %v, want the three buckets that have existed", got)
	}
	if got[1] != 0 || got[2] != 0 {
		t.Fatalf("two quiet buckets still show activity: %v", got)
	}
	if got[0] == 0 {
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
	p.read(time.Now().Add(time.Minute))
	if !p.heard.IsZero() {
		t.Fatalf("a missing log claimed it was last heard at %v", p.heard)
	}
	if bars := sparkline(p.window()); bars != "" {
		t.Fatalf("a log that does not exist drew a line: %q", bars)
	}
}

// A log deleted under a live agent — which invariant 1 says may cost compute,
// never correctness — is not an agent that has gone quiet. The row stops
// reporting rather than freezing the last time the file was there.
func TestPulseStopsReadingALogThatVanished(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	appendLog(t, path, "working\n")
	p := newPulse(path)
	p.read(time.Now())
	if p.heard.IsZero() {
		t.Fatal("a log that is there reports no time")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	p.read(time.Now())
	if !p.heard.IsZero() {
		t.Fatalf("a deleted log still reports %v as when it last spoke", p.heard)
	}
	if bars := sparkline(p.window()); bars != "" {
		t.Fatalf("a deleted log still draws a line: %q", bars)
	}
}

// A run picked up seconds ago has no history to draw, and a long flat line is
// the picture of one that died. The window grows with the run instead.
func TestPulseWindowGrowsWithTheRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	appendLog(t, path, "started\n")
	start := time.Now()
	p := newPulse(path)
	p.read(start)
	if got := len(p.window()); got != 1 {
		t.Fatalf("a run one poll old draws %d buckets, want 1", got)
	}
	p.read(start.Add(3 * sparkBucket))
	if got := len(p.window()); got != 4 {
		t.Fatalf("a run four buckets old draws %d, want 4", got)
	}
	p.read(start.Add(time.Hour))
	if got := len(p.window()); got != sparkCells {
		t.Fatalf("an old run draws %d buckets, want the whole window of %d", got, sparkCells)
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

// A log rewritten in place is a new log. Folding what was already in it into
// the bucket now filling would draw one spike and flatten every real bucket
// beside it, since the bars scale to the window's own tallest.
func TestPulseStartsOverWhenTheLogIsRewritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	appendLog(t, path, "one\ntwo\nthree\n")
	start := time.Now()
	p := newPulse(path)
	p.read(start)
	appendLog(t, path, "four\nfive\n")
	p.read(start.Add(sparkBucket))
	if got := len(p.window()); got != 2 {
		t.Fatalf("window is %d buckets before the rewrite, want 2", got)
	}

	writeLog(t, path, []byte("a wholly new log\n"))
	p.read(start.Add(2 * sparkBucket))
	got := p.window()
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("the rewritten log carries the old ring: %v", got)
	}
}
