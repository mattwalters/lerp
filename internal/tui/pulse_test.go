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
	p := newPulse(path, now, now)
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
	p := newPulse(path, now, now)
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

	now := time.Now()
	p := newPulse(path, now, now)
	p.read(now)
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
	p := newPulse(path, start, start)
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
	now := time.Now()
	p := newPulse(path, now, now)
	p.read(now)
	if p.heard.IsZero() || p.heard.Sub(old).Abs() > time.Second {
		t.Fatalf("heard = %v, want the file's mtime %v", p.heard, old)
	}
}

// A log that is not there yet is not a run that has gone quiet: the row shows
// no reading rather than a made-up one.
func TestPulseWithoutAFileHasNothingToSay(t *testing.T) {
	now := time.Now()
	p := newPulse(filepath.Join(t.TempDir(), "not-yet"), now, now)
	p.read(now)
	p.read(now.Add(time.Minute))
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
	now := time.Now()
	p := newPulse(path, now, now)
	p.read(now)
	if p.heard.IsZero() {
		t.Fatal("a log that is there reports no time")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	p.read(now)
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
	p := newPulse(path, start, start)
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

// Done-when: a run adopted from a previous process must not read as one that
// just started. The span between the run starting and this process attaching
// is drawn as unread — history no reading exists for — rather than left off
// the line, which is how an hour-old agent came to draw the short line of a
// ten-second-old one.
func TestPulseMarksTheSpanItNeverWatched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	appendLog(t, path, strings.Repeat("an hour of work\n", 200))
	now := time.Now()
	p := newPulse(path, now.Add(-time.Hour), now)
	p.read(now)

	got := p.window()
	if len(got) != sparkCells {
		t.Fatalf("an hour-old run draws %d buckets, want the whole ring of %d", len(got), sparkCells)
	}
	for i, c := range got[:len(got)-1] {
		if c != unreadBucket {
			t.Fatalf("bucket %d of a span nobody counted holds %d: %v", i, c, got)
		}
	}
	bars := sparkline(got)
	fresh := sparkline(func() []int {
		q := newPulse(path, now, now)
		q.read(now)
		return q.window()
	}())
	if bars == fresh {
		t.Fatalf("the adopted run draws %q, the same line as a run that just started", bars)
	}
	if !strings.HasPrefix(bars, string(sparkUnread)) || !strings.ContainsRune(bars, sparkBars[0]) {
		t.Fatalf("the line does not read as unread history followed by a watched bucket: %q", bars)
	}
}

// The unread span is not a permanent header: it gives way bucket by bucket to
// what the pulse has actually watched, so a run adopted ten minutes ago
// eventually draws a line of nothing but its own reading.
func TestPulseUnreadGivesWayToWhatItWatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	appendLog(t, path, "adopted mid-run\n")
	start := time.Now()
	p := newPulse(path, start.Add(-time.Hour), start)
	p.read(start)

	appendLog(t, path, "still working\n")
	p.read(start.Add(10 * sparkBucket))
	got := p.window()
	if len(got) != sparkCells {
		t.Fatalf("window = %d buckets, want the ring's %d", len(got), sparkCells)
	}
	// Eleven buckets have existed under this pulse; the rest is what it
	// never saw, and the unread span shortens by exactly what it watched.
	if watched := 11; got[len(got)-watched] == unreadBucket || got[len(got)-watched-1] != unreadBucket {
		t.Fatalf("the unread span did not give way after %d buckets: %v", watched, got)
	}

	p.read(start.Add(time.Hour))
	for i, c := range p.window() {
		if c == unreadBucket {
			t.Fatalf("bucket %d still reads unread after the ring filled: %v", i, p.window())
		}
	}
}

