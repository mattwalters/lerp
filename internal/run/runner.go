package run

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mattwalters/lerp/internal/config"
)

// Result is the outcome of one runner invocation. A non-zero ExitCode is a
// completed run, not an execution error; callers use it to choose the queue's
// success or failure status.
type Result struct {
	ExitCode  int
	Duration  time.Duration
	SessionID string
}

// TicketEnv names the environment variable carrying the ticket identifier into
// the runner process, for commands and prompts that would rather read the
// environment than interpolate {{ticket}}.
const TicketEnv = "LERP_TICKET"

// Invocation is one runner call: the configured command, the queue this run
// executes for (its prompt and the status pointers the prompt may reference),
// and the ticket and paths this run belongs to.
type Invocation struct {
	Runner  config.Runner
	Queue   config.Queue
	Ticket  string // human identifier, e.g. LERP-42
	Workdir string
	LogPath string
	// ExitPath, when set, is where the run records its own exit status, for a
	// lerp that was not the agent's parent and so can never wait() for it.
	// Empty means no wrapper at all and byte-for-byte the configured command.
	ExitPath string

	// Started, when set, is called once with the runner's PID as soon as the
	// process exists, before it is waited on. It is how run evidence learns
	// the PID of an agent that is still alive; a PID first reported after
	// exit would be useless for adoption.
	Started func(pid int)
}

// Execute expands inv.Runner.Command and runs it in inv.Workdir, writing the
// runner's combined stdout and stderr to inv.LogPath.
//
// {{ticket}}, {{prompt}}, {{workdir}} and {{session}} are shell-escaped before
// substitution into the command. The prompt itself is expanded first, by
// config.Queue.ExpandPrompt: {{ticket}} names the ticket the run is about —
// without that, every ticket in a queue would reach its agent as the same
// anonymous instruction — and {{status}}, {{on_success}}, and {{on_failure}}
// carry the queue's own configured statuses into the prose. The ticket
// identifier is also exported as TicketEnv.
//
// A session ID is generated and returned only when the command uses
// {{session}}.
//
// When inv.ExitPath is set, the command is wrapped in a shell that writes the
// command's own exit status to that file and then exits with it, so a
// successor process can learn how a run it did not start ended.
//
// The command runs in its own process group. Cancelling ctx kills that entire
// group, so child processes do not outlive the runner.
func Execute(ctx context.Context, inv Invocation) (Result, error) {
	result := Result{ExitCode: -1}
	if inv.Ticket == "" {
		return result, errors.New("runner invocation requires a ticket")
	}

	sessionID := ""
	if strings.Contains(inv.Runner.Command, "{{session}}") {
		var err error
		sessionID, err = newSessionID()
		if err != nil {
			return result, fmt.Errorf("generating runner session ID: %w", err)
		}
		result.SessionID = sessionID
	}

	// Placeholder values go into the prompt unquoted: expand shell-escapes the
	// whole prompt when it substitutes it into the command.
	prompt := inv.Queue.ExpandPrompt(inv.Ticket)
	command := expand(inv.Runner.Command, prompt, inv.Ticket, inv.Workdir, sessionID)
	if inv.ExitPath != "" {
		var err error
		command, err = withExitStatus(command, inv.ExitPath)
		if err != nil {
			return result, err
		}
	}
	log, err := os.OpenFile(inv.LogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return result, fmt.Errorf("opening runner log: %w", err)
	}
	defer log.Close()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = inv.Workdir
	cmd.Env = append(os.Environ(), TicketEnv+"="+inv.Ticket)
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	started := time.Now()
	if err := cmd.Start(); err != nil {
		return result, fmt.Errorf("starting runner: %w", err)
	}
	if inv.Started != nil {
		inv.Started(cmd.Process.Pid)
	}
	err = cmd.Wait()
	result.Duration = time.Since(started)
	if err == nil {
		result.ExitCode = 0
		return result, nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return result, fmt.Errorf("waiting for runner: %w", err)
	}
	result.ExitCode = exitErr.ExitCode()
	return result, nil
}

// withExitStatus wraps the configured command in a shell that records the
// command's own exit status. The path is made absolute first — the command
// runs with cwd set to the workspace, while the path points into the clone's
// .lerp/runs — and quoted like every other substitution.
//
// The command is handed to a nested `sh -c` as a single quoted word rather
// than pasted in ahead of the epilogue, so no configured text can parse as one
// construct with what follows it. Appending would not be safe: a template
// ending in a comment would swallow the epilogue, and one ending in `&&` — a
// plausible typo — would join it, leaving `s` unset and reporting a broken
// command as a clean exit 0. Nesting also means a template that ends in `exec`
// replaces only the inner shell, so the status is still written.
//
// `exit $s` matters as much as the file: without it Execute's own Wait() would
// report the echo's status, changing the result of every live run.
//
// One consequence is worth naming. The recorded PID is now the outer shell —
// the very process that writes the file — rather than the agent. Alive
// therefore goes false only after the status is on disk, which is what lets a
// reader trust an absent file to mean "this run never finished". Both kill
// sites signal the process group, so the extra shell changes nothing there.
func withExitStatus(command, exitPath string) (string, error) {
	abs, err := filepath.Abs(exitPath)
	if err != nil {
		return "", fmt.Errorf("resolving runner exit path: %w", err)
	}
	return "sh -c " + shellQuote(command) + "\ns=$?\necho \"$s\" > " + shellQuote(abs) + "\nexit $s\n", nil
}

// expand replaces the supported command-template placeholders. Values are
// quoted for /bin/sh so ticket text cannot alter the configured command.
func expand(command, prompt, ticket, workdir, sessionID string) string {
	command = strings.ReplaceAll(command, "{{prompt}}", shellQuote(prompt))
	command = strings.ReplaceAll(command, "{{ticket}}", shellQuote(ticket))
	command = strings.ReplaceAll(command, "{{workdir}}", shellQuote(workdir))
	return strings.ReplaceAll(command, "{{session}}", shellQuote(sessionID))
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

// newSessionID returns a random RFC 4122 version 4 UUID.
//
// The UUID shape is not decoration: agent CLIs that accept a caller-chosen
// session id tend to require one. Claude Code's --session-id rejects anything
// that is not a valid UUID, so a bare hex string would make {{session}} — and
// with it the resume command that eject hands over — unusable.
func newSessionID() (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
