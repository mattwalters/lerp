package telemetry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Run is one finished run, normalized to what deterministic code can say
// about it: config, the loop's own bookkeeping, and what internal/logfmt
// decoded from the run's log. Nothing here ever trusts agent-written prose —
// no parsing of what the agent said, no asking it to report.
//
// Fields a runner or a settlement path could not supply are absent
// (omitempty) rather than zero-faked: a command-template runner naming no
// vendor is not vendor "", and a killed run with no exit file is not exit
// code 0. ExitCode is a pointer because it is the one field where zero is
// itself a real, common value — a clean exit.
//
// The format is a stable interface from day one: additive changes only,
// keys never renamed or repurposed, pinned key-for-key by TestRunJSON.
type Run struct {
	At         time.Time `json:"at"`
	Repo       string    `json:"repo"`
	Team       string    `json:"team"`
	Ticket     string    `json:"ticket"`
	Queue      string    `json:"queue"`
	Runner     string    `json:"runner"`
	Vendor     string    `json:"vendor,omitempty"`
	Model      string    `json:"model,omitempty"`
	Session    string    `json:"session,omitempty"`
	DurationMS int64     `json:"duration_ms,omitempty"`
	Tokens     int       `json:"tokens,omitempty"`
	CostUSD    float64   `json:"cost_usd,omitempty"`
	ExitCode   *int      `json:"exit_code,omitempty"`
	Status     string    `json:"status,omitempty"`
}

const (
	subdir   = "lerp"
	fileName = "runs.jsonl"
)

// Path is where the telemetry file lives: $XDG_STATE_HOME/lerp/runs.jsonl,
// falling back to ~/.local/state/lerp/runs.jsonl on every platform. Go has
// no os.UserStateDir — this is that function's XDG_STATE_HOME sibling — kept
// here so a test can ask for the rule instead of spelling out where it puts
// the file.
func Path() (string, error) {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locating the user home directory: %w", err)
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, subdir, fileName), nil
}

// mu serializes the lanes of one lerp process appending at once; O_APPEND is
// what keeps two lerps in different clones from tearing each other's lines.
var mu sync.Mutex

// Append writes one line, creating the file and its directory if this is the
// first write. Every failure here — missing dir, full disk, an unresolvable
// home — is the caller's to log and carry on from: telemetry is history, not
// state (SCOPE.md invariant 1), and must never fail the run it describes.
func Append(run Run) error {
	path, err := Path()
	if err != nil {
		return err
	}
	line, err := json.Marshal(run)
	if err != nil {
		return fmt.Errorf("encoding telemetry event: %w", err)
	}
	line = append(line, '\n')

	mu.Lock()
	defer mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating the telemetry directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
