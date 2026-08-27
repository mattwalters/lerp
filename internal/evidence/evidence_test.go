//go:build unix

package evidence

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRecordsRoundTripAndRemove(t *testing.T) {
	e := New(t.TempDir())
	want := Record{
		Lane:           2,
		StartedAt:      time.Date(2026, 8, 23, 12, 1, 0, 0, time.UTC),
		TicketID:       "LERP-8",
		Queue:          "Implementing",
		StartingStatus: "Todo",
		Workspace:      "/tmp/lerp-8",
		LogPath:        "/tmp/lerp-8.log",
		SessionID:      "session",
	}
	got, err := e.Create(want)
	if err != nil {
		t.Fatal(err)
	}
	if got.RunID == "" || got.LogPath == "" || got.ExitPath == "" {
		t.Fatalf("Create did not set run ID, log path and exit path: %#v", got)
	}
	if _, err := os.Stat(got.LogPath); err != nil {
		t.Fatalf("run log: %v", err)
	}
	runDir := filepath.Dir(got.LogPath)
	if filepath.Dir(got.ExitPath) != runDir {
		t.Errorf("exit path = %q, want it inside the run directory %q", got.ExitPath, runDir)
	}
	// Reserved, never created: the file's absence is what tells a reader the
	// run never reached its own last line.
	if _, err := os.Stat(got.ExitPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("exit file after Create: stat error = %v, want not exist", err)
	}
	got, err = e.Read(got.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TicketID != want.TicketID || got.Lane != want.Lane || got.RunID == "" {
		t.Errorf("record = %#v, want the original run facts", got)
	}
	if got.ExitPath != filepath.Join(runDir, "exit") {
		t.Errorf("exit path did not round-trip: %q", got.ExitPath)
	}
	if err := e.Remove(got.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Read(got.RunID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Read after Remove error = %v, want not exist", err)
	}
	if err := e.Remove(got.RunID); err != nil {
		t.Fatalf("second Remove: %v", err)
	}
}

// A record without a workspace policy of its own gets one under
// .lerp/workspaces — beside the run evidence, not inside it, so deleting run
// records never touches a live agent's working tree.
func TestCreateChoosesAWorkspaceBesideTheRunDirectory(t *testing.T) {
	e := New(t.TempDir())
	record, err := e.Create(Record{Lane: 1, TicketID: "LERP-9"})
	if err != nil {
		t.Fatal(err)
	}
	if record.Workspace != filepath.Join(e.workspacesPath(), record.RunID) {
		t.Errorf("Workspace = %q, want it under the workspaces directory", record.Workspace)
	}
	kept, err := e.Create(Record{Lane: 1, TicketID: "LERP-9", Workspace: "/elsewhere"})
	if err != nil {
		t.Fatal(err)
	}
	if kept.Workspace != "/elsewhere" {
		t.Errorf("Workspace = %q, want the caller's own path kept", kept.Workspace)
	}
}

func TestWriteRejectsInvalidLane(t *testing.T) {
	e := New(t.TempDir())
	if _, err := e.Create(Record{}); err == nil {
		t.Error("Create succeeded for lane zero")
	}
}

func TestList(t *testing.T) {
	e := New(t.TempDir())
	first, err := e.Create(Record{Lane: 1, TicketID: "LERP-8"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.Create(Record{Lane: 2, TicketID: "LERP-9"})
	if err != nil {
		t.Fatal(err)
	}
	records, err := e.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("List returned %d records, want 2", len(records))
	}
	seen := map[string]bool{records[0].RunID: true, records[1].RunID: true}
	if !seen[first.RunID] || !seen[second.RunID] {
		t.Errorf("List = %#v, want both created runs", records)
	}
}

// Local evidence is disposable, so one damaged record must not hide the
// healthy ones — that would turn lost local state into a reconciler that can
// see no runs at all.
func TestListSkipsUnreadableRecords(t *testing.T) {
	e := New(t.TempDir())
	healthy, err := e.Create(Record{Lane: 1, TicketID: "LERP-8"})
	if err != nil {
		t.Fatal(err)
	}
	corrupt, err := e.Create(Record{Lane: 2, TicketID: "LERP-9"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.runPath(corrupt.RunID), "metadata.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing, err := e.Create(Record{Lane: 3, TicketID: "LERP-10"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(e.runPath(missing.RunID), "metadata.json")); err != nil {
		t.Fatal(err)
	}

	records, err := e.List()
	if err != nil {
		t.Fatalf("List error = %v, want the healthy record", err)
	}
	if len(records) != 1 || records[0].RunID != healthy.RunID {
		t.Errorf("List = %#v, want only the healthy run", records)
	}
}

// A half-built run directory must never be listable: Create assembles it under
// a name List ignores and renames it into place.
func TestCreateLeavesNoStagingDirectories(t *testing.T) {
	e := New(t.TempDir())
	record, err := e.Create(Record{Lane: 1, TicketID: "LERP-8"})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(e.runsPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != record.RunID {
		t.Errorf("runs directory = %v, want only the published run", names(entries))
	}
	// Every published directory holds a complete record.
	if _, err := e.Read(record.RunID); err != nil {
		t.Errorf("published run is unreadable: %v", err)
	}
}

func TestListIgnoresLeftoverStagingDirectories(t *testing.T) {
	e := New(t.TempDir())
	if _, err := e.Create(Record{Lane: 1, TicketID: "LERP-8"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(e.runsPath(), ".staging-abandoned"), 0o755); err != nil {
		t.Fatal(err)
	}
	records, err := e.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Errorf("List = %#v, want to ignore the abandoned staging directory", records)
	}
}

// A run directory can hold more than lerp put there — a temp file from an
// interrupted write, for instance — and removal must still finish.
func TestRemoveDiscardsUnexpectedFiles(t *testing.T) {
	e := New(t.TempDir())
	record, err := e.Create(Record{Lane: 1, TicketID: "LERP-8"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.runPath(record.RunID), ".run-leftover"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.Remove(record.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.runPath(record.RunID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("run directory still present: %v", err)
	}
}

func TestRemoveFinishesAnInterruptedRemoval(t *testing.T) {
	e := New(t.TempDir())
	record, err := e.Create(Record{Lane: 1, TicketID: "LERP-8"})
	if err != nil {
		t.Fatal(err)
	}
	// Model a process that died between the rename and the delete.
	discarded := filepath.Join(e.runsPath(), ".discarded-"+record.RunID)
	if err := os.Rename(e.runPath(record.RunID), discarded); err != nil {
		t.Fatal(err)
	}
	if err := e.Remove(record.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(discarded); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("discarded directory survived: %v", err)
	}
}

func TestAcquireLockRefusesASecondHolder(t *testing.T) {
	dir := t.TempDir()
	first, err := New(dir).AcquireLock()
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	if _, err := New(dir).AcquireLock(); !errors.Is(err, ErrLocked) {
		t.Fatalf("second AcquireLock = %v, want ErrLocked", err)
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := New(dir).AcquireLock()
	if err != nil {
		t.Fatalf("AcquireLock after release = %v, want success", err)
	}
	if err := again.Close(); err != nil {
		t.Fatal(err)
	}
}

// The lock file's contents are diagnostics. Whatever they say, or fail to say,
// must never decide whether the clone can be locked — a truncated write used
// to wedge the clone until a human deleted a file nobody had told them about.
func TestAcquireLockIgnoresLockFileContents(t *testing.T) {
	for _, contents := range []string{"", "{", "pid 0\n", "not json at all"} {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, stateDir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, stateDir, lockFile), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		lock, err := New(dir).AcquireLock()
		if err != nil {
			t.Fatalf("AcquireLock with lock file %q = %v, want success", contents, err)
		}
		if err := lock.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

const holdLockEnv = "LERP_TEST_HOLD_LOCK_DIR"

// The whole reason to let the kernel hold the lock: a process that dies without
// cleaning up releases it anyway, so there is no stale lock to detect and no
// way for a survivor to wrongly conclude the holder is dead.
func TestLockIsReleasedWhenTheHolderIsKilled(t *testing.T) {
	dir := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run=TestHelperHoldsLock", "-test.v")
	child.Env = append(os.Environ(), holdLockEnv+"="+dir)
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := false
	t.Cleanup(func() {
		if !reaped {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	})

	holding := false
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), "lock-held") {
			holding = true
			break
		}
	}
	if !holding {
		t.Fatal("helper process never reported holding the lock")
	}

	if _, err := New(dir).AcquireLock(); !errors.Is(err, ErrLocked) {
		t.Fatalf("AcquireLock while the helper holds it = %v, want ErrLocked", err)
	}

	// SIGKILL gives the helper no chance to release anything.
	if err := child.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	reaped = true

	lock, err := New(dir).AcquireLock()
	if err != nil {
		t.Fatalf("AcquireLock after the holder was killed = %v, want success", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestHelperHoldsLock is not a test of its own: it is the child process for
// TestLockIsReleasedWhenTheHolderIsKilled, which kills it mid-hold.
func TestHelperHoldsLock(t *testing.T) {
	dir := os.Getenv(holdLockEnv)
	if dir == "" {
		t.Skip("helper process for TestLockIsReleasedWhenTheHolderIsKilled")
	}
	if _, err := New(dir).AcquireLock(); err != nil {
		t.Fatalf("helper could not acquire the lock: %v", err)
	}
	fmt.Println("lock-held")
	time.Sleep(2 * time.Minute)
}

// ps space-pads single-digit days, and the recorded value has to mean the same
// thing regardless of the reader's timezone, so parsing is pinned to UTC.
func TestParseStartTime(t *testing.T) {
	for _, tt := range []struct {
		text string
		want int64
	}{
		{"Sun Aug 24 05:12:33 2026", time.Date(2026, 8, 24, 5, 12, 33, 0, time.UTC).Unix()},
		{"Mon Aug  3 05:12:33 2026", time.Date(2026, 8, 3, 5, 12, 33, 0, time.UTC).Unix()},
		{"Mon Aug 3 05:12:33 2026", time.Date(2026, 8, 3, 5, 12, 33, 0, time.UTC).Unix()},
	} {
		got, err := parseStartTime(tt.text)
		if err != nil {
			t.Errorf("parseStartTime(%q) error = %v", tt.text, err)
			continue
		}
		if got != tt.want {
			t.Errorf("parseStartTime(%q) = %d, want %d", tt.text, got, tt.want)
		}
	}
	if _, err := parseStartTime("not a time"); err == nil {
		t.Error("parseStartTime accepted nonsense")
	}
}

func TestAliveTracksTheRecordedProcess(t *testing.T) {
	started, err := ProcessStart(os.Getpid())
	if err != nil {
		t.Skipf("this system cannot report process start times: %v", err)
	}
	if started <= 0 || started > time.Now().Unix() {
		t.Errorf("ProcessStart = %d, want a past epoch second", started)
	}

	if !Alive(Record{PID: os.Getpid(), ProcessStartedUnix: started}) {
		t.Error("Alive = false for this test's own process")
	}
	// A different start time on the same PID is how PID reuse looks.
	if Alive(Record{PID: os.Getpid(), ProcessStartedUnix: started - 1}) {
		t.Error("Alive = true for a PID whose start time does not match: reuse went undetected")
	}
	// No recorded start time degrades to an existence check rather than
	// reporting every run dead.
	if !Alive(Record{PID: os.Getpid()}) {
		t.Error("Alive = false for a live PID with no recorded start time")
	}
	if Alive(Record{}) {
		t.Error("Alive = true for a record with no PID")
	}
}

func TestAliveReportsAnExitedProcess(t *testing.T) {
	done := exec.Command("sh", "-c", "exit 0")
	if err := done.Run(); err != nil {
		t.Fatal(err)
	}
	pid := done.Process.Pid
	if Alive(Record{PID: pid, ProcessStartedUnix: time.Now().Unix()}) {
		t.Errorf("Alive = true for exited process %d", pid)
	}
}

func TestAttachRecordsTheProcessIdentity(t *testing.T) {
	e := New(t.TempDir())
	record, err := e.Create(Record{Lane: 1, TicketID: "LERP-8"})
	if err != nil {
		t.Fatal(err)
	}
	if record.PID != 0 {
		t.Fatalf("Create recorded a PID it was not given: %d", record.PID)
	}
	attached, err := e.Attach(record.RunID, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if attached.PID != os.Getpid() {
		t.Errorf("attached PID = %d, want %d", attached.PID, os.Getpid())
	}
	stored, err := e.Read(record.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.PID != os.Getpid() {
		t.Errorf("stored PID = %d, want it persisted", stored.PID)
	}
	if stored.TicketID != "LERP-8" {
		t.Errorf("stored ticket = %q, want the original run facts kept", stored.TicketID)
	}
	if _, err := e.Attach(record.RunID, 0); err == nil {
		t.Error("Attach accepted PID zero")
	}
}

func names(entries []os.DirEntry) []string {
	out := make([]string, len(entries))
	for i, entry := range entries {
		out[i] = entry.Name()
	}
	return out
}

// ExitStatus is the only thing standing between a torn file and a wrong hop on
// the board, so it accepts exactly a whole file that trims to one integer in
// the range a shell can report, and nothing else.
func TestExitStatusReadsOnlyACleanStatus(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string // the exit file's bytes; missing means no file at all
		missing bool
		want    int
		wantOK  bool
	}{
		{name: "clean zero", content: "0", want: 0, wantOK: true},
		{name: "trailing newline", content: "0\n", want: 0, wantOK: true},
		{name: "signalled", content: "137\n", want: 137, wantOK: true},
		{name: "surrounding whitespace", content: "  3 \n", want: 3, wantOK: true},
		{name: "absent", missing: true},
		{name: "empty", content: ""},
		{name: "whitespace only", content: " \n"},
		{name: "not a number", content: "abc\n"},
		{name: "two numbers", content: "1 2\n"},
		{name: "out of range", content: "999\n"},
		{name: "negative", content: "-1\n"},
		{name: "oversized", content: strings.Repeat("0", 33)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := New(t.TempDir())
			record, err := e.Create(Record{Lane: 1, TicketID: "LERP-1"})
			if err != nil {
				t.Fatal(err)
			}
			if !tc.missing {
				if err := os.WriteFile(record.ExitPath, []byte(tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			code, ok := ExitStatus(record)
			if ok != tc.wantOK || code != tc.want {
				t.Errorf("ExitStatus(%q) = (%d, %v), want (%d, %v)", tc.content, code, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// A record written before exit files existed carries no path, and reads as a
// run that said nothing about how it ended rather than as a status of 0.
func TestExitStatusWithoutAPathReportsNothing(t *testing.T) {
	if code, ok := ExitStatus(Record{RunID: "old", LogPath: "/tmp/run.log"}); ok {
		t.Errorf("ExitStatus of a pathless record = (%d, %v), want no status", code, ok)
	}
}

func TestEvidenceLayoutAccessors(t *testing.T) {
	root := t.TempDir()
	e := New(root)
	if got, want := e.StateDir(), filepath.Join(root, ".lerp"); got != want {
		t.Errorf("StateDir = %q, want %q", got, want)
	}
	if got, want := e.RunsDir(), filepath.Join(root, ".lerp", "runs"); got != want {
		t.Errorf("RunsDir = %q, want %q", got, want)
	}
	if got, want := e.LoopLogPath(), filepath.Join(root, ".lerp", "loop.log"); got != want {
		t.Errorf("LoopLogPath = %q, want %q", got, want)
	}
}
