//go:build unix

package evidence

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mattwalters/lerp/internal/childenv"
)

const (
	stateDir      = ".lerp"
	runsDir       = "runs"
	workspacesDir = "workspaces"
	lockFile      = "lock"
)

// Record is the local evidence for a lane's running agent. It is intentionally
// limited to process facts; ticket decisions remain in Linear.
//
// ProcessStartedUnix pairs with PID to survive PID reuse. It is stored as
// seconds since the epoch rather than as the operating system's own rendering
// of the time: `ps` formats start times in the caller's locale and timezone,
// so a stored string would compare unequal to itself when read back by a
// process whose TZ or LC_TIME differs.
type Record struct {
	RunID              string    `json:"run_id"`
	Lane               int       `json:"lane"`
	PID                int       `json:"pid"`
	ProcessStartedUnix int64     `json:"process_started_unix"`
	StartedAt          time.Time `json:"started_at"`
	TicketID           string    `json:"ticket_id"`
	Queue              string    `json:"queue"`
	StartingStatus     string    `json:"starting_status"`
	Workspace          string    `json:"workspace"`
	LogPath            string    `json:"log_path"`
	// ExitPath is where the run writes its own exit status. A run started by
	// a previous lerp is not this process's child, so nobody can wait() for
	// its code; the file is how a finished run still reports one. Empty on
	// records written before it existed, which ExitStatus reads as "no
	// status", the same as a run that never got that far.
	ExitPath string `json:"exit_path,omitempty"`
	// SessionID is the session the agent was told to open, when its runner's
	// command asked for one. It is written before the agent starts, so a run
	// left behind by a previous lerp can still be resumed; a run whose runner
	// mints its own session ids has none here and cannot be ejected.
	SessionID string `json:"session_id,omitempty"`
	// Ticket is the human identifier the run was started for — LERP-42, not
	// TicketID's opaque Linear id. It is what {{ticket}} expands to, so the
	// resume command eject hands over reads like the command lerp ran.
	Ticket string `json:"ticket,omitempty"`
}

// ErrLocked reports that another lerp process is running in this clone.
var ErrLocked = errors.New("lerp is already running in this clone")

// Evidence manages a clone's disposable run records and its single-process
// lock. root is the repository root.
type Evidence struct {
	root string
}

// New returns an Evidence store rooted at root.
func New(root string) *Evidence {
	return &Evidence{root: root}
}

// Create makes a directory for one run under .lerp/runs, writes that run's
// metadata, and reserves its log file. The directory is assembled under a
// temporary name and renamed into place, so a run directory never becomes
// visible to List before it holds a complete record.
//
// A caller that already knows the agent's PID may set it on record; Create
// stamps the matching process start time. A PID that only exists after the
// agent is spawned goes in later, through Attach.
func (e *Evidence) Create(record Record) (Record, error) {
	if record.Lane < 1 {
		return record, fmt.Errorf("creating run record: lane must be positive")
	}
	var err error
	record.RunID, err = newRunID()
	if err != nil {
		return record, fmt.Errorf("generating run ID: %w", err)
	}
	if record.PID > 0 && record.ProcessStartedUnix == 0 {
		if started, err := ProcessStart(record.PID); err == nil {
			record.ProcessStartedUnix = started
		}
	}

	if err := os.MkdirAll(e.runsPath(), 0o755); err != nil {
		return record, fmt.Errorf("creating run evidence directory: %w", err)
	}
	// The staging name is deliberately not a valid run ID, so List skips it
	// even while it is half-built.
	staging, err := os.MkdirTemp(e.runsPath(), ".staging-")
	if err != nil {
		return record, fmt.Errorf("creating run evidence directory: %w", err)
	}
	defer os.RemoveAll(staging)

	runPath := e.runPath(record.RunID)
	record.LogPath = filepath.Join(runPath, "run.log")
	// Only the path is reserved. Unlike the log, the file is deliberately not
	// created: its absence is the signal that the run never reached its own
	// last line, and an empty file created here would be indistinguishable
	// from a torn write.
	record.ExitPath = filepath.Join(runPath, "exit")
	if record.Workspace == "" {
		// Unless the caller has its own placement policy, the workspace lives
		// under .lerp/workspaces, beside the run evidence rather than inside
		// it. Deleting run records must orphan agents, never rip working
		// trees out from under them (SCOPE invariant 1), and Remove must
		// never delete a provisioned workspace behind its dispose command's
		// back — a git worktree swept away by RemoveAll would strand its
		// registration in the parent repository.
		record.Workspace = filepath.Join(e.workspacesPath(), record.RunID)
	}
	log, err := os.OpenFile(filepath.Join(staging, "run.log"), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return record, fmt.Errorf("creating run log: %w", err)
	}
	if err := log.Close(); err != nil {
		return record, fmt.Errorf("closing run log: %w", err)
	}
	if err := writeJSON(filepath.Join(staging, "metadata.json"), record); err != nil {
		return record, err
	}
	if err := os.Rename(staging, runPath); err != nil {
		return record, fmt.Errorf("publishing run record: %w", err)
	}
	return record, nil
}

