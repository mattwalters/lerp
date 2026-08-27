package telemetry

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPathHonorsXDGStateHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	got, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "lerp", "runs.jsonl"); got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestPathFallsBackToLocalState(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".local", "state", "lerp", "runs.jsonl"); got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

// Append is called from every settling lane, so a second run's line must
// land after the first's rather than replacing it.
func TestAppendCreatesTheDirAndAppendsWithoutClobbering(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	if err := Append(Run{Ticket: "LERP-1"}); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if err := Append(Run{Ticket: "LERP-2"}); err != nil {
		t.Fatalf("second append: %v", err)
	}

	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if len(lines) != 2 {
		t.Fatalf("file has %d lines, want 2: %v", len(lines), lines)
	}
	var first, second Run
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatal(err)
	}
	if first.Ticket != "LERP-1" || second.Ticket != "LERP-2" {
		t.Fatalf("lines = %q/%q, want LERP-1 then LERP-2", first.Ticket, second.Ticket)
	}
}

// A write failure must be reported, not panic: telemetry is history that a
// bad disk may cost, never a crash the run it describes has to survive.
//
// A directory sitting where the file goes, rather than a chmod: a test
// running as root writes through a read-only mode happily, and CI
// containers do run as root.
func TestAppendReportsAnUnwritableDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := Append(Run{Ticket: "LERP-1"}); err == nil {
		t.Fatal("Append where the file's own path is a directory succeeded, want an error")
	}
}

// The format is a stable interface from day one, so its exact shape is
// pinned key-for-key: a full event, and a degraded one with nothing a run
// could not supply.
func TestRunJSON(t *testing.T) {
	at := time.Date(2026, 8, 27, 10, 4, 11, 0, time.UTC)
	code := 0

	t.Run("full", func(t *testing.T) {
		run := Run{
			At: at, Repo: "/Users/matt/src/donewell/lerp", Team: "LERP", Ticket: "LERP-138",
			Queue: "implement", Runner: "claude", Vendor: "claude", Model: "claude-opus-4-6",
			Session: "7420e6f8", DurationMS: 742318, Tokens: 1284310, CostUSD: 3.71,
			ExitCode: &code, Status: "In Review",
		}
		want := `{"at":"2026-08-27T10:04:11Z","repo":"/Users/matt/src/donewell/lerp","team":"LERP",` +
			`"ticket":"LERP-138","queue":"implement","runner":"claude","vendor":"claude",` +
			`"model":"claude-opus-4-6","session":"7420e6f8","duration_ms":742318,"tokens":1284310,` +
			`"cost_usd":3.71,"exit_code":0,"status":"In Review"}`
		assertJSON(t, run, want)
	})

	t.Run("degraded: no vendor, no model, no cost, no exit code, no status", func(t *testing.T) {
		run := Run{
			At: at, Repo: "/repo", Team: "LERP", Ticket: "LERP-1", Queue: "implement",
			Runner: "shell",
		}
		want := `{"at":"2026-08-27T10:04:11Z","repo":"/repo","team":"LERP","ticket":"LERP-1",` +
			`"queue":"implement","runner":"shell"}`
		assertJSON(t, run, want)
	})
}

func assertJSON(t *testing.T, run Run, want string) {
	t.Helper()
	data, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != want {
		t.Errorf("json = %s, want %s", got, want)
	}
}
