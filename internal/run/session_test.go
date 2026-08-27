//go:build unix

package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/evidence"
)

// codexRunner is a vendor-resolved runner whose adapter names its own
// session — the shape resolveSession has to read a log for.
func codexRunner(t *testing.T) config.Runner {
	t.Helper()
	c, err := config.ParseRepoConfig(`
teams = ["LERP"]
provision = "p"
dispose = "d"

[runners.codex]
vendor = "codex"

[queues.implement]
status = "Implementing"
prompt = "p {{ticket}}"
runner = "codex"
on_success = "Done"
`, "test")
	if err != nil {
		t.Fatal(err)
	}
	return c.Runners["codex"]
}

func writeLog(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "run.log")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The session-naming event can arrive after other lines — logfmt's own
// sniffWindow establishes that a couple of lines can precede the first real
// event on the same descriptor — and resolveSession has to keep reading past
// them rather than judging the log by its first line alone.
func TestResolveSessionReadsThreadStartedMidStream(t *testing.T) {
	logPath := writeLog(t,
		`{"type":"turn.started"}`,
		`{"type":"thread.started","thread_id":"01a03575-0a83-7601-bdcc-1a734ee2b1b2"}`,
		`{"type":"item.completed"}`,
	)
	id, err := resolveSession(codexRunner(t), evidence.Record{RunID: "run", LogPath: logPath})
	if err != nil {
		t.Fatal(err)
	}
	if want := "01a03575-0a83-7601-bdcc-1a734ee2b1b2"; id != want {
		t.Errorf("resolveSession = %q, want %q", id, want)
	}
}

// A run that died before its thread id ever appeared must fail plainly,
// rather than resume handing back a command with an empty session.
func TestResolveSessionErrorsWhenTheLogNeverNamesOne(t *testing.T) {
	logPath := writeLog(t, `{"type":"turn.started"}`, `{"type":"error","message":"boom"}`)
	_, err := resolveSession(codexRunner(t), evidence.Record{RunID: "run-1", LogPath: logPath})
	if err == nil || !strings.Contains(err.Error(), "run-1") || !strings.Contains(err.Error(), "never reported a session id") {
		t.Fatalf("resolveSession error = %v, want one naming run-1 and no reported session", err)
	}
}

// A run that died before its log was ever written reads the same as one
// whose log said nothing: there is no session on disk either way.
func TestResolveSessionErrorsWhenTheLogDoesNotExist(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "never-written.log")
	_, err := resolveSession(codexRunner(t), evidence.Record{RunID: "run-2", LogPath: logPath})
	if err == nil || !strings.Contains(err.Error(), "never reported a session id") {
		t.Fatalf("resolveSession error = %v, want a no-session error", err)
	}
}

// bufio.Reader, not Scanner: an agent log can put a whole tool result on one
// line, well past a Scanner's default token size, and the scan must not die
// on it before reaching the session id that follows.
func TestResolveSessionSurvivesALineOverTheScannerBudget(t *testing.T) {
	huge := `{"type":"item.completed","item":{"text":"` + strings.Repeat("x", 128*1024) + `"}}`
	logPath := writeLog(t, huge, `{"type":"thread.started","thread_id":"01a03575-0a83-7601-bdcc-1a734ee2b1b2"}`)
	id, err := resolveSession(codexRunner(t), evidence.Record{RunID: "run", LogPath: logPath})
	if err != nil {
		t.Fatal(err)
	}
	if want := "01a03575-0a83-7601-bdcc-1a734ee2b1b2"; id != want {
		t.Errorf("resolveSession = %q, want %q", id, want)
	}
}