// Attach records the PID of an agent that has now started, together with that
// process's start time, and returns the updated record. It exists because a
// runner's PID is only knowable after the process is spawned, while its log
// path has to be reserved before.
func (e *Evidence) Attach(runID string, pid int) (Record, error) {
	record, err := e.Read(runID)
	if err != nil {
		return record, err
	}
	if pid < 1 {
		return record, fmt.Errorf("attaching PID to run %s: PID must be positive", runID)
	}
	record.PID = pid
	// A process that has already exited leaves no start time to read. Record
	// the PID anyway: Alive then falls back to an existence check, which is
	// strictly better than treating the run as never having had a process.
	if started, err := ProcessStart(pid); err == nil {
		record.ProcessStartedUnix = started
	}
	if err := writeJSON(filepath.Join(e.runPath(runID), "metadata.json"), record); err != nil {
		return record, err
	}
	return record, nil
}

// Disown drops the two things that make a record actionable — the workspace
// to dispose and the ticket to settle — leaving a record that says only that
// a run was here.
//
// It is how a workspace is handed to the operator (eject): the record is
// removed straight afterwards, but a removal can fail and a process can die
// between the two, and a leftover record is read by the next lerp as a dead
// run to reap. Reaping this one disposes nothing and touches no ticket, which
// is the whole promise — an ejected workspace is the operator's, and their
// ticket keeps the claim they took over with.
func (e *Evidence) Disown(runID string) error {
	record, err := e.Read(runID)
	if err != nil {
		return fmt.Errorf("disowning run %s: %w", runID, err)
	}
	record.Workspace, record.TicketID = "", ""
	if err := writeJSON(filepath.Join(e.runPath(runID), "metadata.json"), record); err != nil {
		return fmt.Errorf("disowning run %s: %w", runID, err)
	}
	return nil
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

// List returns all locally readable runs. It deliberately does not infer a
// queue state; callers reconcile these process facts against Linear.
//
// An entry that cannot be read or decoded is skipped rather than failing the
// listing. Local evidence is disposable by design — someone may delete a run
// directory under a live agent — and letting one damaged record hide every
// healthy one would turn a loss of local state into a loss of correctness: the
// reconciler could see no runs at all and neither adopt nor reap anything.
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
			continue
		}
		records = append(records, record)
	}
	return records, nil
}

// Remove discards a run's local evidence. It is safe to call for a run whose
// directory has already gone away, and safe to call again after a failure.
//
// The directory is renamed out of the way first, under a name List ignores, so
// a run either appears intact or not at all — never as a directory whose
// metadata has already been deleted.
func (e *Evidence) Remove(runID string) error {
	if !validRunID(runID) {
		return fmt.Errorf("removing run record: invalid run ID")
	}
	runPath := e.runPath(runID)
	discarded := filepath.Join(e.runsPath(), ".discarded-"+runID)
	switch err := os.Rename(runPath, discarded); {
	case errors.Is(err, fs.ErrNotExist):
		// Already gone, or already renamed by an interrupted earlier call.
	case err != nil:
		return fmt.Errorf("discarding run record: %w", err)
	}
	if err := os.RemoveAll(discarded); err != nil {
		return fmt.Errorf("removing run record: %w", err)
	}
	return nil
}

// Lock is ownership of a clone lock, held for as long as its file stays open.
type Lock struct {
	file *os.File
}

// AcquireLock claims this clone for the current lerp process by taking an
// advisory lock on .lerp/lock.
//
// The kernel arbitrates, which is the point: the lock is released when the
// descriptor closes, including when the process dies for any reason, so there
// is no such thing as a stale lock to detect and break. Two processes can
// never agree that a third is dead, a crash cannot wedge the clone, and the
// file's contents are never consulted to decide whether the lock is held —
// they are diagnostics for whoever reads .lerp/lock.
func (e *Evidence) AcquireLock() (*Lock, error) {
	if err := os.MkdirAll(e.statePath(), 0o755); err != nil {
		return nil, fmt.Errorf("creating lerp state directory: %w", err)
	}
	f, err := os.OpenFile(e.lockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening clone lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		holder := e.lockHolder()
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			if holder != "" {
				return nil, fmt.Errorf("%w (%s)", ErrLocked, holder)
			}
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("locking clone: %w", err)
	}

	lock := &Lock{file: f}
	if err := lock.describe(); err != nil {
		lock.Close()
		return nil, err
	}
	return lock, nil
}

