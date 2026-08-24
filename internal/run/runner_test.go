package run

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mattwalters/lerp/internal/config"
)

func TestExecuteWritesCombinedLogAndReportsExit(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "runner.sh", `
printf 'prompt=%s\n' "$1"
printf 'workdir=%s\n' "$2"
printf 'session=%s\n' "$3" >&2
exit 7
`)
	logPath := filepath.Join(dir, "runner.log")
	prompt := "plan; touch should-not-exist"

	result, err := Execute(context.Background(), Invocation{
		Runner:  config.Runner{Command: shellQuote(script) + " {{prompt}} {{workdir}} {{session}}"},
		Prompt:  prompt,
		Ticket:  "LERP-1",
		Workdir: dir,
		LogPath: logPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 7 {
		t.Errorf("ExitCode = %d, want 7", result.ExitCode)
	}
	if result.Duration <= 0 {
		t.Errorf("Duration = %s, want positive", result.Duration)
	}
	if len(result.SessionID) != 32 {
		t.Errorf("SessionID = %q, want 32 hex characters", result.SessionID)
	}
	if _, err := os.Stat(filepath.Join(dir, "should-not-exist")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("prompt was interpreted by shell: stat error = %v", err)
	}

	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "prompt=" + prompt + "\nworkdir=" + dir + "\nsession=" + result.SessionID + "\n"
	if string(got) != want {
		t.Errorf("log = %q, want %q", got, want)
	}
}

func TestExecuteDoesNotCreateSessionWithoutPlaceholder(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "runner.sh", "exit 0")

	result, err := Execute(context.Background(), Invocation{
		Runner:  config.Runner{Command: shellQuote(script)},
		Prompt:  "prompt",
		Ticket:  "LERP-1",
		Workdir: dir,
		LogPath: filepath.Join(dir, "runner.log"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
	if result.SessionID != "" {
		t.Errorf("SessionID = %q, want empty", result.SessionID)
	}
}

// The agent has to be told which ticket it is working on: the prompt is shared
// by every ticket in a queue, so {{ticket}} in the prompt and the environment
// variable are the only things that name the work.
func TestExecuteCarriesTicketIntoPromptAndEnvironment(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "runner.sh", `
printf 'prompt=%s\n' "$1"
printf 'ticket=%s\n' "$2"
printf 'env=%s\n' "$LERP_TICKET"
`)
	logPath := filepath.Join(dir, "runner.log")

	if _, err := Execute(context.Background(), Invocation{
		Runner:  config.Runner{Command: shellQuote(script) + " {{prompt}} {{ticket}}"},
		Prompt:  "Implement {{ticket}} per its plan comment.",
		Ticket:  "LERP-42",
		Workdir: dir,
		LogPath: logPath,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "prompt=Implement LERP-42 per its plan comment.\nticket=LERP-42\nenv=LERP-42\n"
	if string(got) != want {
		t.Errorf("log = %q, want %q", got, want)
	}
}

func TestExecuteRequiresATicket(t *testing.T) {
	dir := t.TempDir()
	_, err := Execute(context.Background(), Invocation{
		Runner:  config.Runner{Command: "true"},
		Prompt:  "prompt",
		Workdir: dir,
		LogPath: filepath.Join(dir, "runner.log"),
	})
	if err == nil || !strings.Contains(err.Error(), "requires a ticket") {
		t.Errorf("Execute error = %v, want a missing-ticket error", err)
	}
}

func TestExecuteCancelsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	childPIDPath := filepath.Join(dir, "child.pid")
	script := writeScript(t, dir, "runner.sh", `
sleep 30 &
echo "$!" > "$1"
wait
`)
	logPath := filepath.Join(dir, "runner.log")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resultCh := make(chan Result, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := Execute(ctx, Invocation{
			Runner:  config.Runner{Command: shellQuote(script) + " {{prompt}}"},
			Prompt:  childPIDPath,
			Ticket:  "LERP-1",
			Workdir: dir,
			LogPath: logPath,
		})
		resultCh <- result
		errCh <- err
	}()

	var pid int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		contents, err := os.ReadFile(childPIDPath)
		if err == nil {
			pid, err = strconv.Atoi(strings.TrimSpace(string(contents)))
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("runner did not start its child process")
	}

	cancel()
	select {
	case result := <-resultCh:
		if result.ExitCode == 0 {
			t.Error("cancelled runner ExitCode = 0, want non-zero")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled runner did not exit")
	}
	if err := <-errCh; err != nil {
		t.Fatalf("Execute error = %v, want nil for a killed process", err)
	}

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("child process %d still exists after runner cancellation", pid)
}

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
