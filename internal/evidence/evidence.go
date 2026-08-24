package evidence

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	stateDir = ".lerp"
	runsDir  = "runs"
	lockFile = "lock.json"
)

// Record is the local evidence for a lane's running agent. It is intentionally
// limited to process facts; ticket decisions remain in Linear.
type Record struct {
	RunID          string    `json:"run_id"`
	Lane           int       `json:"lane"`
	PID            int       `json:"pid"`
	ProcessStarted string    `json:"process_started"`
	StartedAt      time.Time `json:"started_at"`
	TicketID       string    `json:"ticket_id"`
	Queue          string    `json:"queue"`
	StartingStatus string    `json:"starting_status"`
	Workspace      string    `json:"workspace"`
	LogPath        string    `json:"log_path"`
	SessionID      string    `json:"session_id,omitempty"`
}

// ErrLocked reports that another lerp process is running in this clone.
var ErrLocked = errors.New("lerp is already running in this clone")

// Evidence manages a clone's disposable run records and its single-process
// lock. root is the repository root.
type Evidence struct {
	root            string
	processIdentity func(int) (string, bool)
}

// New returns an Evidence store rooted at root.
func New(root string) *Evidence {
	return newEvidence(root, processStart)
}

func newEvidence(root string, processIdentity func(int) (string, bool)) *Evidence {
	return &Evidence{root: root, processIdentity: processIdentity}
}

// Create makes a directory for one run under .lerp/runs. It atomically writes
// that run's metadata and reserves its log file, then returns the complete
// record for callers to pass to a runner.
func (e *Evidence) Create(record Record) (Record, error) {
	if record.Lane < 1 {
		return record, fmt.Errorf("creating run record: lane must be positive")
	}
	var err error
	record.RunID, err = newRunID()
	if err != nil {
		return record, fmt.Errorf("generating run ID: %w", err)
	}
	runPath := e.runPath(record.RunID)
	if err := os.MkdirAll(runPath, 0o755); err != nil {
		return record, fmt.Errorf("creating run evidence directory: %w", err)
	}
	record.LogPath = filepath.Join(runPath, "run.log")
	log, err := os.OpenFile(record.LogPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return record, fmt.Errorf("creating run log: %w", err)
	}
	if err := log.Close(); err != nil {
		return record, fmt.Errorf("closing run log: %w", err)
	}
	if err := writeJSON(filepath.Join(runPath, "metadata.json"), record); err != nil {
		_ = os.Remove(record.LogPath)
		_ = os.Remove(runPath)
		return record, err
	}
	return record, nil
}

// Read returns one run's recorded evidence. It returns fs.ErrNotExist when
// the run has no local record.
func (e *Evidence) Read(runID string) (Record, error) {
	var record Record
	if !validRunID(runID) {
		return record, fmt.Errorf("reading run record: invalid run ID")
	}
	data, err := os.ReadFile(filepath.Join(e.runPath(runID), "metadata.json"))
	if err != nil {
		return record, err
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return record, fmt.Errorf("decoding run record: %w", err)
	}
	if record.RunID != runID {
		return record, errors.New("decoding run record: mismatched run ID")
	}
	return record, nil
}

// List returns all locally recorded runs. It deliberately does not infer a
// queue state; callers reconcile these process facts against Linear.
func (e *Evidence) List() ([]Record, error) {
	entries, err := os.ReadDir(e.runsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []Record
	for _, entry := range entries {
		if !entry.IsDir() || !validRunID(entry.Name()) {
			continue
		}
		record, err := e.Read(entry.Name())
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

// Remove discards a run's local evidence. It is safe to call for a run whose
// directory has already gone away.
func (e *Evidence) Remove(runID string) error {
	if !validRunID(runID) {
		return fmt.Errorf("removing run record: invalid run ID")
	}
	runPath := e.runPath(runID)
	err := os.Remove(filepath.Join(runPath, "metadata.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(runPath, "run.log")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Remove(runPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Lock is ownership of a clone lock. Close releases it.
type Lock struct {
	evidence *Evidence
	record   lockRecord
}

type lockRecord struct {
	PID            int       `json:"pid"`
	ProcessStarted string    `json:"process_started"`
	StartedAt      time.Time `json:"started_at"`
}

// AcquireLock claims this clone for the current lerp process. Stale locks
// from dead processes, including a reused PID, are broken before retrying.
func (e *Evidence) AcquireLock() (*Lock, error) {
	if err := os.MkdirAll(e.statePath(), 0o755); err != nil {
		return nil, fmt.Errorf("creating lerp state directory: %w", err)
	}

	record := lockRecord{PID: os.Getpid(), StartedAt: time.Now().UTC()}
	var alive bool
	record.ProcessStarted, alive = e.processIdentity(record.PID)
	if !alive {
		return nil, errors.New("finding lerp process start time")
	}

	for {
		err := createJSON(e.lockPath(), record)
		if err == nil {
			return &Lock{evidence: e, record: record}, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("creating clone lock: %w", err)
		}

		locked, err := e.readLock()
		if err != nil {
			return nil, err
		}
		started, alive := e.processIdentity(locked.PID)
		if alive && started == locked.ProcessStarted {
			return nil, fmt.Errorf("%w (pid %d)", ErrLocked, locked.PID)
		}
		if err := os.Remove(e.lockPath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("breaking stale clone lock: %w", err)
		}
	}
}

// Close releases this lock. It will not remove a lock that another process
// acquired after this one was forcibly removed.
func (l *Lock) Close() error {
	current, err := l.evidence.readLock()
	if err != nil {
		return err
	}
	if current != l.record {
		return errors.New("clone lock is no longer owned by this process")
	}
	if err := os.Remove(l.evidence.lockPath()); err != nil {
		return fmt.Errorf("removing clone lock: %w", err)
	}
	return nil
}

func (e *Evidence) readLock() (lockRecord, error) {
	var record lockRecord
	data, err := os.ReadFile(e.lockPath())
	if err != nil {
		return record, fmt.Errorf("reading clone lock: %w", err)
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return record, fmt.Errorf("decoding clone lock: %w", err)
	}
	if record.PID < 1 || record.ProcessStarted == "" {
		return record, errors.New("decoding clone lock: missing process identity")
	}
	return record, nil
}

func (e *Evidence) statePath() string           { return filepath.Join(e.root, stateDir) }
func (e *Evidence) runsPath() string            { return filepath.Join(e.statePath(), runsDir) }
func (e *Evidence) lockPath() string            { return filepath.Join(e.statePath(), lockFile) }
func (e *Evidence) runPath(runID string) string { return filepath.Join(e.runsPath(), runID) }

func createJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func writeJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".run-*")
	if err != nil {
		return fmt.Errorf("creating temporary run record: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replacing run record: %w", err)
	}
	return nil
}

// processStart identifies a process by its PID and operating-system start
// time. The latter distinguishes a stale lock from a new process that reused
// its PID.
func processStart(pid int) (string, bool) {
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", false
	}
	start := strings.TrimSpace(string(out))
	return start, start != ""
}

func newRunID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", value), nil
}

func validRunID(runID string) bool {
	if len(runID) != 32 {
		return false
	}
	for _, r := range runID {
		if !('0' <= r && r <= '9' || 'a' <= r && r <= 'f') {
			return false
		}
	}
	return true
}