// Close releases this lock.
//
// The file is left in place on purpose. Unlinking it would let a second
// process, already holding a lock on the same inode, be joined by a third that
// creates a fresh file and locks that instead — two holders of one clone.
func (l *Lock) Close() error {
	// Clear the description so a released lock does not read as a held one to
	// somebody inspecting .lerp/lock. Cosmetic: nothing decides anything from
	// these bytes, so a failure here is not worth reporting.
	_ = l.file.Truncate(0)
	if err := l.file.Close(); err != nil {
		return fmt.Errorf("releasing clone lock: %w", err)
	}
	return nil
}

// describe records who holds the lock, for a human reading the file. Nothing
// reads it back to make a decision, so a torn or truncated write here cannot
// affect whether the clone is lockable.
func (l *Lock) describe() error {
	text := fmt.Sprintf("pid %d\nsince %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
	if err := l.file.Truncate(0); err != nil {
		return fmt.Errorf("describing clone lock: %w", err)
	}
	if _, err := l.file.WriteAt([]byte(text), 0); err != nil {
		return fmt.Errorf("describing clone lock: %w", err)
	}
	return nil
}

// lockHolder reads the lock file's description for an error message. Any
// problem reading it yields an empty string: this is decoration, never a
// decision.
func (e *Evidence) lockHolder() string {
	data, err := os.ReadFile(e.lockPath())
	if err != nil {
		return ""
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(data)), "\n")
	return strings.TrimSpace(first)
}

// Alive reports whether the process recorded for a run is still running.
//
// When a start time was recorded, it must match the process now holding that
// PID, so a recycled PID reads as dead. When no start time could be recorded,
// the check falls back to the PID's existence alone.
func Alive(record Record) bool {
	if record.PID < 1 {
		return false
	}
	// Signal 0 checks for existence without delivering anything. EPERM means
	// the process exists but belongs to someone else.
	err := syscall.Kill(record.PID, 0)
	if errors.Is(err, syscall.ESRCH) {
		return false
	}
	if err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	if record.ProcessStartedUnix == 0 {
		return true
	}
	started, err := ProcessStart(record.PID)
	if err != nil {
		return true
	}
	return started == record.ProcessStartedUnix
}

// exitStatusMax bounds what ExitStatus will read. A status is at most three
// digits and a newline; anything longer is not one, and reading it in full
// would let a runaway file into a decision that has a safe answer already.
const exitStatusMax = 32

// ExitStatus returns the exit status a run recorded for itself, and whether
// there is one to read. It is a process fact, so it lives beside Alive.
//
// Only a whole file that trims to a single integer in 0-255 counts. Absent,
// empty, torn, oversized or non-numeric all report false, and callers fall
// back to knowing nothing about how the run ended: a run killed with SIGKILL
// or interrupted mid-write never wrote a status, and guessing at one would
// turn a lost exit code into a wrong hop on the board.
func ExitStatus(record Record) (int, bool) {
	if record.ExitPath == "" {
		return 0, false
	}
	f, err := os.Open(record.ExitPath)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, exitStatusMax+1))
	if err != nil || len(data) > exitStatusMax {
		return 0, false
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || code < 0 || code > 255 {
		return 0, false
	}
	return code, true
}

// ProcessStart returns the start time of a running process, in seconds since
// the epoch.
//
// LC_ALL and TZ are pinned so `ps` renders a format this can parse and a value
// that means the same thing to every reader; without that, the same live
// process yields different answers to callers in different timezones. The
// environment is built by childenv like every other child lerp spawns: `ps`
// is resolved through PATH, and an agent that can write earlier on that PATH
// must not find the operator's Linear key in what it inherits.
func ProcessStart(pid int) (int64, error) {
	cmd := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	cmd.Env = childenv.Inherited("LC_ALL=C", "TZ=UTC")
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("reading start time of process %d: %w", pid, err)
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return 0, fmt.Errorf("reading start time of process %d: no such process", pid)
	}
	started, err := parseStartTime(text)
	if err != nil {
		return 0, fmt.Errorf("start time of process %d: %w", pid, err)
	}
	return started, nil
}

// parseStartTime reads the `ps` C-locale start time, e.g.
// "Sun Aug 24 05:12:33 2026". Both BSD and GNU ps space-pad single-digit days
// ("Aug  3"), so runs of whitespace are collapsed before parsing rather than
// being matched by the layout.
func parseStartTime(text string) (int64, error) {
	started, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.Join(strings.Fields(text), " "), time.UTC)
	if err != nil {
		return 0, fmt.Errorf("parsing start time %q: %w", text, err)
	}
	return started.Unix(), nil
}

func (e *Evidence) statePath() string           { return filepath.Join(e.root, stateDir) }
func (e *Evidence) runsPath() string            { return filepath.Join(e.statePath(), runsDir) }
func (e *Evidence) workspacesPath() string      { return filepath.Join(e.statePath(), workspacesDir) }
func (e *Evidence) lockPath() string            { return filepath.Join(e.statePath(), lockFile) }
func (e *Evidence) runPath(runID string) string { return filepath.Join(e.runsPath(), runID) }

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
