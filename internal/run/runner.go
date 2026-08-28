//go:build unix

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

	"github.com/mattwalters/lerp/internal/childenv"
	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/evidence"
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
	// SessionID, when set, is the session the runner is told to open, rather
	// than one minted here. A caller that has to be able to resume the run
	// later — eject hands over the runner's own resume command — must know
	// the id before the agent starts, so it can be recorded with the run;
	// an id first returned in Result would be lost with the process.
	SessionID string

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
// The command inherits lerp's environment, minus the operator's Linear API
// key: see internal/childenv.
//
// A session ID is generated and returned only when the command uses
// {{session}}, and only when inv.SessionID is empty: a caller that minted one
// itself gets that one substituted, unchanged.
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

	sessionID := inv.SessionID
	if sessionID == "" {
		var err error
		sessionID, err = NewSessionID(inv.Runner)
		if err != nil {
			return result, fmt.Errorf("generating runner session ID: %w", err)
		}
	}
	result.SessionID = sessionID

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
	log, err := os.OpenFile(inv.LogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return result, fmt.Errorf("opening runner log: %w", err)
	}
	defer log.Close()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = inv.Workdir
	cmd.Env = childenv.Inherited(TicketEnv + "=" + inv.Ticket)
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
// quoted for /bin/sh so ticket text cannot alter the configured command. A
// resume template is expanded through here too, with no prompt: resuming
// hands the operator the session, and the session already holds the prompt.
//
// One pass, via strings.NewReplacer, which never rescans what it has already
// written — the same rule config.Queue.ExpandPrompt follows. Replacing one
// placeholder at a time would let a later pass see text that came from an
// earlier value: a literal {{workdir}} carried in the prompt would have a
// quoted path spliced into the already-quoted prompt, ending the quote
// mid-string. Nothing lerp substitutes today contains a placeholder, so this
// is the shape holding rather than a bug being fixed.
func expand(command, prompt, ticket, workdir, sessionID string) string {
	return strings.NewReplacer(
		"{{prompt}}", shellQuote(prompt),
		"{{ticket}}", shellQuote(ticket),
		"{{workdir}}", shellQuote(workdir),
		"{{session}}", shellQuote(sessionID),
	).Replace(command)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

// ResumeCommand is the runner's own resume command for a recorded run: what
// eject hands the operator so the headless run becomes their interactive
// session. It returns ("", nil) for a runner with no resume template, which
// is what makes that runner un-ejectable. It errors when the run's session
// cannot be resolved — record.SessionID empty and, for a vendor that names
// its own, nothing found in the run's log either — rather than handing back
// a resume command with nothing to resume.
//
// The same substitution Execute makes, quoted the same way, so what the
// operator pastes is what lerp would have run. {{ticket}} is the human
// identifier the run was started for, {{workdir}} its workspace — the one
// lerp deliberately leaves behind on eject.
func ResumeCommand(runner config.Runner, record evidence.Record) (string, error) {
	if runner.Resume == "" {
		return "", nil
	}
	sessionID, err := resolveSession(runner, record)
	if err != nil {
		return "", err
	}
	return expand(runner.Resume, "", record.Ticket, record.Workspace, sessionID), nil
}

// OpensSession reports whether runs under this runner get a session id lerp
// chose — the one thing that can later be resumed. It is the rule NewSessionID
// applies, asked ahead of a run: a caller deciding whether a run could ever be
// ejected must get the same answer as the run itself.
//
// It is not the only way a run becomes resumable: a vendor whose CLI names
// its own session instead of accepting one lerp mints is resumable without
// ever opening one — see CapturesSession.
func OpensSession(runner config.Runner) bool {
	return strings.Contains(runner.Command, "{{session}}")
}

// NewSessionID returns the session id a run under this runner should open
// with, or "" for a runner whose command never asks for one. The rule lives
// here alone: a session id exists exactly when the command template has a
// {{session}} to put it in, whether Execute mints it or a caller that has to
// record it first does.
//
// The UUID shape is not decoration: agent CLIs that accept a caller-chosen
// session id tend to require one. Claude Code's --session-id rejects anything
// that is not a valid UUID, so a bare hex string would make {{session}} — and
// with it the resume command that eject hands over — unusable.
func NewSessionID(runner config.Runner) (string, error) {
	if !OpensSession(runner) {
		return "", nil
	}
	return newUUID()
}

// newUUID returns a random RFC 4122 version 4 UUID.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
