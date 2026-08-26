//go:build unix

package run

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mattwalters/lerp/internal/childenv"
	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/evidence"
)

// Version 4, RFC 4122 variant: the shape --session-id will accept.
var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestSessionIDsAreDistinctUUIDs(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		id, err := newUUID()
		if err != nil {
			t.Fatal(err)
		}
		if !uuidPattern.MatchString(id) {
			t.Fatalf("newUUID = %q, want a version 4 UUID", id)
		}
		if seen[id] {
			t.Fatalf("newUUID repeated %q", id)
		}
		seen[id] = true
	}
}

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
		Queue:   config.Queue{Prompt: prompt},
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
	// Agent CLIs that take a caller-chosen session id require a real UUID:
	// Claude Code's --session-id rejects anything else.
	if !uuidPattern.MatchString(result.SessionID) {
		t.Errorf("SessionID = %q, want a version 4 UUID", result.SessionID)
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
		Queue:   config.Queue{Prompt: "prompt"},
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
// variable are the only things that name the work. The queue's own statuses
// reach the prompt the same way — {{status}}, {{on_success}}, {{on_failure}} —
// so movement instructions follow the config rather than hardcoded names.
func TestExecuteExpandsPromptFromTicketAndQueue(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "runner.sh", `
printf 'prompt=%s\n' "$1"
printf 'ticket=%s\n' "$2"
printf 'env=%s\n' "$LERP_TICKET"
`)
	logPath := filepath.Join(dir, "runner.log")

	if _, err := Execute(context.Background(), Invocation{
		Runner: config.Runner{Command: shellQuote(script) + " {{prompt}} {{ticket}}"},
		Queue: config.Queue{
			Status:    "Implementing",
			Prompt:    "Implement {{ticket}}, now in {{status}}; done work goes to {{on_success}}, trouble to {{on_failure}}.",
			OnSuccess: "Agent Review",
			OnFailure: "Needs Attention",
		},
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
	want := "prompt=Implement LERP-42, now in Implementing; done work goes to Agent Review, trouble to Needs Attention.\n" +
		"ticket=LERP-42\nenv=LERP-42\n"
	if string(got) != want {
		t.Errorf("log = %q, want %q", got, want)
	}
}

// Substitution happens in one pass, so a value can never be re-read as a
// template. Prompt prose is the value most likely to name a placeholder — the
// prose that tells an agent what {{workdir}} means is prose about lerp — and a
// second pass over the quoted prompt would splice a quoted path into it and
// end the quote mid-argument, splitting one argument into several or leaving
// the command unparseable. The runner prints its whole argument list, so a
// prompt that arrives as one argument, verbatim, is the proof.
func TestExecuteDoesNotExpandPlaceholdersInsideSubstitutedValues(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "runner.sh", `
printf 'argc=%s\n' "$#"
printf 'prompt=%s\n' "$1"
`)
	logPath := filepath.Join(dir, "runner.log")

	if _, err := Execute(context.Background(), Invocation{
		Runner: config.Runner{Command: shellQuote(script) + " {{prompt}} {{session}}"},
		Queue: config.Queue{
			Prompt: "Work {{ticket}} in {{workdir}}; resume with {{session}}.",
		},
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
	want := "argc=2\n" +
		"prompt=Work LERP-42 in {{workdir}}; resume with {{session}}.\n"
	if string(got) != want {
		t.Errorf("log = %q, want %q", got, want)
	}
}

// Adoption needs the PID of a process that is still alive: Started reports it
// as soon as the runner is spawned, not after it exits.
func TestExecuteReportsPIDOnceStarted(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "runner.sh", "exit 0")
	var pid int
	if _, err := Execute(context.Background(), Invocation{
		Runner:  config.Runner{Command: shellQuote(script)},
		Queue:   config.Queue{Prompt: "prompt"},
		Ticket:  "LERP-1",
		Workdir: dir,
		LogPath: filepath.Join(dir, "runner.log"),
		Started: func(p int) { pid = p },
	}); err != nil {
		t.Fatal(err)
	}
	if pid <= 0 {
		t.Errorf("Started reported PID %d, want the runner's positive PID", pid)
	}
}

func TestExecuteRequiresATicket(t *testing.T) {
	dir := t.TempDir()
	_, err := Execute(context.Background(), Invocation{
		Runner:  config.Runner{Command: "true"},
		Queue:   config.Queue{Prompt: "prompt"},
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
			Queue:   config.Queue{Prompt: childPIDPath},
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

// The whole point of the epilogue: a run leaves its exit status on disk for a
// lerp that was not its parent, without changing the code Execute itself
// reports. The workspace is deliberately somewhere other than the exit file's
// directory — the wrapper runs with cwd set to the workspace, and a relative
// path would land the status in the wrong place.
func TestExecuteRecordsItsOwnExitStatus(t *testing.T) {
	for _, want := range []int{0, 3} {
		t.Run(strconv.Itoa(want), func(t *testing.T) {
			dir := t.TempDir()
			workdir := t.TempDir()
			script := writeScript(t, dir, "runner.sh", "exit "+strconv.Itoa(want)+"\n")
			exitPath := filepath.Join(dir, "exit")

			result, err := Execute(context.Background(), Invocation{
				Runner:   config.Runner{Command: shellQuote(script)},
				Ticket:   "LERP-1",
				Workdir:  workdir,
				LogPath:  filepath.Join(dir, "runner.log"),
				ExitPath: exitPath,
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.ExitCode != want {
				t.Errorf("ExitCode = %d, want %d — the epilogue must not change what Execute reports", result.ExitCode, want)
			}
			recorded, err := os.ReadFile(exitPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(string(recorded)) != strconv.Itoa(want) {
				t.Errorf("exit file = %q, want %d", recorded, want)
			}
		})
	}
}

// The configured command is a quoted word to the wrapping shell, never text
// pasted in ahead of the epilogue — so nothing a template ends in can parse as
// one construct with what follows it. A trailing comment would swallow the
// epilogue; a trailing `&&` would join it, leaving `s` unset and reporting a
// broken command as a clean exit 0; a trailing `exec` would replace the shell
// that has to write the file. Each of these must still report the status the
// command itself ended with.
func TestExecuteRecordsItsExitStatusWhateverTheCommandEndsIn(t *testing.T) {
	for _, tc := range []struct {
		name   string
		suffix string
		want   int
		// wantAny takes any non-zero status instead of a fixed one, for the
		// case where the number itself is the shell's business.
		wantAny bool
	}{
		{name: "trailing comment", suffix: " # run the agent", want: 5},
		// A syntax error in the configured command is the inner shell's, and
		// it exits non-zero for it — as it did before there was any wrapper.
		// Which non-zero is unspecified by POSIX (2 in bash and dash, 3 in
		// ksh93, 1 in zsh's sh mode), so the claim under test is that the
		// command was a quoted word and whatever status it ended with was
		// recorded faithfully — not the number.
		{name: "trailing and", suffix: " &&", wantAny: true},
		{name: "leading exec", suffix: "", want: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			script := writeScript(t, dir, "runner.sh", "exit 5\n")
			command := shellQuote(script) + tc.suffix
			if tc.name == "leading exec" {
				command = "exec " + command
			}
			exitPath := filepath.Join(dir, "exit")

			result, err := Execute(context.Background(), Invocation{
				Runner:   config.Runner{Command: command},
				Ticket:   "LERP-1",
				Workdir:  dir,
				LogPath:  filepath.Join(dir, "runner.log"),
				ExitPath: exitPath,
			})
			if err != nil {
				t.Fatal(err)
			}
			want := tc.want
			switch {
			case tc.wantAny && result.ExitCode == 0:
				t.Errorf("ExitCode = 0, want any non-zero: the command was a syntax error")
			case tc.wantAny:
				want = result.ExitCode
			case result.ExitCode != tc.want:
				t.Errorf("ExitCode = %d, want %d", result.ExitCode, tc.want)
			}
			recorded, err := os.ReadFile(exitPath)
			if err != nil {
				t.Fatalf("no exit status was recorded: %v", err)
			}
			if strings.TrimSpace(string(recorded)) != strconv.Itoa(want) {
				t.Errorf("exit file = %q, want %d — the status Execute reported", recorded, want)
			}
		})
	}
}

// Without an exit path there is no wrapper and nothing to record: the dev
// harness that owns no run evidence must not start writing status files into
// somebody else's directory.
func TestExecuteWithoutAnExitPathRecordsNothing(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "runner.sh", "exit 4\n")

	result, err := Execute(context.Background(), Invocation{
		Runner:  config.Runner{Command: shellQuote(script)},
		Ticket:  "LERP-1",
		Workdir: dir,
		LogPath: filepath.Join(dir, "runner.log"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 4 {
		t.Errorf("ExitCode = %d, want 4", result.ExitCode)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == "exit" {
			t.Errorf("an exit file was written for an invocation that asked for none")
		}
	}
}

// Reap trusts an absent exit file to mean "this run never finished", and that
// is only sound because the process lerp recorded a PID for is the wrapping
// shell — the very thing that writes the file — rather than the agent itself.
// So the recorded PID must not be the agent's: if sh exec-optimized itself
// away, Alive would go false while the status was still unwritten.
func TestExecuteRecordsThePIDOfTheShellThatWritesTheStatus(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "runner.sh", "printf '%s' \"$$\"\n")
	logPath := filepath.Join(dir, "runner.log")
	var recordedPID int

	if _, err := Execute(context.Background(), Invocation{
		Runner:   config.Runner{Command: shellQuote(script)},
		Ticket:   "LERP-1",
		Workdir:  dir,
		LogPath:  logPath,
		ExitPath: filepath.Join(dir, "exit"),
		Started:  func(pid int) { recordedPID = pid },
	}); err != nil {
		t.Fatal(err)
	}
	agentPID, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(agentPID) == strconv.Itoa(recordedPID) {
		t.Errorf("recorded PID %d is the agent's own, want the wrapping shell's", recordedPID)
	}
}

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// A caller that has to record the session before the agent starts — eject
// can only resume a run whose id is on disk — supplies it on the invocation,
// and Execute substitutes exactly that, minting nothing.
func TestExecuteUsesTheCallersSessionID(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "runner.sh", `printf 'session=%s\n' "$1"`)
	logPath := filepath.Join(dir, "runner.log")

	result, err := Execute(context.Background(), Invocation{
		Runner:    config.Runner{Command: shellQuote(script) + " {{session}}"},
		Queue:     config.Queue{Prompt: "prompt"},
		Ticket:    "LERP-1",
		Workdir:   dir,
		LogPath:   logPath,
		SessionID: "1e9a4a0e-0000-4000-8000-00000000abcd",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionID != "1e9a4a0e-0000-4000-8000-00000000abcd" {
		t.Errorf("SessionID = %q, want the caller's own", result.SessionID)
	}
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := "session=1e9a4a0e-0000-4000-8000-00000000abcd\n"; string(got) != want {
		t.Errorf("log = %q, want %q", got, want)
	}
}

// The rule about when a session exists at all lives in NewSessionID, so the
// caller that records one before the run and Execute itself can never
// disagree: a command that asks for {{session}} gets a UUID, one that does
// not gets nothing to record.
func TestNewSessionIDOnlyWhenTheCommandAsks(t *testing.T) {
	id, err := NewSessionID(config.Runner{Command: "agent -p {{prompt}} --session-id {{session}}"})
	if err != nil {
		t.Fatal(err)
	}
	if !uuidPattern.MatchString(id) {
		t.Errorf("NewSessionID = %q, want a version 4 UUID", id)
	}
	id, err = NewSessionID(config.Runner{Command: "agent -p {{prompt}}"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "" {
		t.Errorf("NewSessionID = %q for a command with no {{session}}, want empty", id)
	}
}

// The resume command is pasted into a shell by a human, so it is quoted the
// way Execute quotes the command it ran — a workspace path with a space in it
// resumes, and nothing a ticket carries can add a second command to it.
func TestResumeCommandExpandsAndQuotes(t *testing.T) {
	runner := config.Runner{Command: "agent {{session}}", Resume: "agent --resume {{session}} --ticket {{ticket}} --cwd {{workdir}}"}
	record := evidence.Record{
		SessionID: "1e9a4a0e-0000-4000-8000-00000000abcd",
		Ticket:    "LERP-42; rm -rf /",
		Workspace: "/tmp/lerp work/lane 1",
	}
	want := "agent --resume '1e9a4a0e-0000-4000-8000-00000000abcd' " +
		"--ticket 'LERP-42; rm -rf /' --cwd '/tmp/lerp work/lane 1'"
	if got := ResumeCommand(runner, record); got != want {
		t.Errorf("ResumeCommand = %q, want %q", got, want)
	}
}

// A runner with no resume template is what makes a run un-ejectable, and the
// empty command is how that is reported.
func TestResumeCommandIsEmptyWithoutATemplate(t *testing.T) {
	got := ResumeCommand(config.Runner{Command: "agent {{session}}"},
		evidence.Record{SessionID: "1e9a4a0e-0000-4000-8000-00000000abcd"})
	if got != "" {
		t.Errorf("ResumeCommand = %q for a runner with no resume, want empty", got)
	}
}

// The Linear API key is lerp's own credential; an agent's Linear access is
// meant to come through its own authorization. What the child process can
// actually see is the only proof it was dropped, so the stub prints its whole
// environment — and nothing here ever prints that log back, since a failure
// in CI would publish every other variable the operator was running with.
func TestExecuteDropsTheLinearAPIKeyFromTheRunnerEnvironment(t *testing.T) {
	t.Setenv(childenv.LinearAPIKeyEnv, "lin_api_secret")
	dir := t.TempDir()
	script := writeScript(t, dir, "runner.sh", "env")
	logPath := filepath.Join(dir, "runner.log")

	if _, err := Execute(context.Background(), Invocation{
		Runner:  config.Runner{Command: shellQuote(script)},
		Queue:   config.Queue{Prompt: "prompt"},
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
	if names := envNames(string(got)); slices.Contains(names, childenv.LinearAPIKeyEnv) {
		t.Errorf("runner environment contains %s, want it dropped", childenv.LinearAPIKeyEnv)
	}
	if strings.Contains(string(got), "lin_api_secret") {
		t.Error("runner environment contains the Linear API key's value")
	}
	// The same environment still carries what lerp does hand the runner, and
	// what it inherited: an Inherited that returned only the extras would
	// pass every assertion above and leave every real agent without a PATH.
	if !strings.Contains(string(got), TicketEnv+"=LERP-42") {
		t.Errorf("runner environment does not carry %s=LERP-42", TicketEnv)
	}
	if !slices.Contains(envNames(string(got)), "PATH") {
		t.Error("runner environment has no PATH; it inherited nothing")
	}
}

// envNames reads the variable names out of `env` output. Only names: a test
// that reports what it found must not report the values beside them.
func envNames(output string) []string {
	var names []string
	for _, line := range strings.Split(output, "\n") {
		if name, _, ok := strings.Cut(line, "="); ok {
			names = append(names, name)
		}
	}
	return names
}
