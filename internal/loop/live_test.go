//go:build unix

package loop

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/evidence"
	"github.com/mattwalters/lerp/internal/linear"
	"github.com/mattwalters/lerp/internal/run"
	"github.com/mattwalters/lerp/internal/workspace"
)

// liveHarness builds a harness wired to the real run.Execute and the real
// evidence.Alive, with the "agent" runner pointing at command and provisioning
// creating the workspace directory on disk so cmd.Dir exists.
func liveHarness(t *testing.T, lanes int, command string) *harness {
	t.Helper()
	h := newHarness(t, lanes, run.Execute)
	h.rec.o.Alive = evidence.Alive
	h.rec.o.Repo.Runners["agent"] = config.Runner{Command: command}
	h.rec.o.Provision = func(_ context.Context, _, _ string, id workspace.Identity, _ io.Writer) error {
		return os.MkdirAll(id.Workspace, 0o755)
	}
	return h
}

// waitFor polls cond until it returns true or the 5s deadline expires.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestLiveCleanRun(t *testing.T) {
	command := `echo "ran {{ticket}} in $PWD"; while [ ! -e go ]; do sleep 0.02; done; exit 0`
	h := liveHarness(t, 1, command)

	// The exit-file assertion needs one deliberate trick, because the run directory
	// is removed the moment a run settles: the harness's injected Dispose reads
	// evidence.ExitStatus(record) and stashes it. provisionAndRun's deferred dispose
	// runs before executeLane removes the record, so this is the last moment the file
	// exists — and ExitStatus is the same reader the reap path uses, which makes the
	// assertion "a successor lerp could have read this run's status".
	var mu sync.Mutex
	var exitStatus int
	var hasExitStatus bool
	origDispose := h.rec.o.Dispose
	h.rec.o.Dispose = func(ctx context.Context, repoDir, cmd string, id workspace.Identity, log io.Writer) {
		records, err := h.evidence.List()
		if err == nil {
			for _, r := range records {
				if r.Workspace == id.Workspace {
					if code, ok := evidence.ExitStatus(r); ok {
						mu.Lock()
						exitStatus = code
						hasExitStatus = true
						mu.Unlock()
					}
				}
			}
		}
		origDispose(ctx, repoDir, cmd, id, log)
	}

	h.fake.AddIssue("LERP", linear.Issue{
		ID:         "t1",
		Identifier: "LERP-1",
		Status:     "Todo",
	})

	h.rec.Tick(context.Background())
	h.waitEvents(t, EventStarted, 1)

	var record evidence.Record
	waitFor(t, "on-disk record with PID > 1", func() bool {
		records, err := h.evidence.List()
		if err != nil || len(records) != 1 {
			return false
		}
		record = records[0]
		return record.PID > 1
	})

	if !evidence.Alive(record) {
		t.Errorf("evidence.Alive(record) = false, want true")
	}
	if record.ProcessStartedUnix == 0 {
		t.Errorf("record.ProcessStartedUnix = 0, want non-zero")
	}

	realWorkspace, err := filepath.EvalSymlinks(record.Workspace)
	if err != nil {
		t.Fatal(err)
	}

	waitFor(t, "run log contains expanded ticket identifier and workspace", func() bool {
		data, err := os.ReadFile(record.LogPath)
		if err != nil {
			return false
		}
		return strings.Contains(string(data), "ran 'LERP-1' in "+realWorkspace)
	})

	goFile := filepath.Join(record.Workspace, "go")
	if err := os.WriteFile(goFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	exited := h.waitEvents(t, EventExited, 1)[0]
	if exited.ExitCode != 0 {
		t.Errorf("exited event ExitCode = %d, want 0", exited.ExitCode)
	}

	mu.Lock()
	gotStatus, gotHasStatus := exitStatus, hasExitStatus
	mu.Unlock()
	if !gotHasStatus || gotStatus != 0 {
		t.Errorf("exit status = %d (found=%v), want 0", gotStatus, gotHasStatus)
	}

	if issue := h.issue(t, "t1"); issue.Status != "Done" {
		t.Errorf("issue status = %q, want Done", issue.Status)
	}

	records, err := h.evidence.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Errorf("records on disk = %v, want none", records)
	}

	disposed := h.disposedIdentities()
	if len(disposed) != 1 || disposed[0].Workspace != record.Workspace {
		t.Errorf("disposed identities = %v, want workspace %q", disposed, record.Workspace)
	}

	tel := h.telemetryRuns()
	if len(tel) != 1 || tel[0].ExitCode == nil || *tel[0].ExitCode != 0 {
		t.Errorf("telemetry runs = %+v, want one run with ExitCode 0", tel)
	}
}

func TestLiveFailingRun(t *testing.T) {
	h := liveHarness(t, 1, "exit 3")
	h.fake.AddIssue("LERP", linear.Issue{
		ID:         "t1",
		Identifier: "LERP-1",
		Status:     "Todo",
	})

	h.rec.Tick(context.Background())
	h.waitEvents(t, EventStarted, 1)

	exited := h.waitEvents(t, EventExited, 1)[0]
	if exited.ExitCode != 3 {
		t.Errorf("exit code = %d, want 3", exited.ExitCode)
	}

	if issue := h.issue(t, "t1"); issue.Status != "Needs Help" {
		t.Errorf("issue status = %q, want 'Needs Help'", issue.Status)
	}
}

func TestLiveAttachFailure(t *testing.T) {
	h := liveHarness(t, 1, "sleep 300")
	h.rec.o.Provision = func(_ context.Context, _, _ string, id workspace.Identity, _ io.Writer) error {
		if err := os.MkdirAll(id.Workspace, 0o755); err != nil {
			return err
		}
		records, err := h.evidence.List()
		if err != nil {
			return err
		}
		if len(records) != 1 {
			return fmt.Errorf("expected 1 record during provision, got %d", len(records))
		}
		runDir := filepath.Join(h.evidence.RunsDir(), records[0].RunID)
		if err := os.RemoveAll(runDir); err != nil {
			return err
		}
		return os.MkdirAll(runDir, 0o755)
	}

	h.fake.AddIssue("LERP", linear.Issue{
		ID:         "t1",
		Identifier: "LERP-1",
		Status:     "Todo",
	})

	h.rec.Tick(context.Background())

	// Only killing the process group can end sleep 300 within the 5s deadline.
	waitIdle(t, h.rec)

	if !strings.Contains(h.logs.String(), "attach pid of run") {
		t.Errorf("loop log %q does not mention 'attach pid of run'", h.logs.String())
	}

	if issue := h.issue(t, "t1"); issue.Status != "Needs Help" {
		t.Errorf("issue status = %q, want 'Needs Help'", issue.Status)
	}
}