// A session named past sessionScanBudget lines in is indistinguishable from
// one that was never reported at all: the scan gives up before reaching it,
// and resolveSession's error reads the same as the died-early case. This
// pins that trade-off rather than leaving the budget's far edge unexercised.
func TestResolveSessionGivesUpAfterTheScanBudget(t *testing.T) {
	lines := make([]string, sessionScanBudget)
	for i := range lines {
		lines[i] = `{"type":"item.completed"}`
	}
	lines = append(lines, `{"type":"thread.started","thread_id":"01a03575-0a83-7601-bdcc-1a734ee2b1b2"}`)
	logPath := writeLog(t, lines...)
	_, err := resolveSession(codexRunner(t), evidence.Record{RunID: "run", LogPath: logPath})
	if err == nil || !strings.Contains(err.Error(), "never reported a session id") {
		t.Fatalf("resolveSession error = %v, want a no-session error past the budget", err)
	}
}

// A runner whose session lerp minted resolves from the record alone: no log
// read at all, so a claude-shaped run is unaffected by anything on disk.
func TestResolveSessionUsesRecordWithoutTouchingDisk(t *testing.T) {
	runner := config.Runner{Command: "claude {{session}}", Resume: "claude --resume {{session}}"}
	record := evidence.Record{
		SessionID: "1e9a4a0e-0000-4000-8000-00000000abcd",
		LogPath:   filepath.Join(t.TempDir(), "does-not-exist", "run.log"),
	}
	id, err := resolveSession(runner, record)
	if err != nil {
		t.Fatal(err)
	}
	if id != record.SessionID {
		t.Errorf("resolveSession = %q, want the record's own %q", id, record.SessionID)
	}
}

func TestCapturesSession(t *testing.T) {
	if !CapturesSession(codexRunner(t)) {
		t.Error("CapturesSession(codex) = false, want true")
	}
	if CapturesSession(config.Runner{Command: "claude {{session}}"}) {
		t.Error("CapturesSession(a hand-written command runner) = true, want false")
	}
}

// antigravityRunner is a vendor-resolved runner for agy, the other adapter
// whose CLI names its own session instead of accepting one lerp mints — the
// same shape codexRunner is, over the seam this ticket shares with LERP-137.
func antigravityRunner(t *testing.T) config.Runner {
	t.Helper()
	c, err := config.ParseRepoConfig(`
teams = ["LERP"]
provision = "p"
dispose = "d"

[runners.agy]
vendor = "antigravity"

[queues.implement]
status = "Implementing"
prompt = "p {{ticket}}"
runner = "agy"
on_success = "Done"
`, "test")
	if err != nil {
		t.Fatal(err)
	}
	return c.Runners["agy"]
}

func TestCapturesSessionAntigravity(t *testing.T) {
	if !CapturesSession(antigravityRunner(t)) {
		t.Error("CapturesSession(antigravity) = false, want true")
	}
}

// agy's init line carries conversation_id as a top-level sibling field, not
// nested inside the "init" object the way its own cwd and tool list are —
// this pins that shape against the seam that reads it.
func TestResolveSessionReadsAntigravityInitLine(t *testing.T) {
	logPath := writeLog(t,
		`{"event":"init","conversation_id":"ffd2f49a-85bf-45ab-bfad-80aed96a9b98","init":{"cwd":"/tmp"}}`,
		`{"event":"step_update","step_update":{"step_index":0,"state":"DONE","step_type":"user_input"}}`,
	)
	id, err := resolveSession(antigravityRunner(t), evidence.Record{RunID: "run", LogPath: logPath})
	if err != nil {
		t.Fatal(err)
	}
	if want := "ffd2f49a-85bf-45ab-bfad-80aed96a9b98"; id != want {
		t.Errorf("resolveSession = %q, want %q", id, want)
	}
}

// An agy run killed before its init line ever appeared leaves nothing to
// resume, and that has to fail plainly rather than resolve to an empty id.
func TestResolveSessionErrorsForAntigravityWhenTheLogNeverNamesOne(t *testing.T) {
	logPath := writeLog(t, `{"event":"step_update","step_update":{"step_index":0,"state":"DONE","step_type":"user_input"}}`)
	_, err := resolveSession(antigravityRunner(t), evidence.Record{RunID: "run-3", LogPath: logPath})
	if err == nil || !strings.Contains(err.Error(), "never reported a session id") {
		t.Fatalf("resolveSession error = %v, want a no-session error", err)
	}
}