// A rewrite takes the counts, never the unread span. The counts describe a
// file that is gone; "this pulse has no reading for what came before" is what
// the rewrite has just made true again of everything it held, and clearing it
// alongside them would hand an adopted run back the line of one that just
// started.
func TestPulseKeepsItsUnreadSpanWhenTheLogIsRewritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	appendLog(t, path, strings.Repeat("an hour of work\n", 200))
	start := time.Now()
	p := newPulse(path, start.Add(-time.Hour), start)
	p.read(start)
	p.read(start.Add(sparkBucket))

	// Shorter than what it replaces, which is how a follower knows a file
	// was rewritten rather than appended to.
	writeLog(t, path, []byte("a wholly new log\n"))
	p.read(start.Add(2 * sparkBucket))
	got := p.window()
	if len(got) != sparkCells {
		t.Fatalf("after the rewrite the run draws %d buckets, want the ring's %d: %v",
			len(got), sparkCells, got)
	}
	if got[0] != unreadBucket {
		t.Fatalf("the rewrite took the unread span with the counts: %v", got)
	}
	if got[len(got)-1] != 1 {
		t.Fatalf("the new log's line did not land in the bucket now filling: %v", got)
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
		// A bucket from before the pulse attached is neither a count nor a
		// quiet stretch, so it draws as neither: off the ramp entirely,
		// while the buckets that were counted keep the window's own scale.
		{"unwatched history is not a quiet stretch",
			[]int{unreadBucket, unreadBucket, 1, 0, 4}, "··▂▁█"},
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
	p := newPulse(path, start, start)
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

// The row's two other readings come off the same events the buckets are
// counted from: what the run has spent, summed as the log reports it, and the
// last call it made, which stays until another one replaces it. A run does
// not stop having run a command while it thinks about the output.
func TestPulseTracksSpendAndTheLastCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	appendLog(t, path, `{"type":"system","subtype":"init","model":"claude-opus-5","session_id":"abc"}`+"\n")
	now := time.Now()
	p := newPulse(path, now, now)
	p.read(now)
	if p.tokens != 0 || p.tool != "" {
		t.Fatalf("a run that has done nothing reports %d tokens and %q", p.tokens, p.tool)
	}

	appendLog(t, path,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/a/b.go"}}],`+
			`"usage":{"input_tokens":100,"output_tokens":900}}}`+"\n")
	p.read(now)
	if p.tokens != 1000 {
		t.Fatalf("the first call spent %d tokens, want 1000", p.tokens)
	}
	if p.tool != "Read" || p.target != "b.go" {
		t.Fatalf("the last call is %q %q, want Read b.go", p.tool, p.target)
	}

	// Prose costs tokens and is not a call: the total moves, the call does
	// not.
	appendLog(t, path,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"that file is fine"}],`+
			`"usage":{"input_tokens":500,"output_tokens":10,"cache_read_input_tokens":1490}}}`+"\n")
	p.read(now)
	if p.tokens != 3000 {
		t.Fatalf("the run has spent %d tokens, want 3000", p.tokens)
	}
	if p.tool != "Read" {
		t.Fatalf("prose replaced the last call with %q", p.tool)
	}

	// A rewritten log takes both with it: they are readings of a file that
	// is gone, and the command on screen would be one nobody can look up.
	writeLog(t, path, []byte("a wholly new log\n"))
	p.read(now.Add(sparkBucket))
	if p.tokens != 0 || p.tool != "" || p.target != "" {
		t.Fatalf("the rewritten log kept %d tokens and the call %q %q", p.tokens, p.tool, p.target)
	}
}

// The row's figure is a sum of what the log reports, so a runner that repeats
// one call's usage on every line of it inflates the row directly: Claude Code
// writes a content block per line, and a thinking-then-tool call read 3x.
func TestPulseBillsOneCallOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	appendLog(t, path, `{"type":"system","subtype":"init","model":"claude-opus-5","session_id":"abc"}`+"\n")
	now := time.Now()
	p := newPulse(path, now, now)
	p.read(now) // attach at the end of what is already there

	// Three lines of one call, each repeating the same 1,000-token usage.
	const usage = `"usage":{"input_tokens":100,"output_tokens":900}`
	appendLog(t, path,
		`{"type":"assistant","message":{"id":"msg_01","content":[{"type":"thinking","thinking":"weighing it"}],`+usage+`}}`+"\n"+
			`{"type":"assistant","message":{"id":"msg_01","content":[{"type":"text","text":"reading it"}],`+usage+`}}`+"\n"+
			`{"type":"assistant","message":{"id":"msg_01","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/a/b.go"}}],`+usage+`}}`+"\n")
	p.read(now)
	if p.tokens != 1000 {
		t.Fatalf("one call across three lines billed %d tokens, want 1000", p.tokens)
	}
	if p.tool != "Read" || p.target != "b.go" {
		t.Fatalf("the last call is %q %q, want Read b.go", p.tool, p.target)
	}
}
