package run

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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

// Invocation is one runner call: the configured command, the queue's prompt,
// and the ticket and paths this run belongs to.
type Invocation struct {
	Runner  config.Runner
	Prompt  string
	Ticket  string // human identifier, e.g. LERP-42
	Workdir string
	LogPath string
}

// Execute expands inv.Runner.Command and runs it in inv.Workdir, writing the
// runner's combined stdout and stderr to inv.LogPath.
//
// {{ticket}}, {{prompt}}, {{workdir}} and {{session}} are shell-escaped before
// substitution into the command. {{ticket}} is also expanded inside the prompt,
// so a queue prompt can name the ticket it is about; without that, every ticket
// in a queue would reach its agent as the same anonymous instruction. The same
// identifier is exported as TicketEnv.
//
// A session ID is generated and returned only when the command uses
// {{session}}.
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

	// The ticket goes into the prompt unquoted: expand shell-escapes the whole
	// prompt when it substitutes it into the command.
	prompt := strings.ReplaceAll(inv.Prompt, "{{ticket}}", inv.Ticket)
	command := expand(inv.Runner.Command, prompt, inv.Ticket, inv.Workdir, sessionID)
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
	err = cmd.Run()
	result.Duration = time.Since(started)
	if err == nil {
		result.ExitCode = 0
		return result, nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return result, fmt.Errorf("starting runner: %w", err)
	}
	result.ExitCode = exitErr.ExitCode()
	return result, nil
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

func newSessionID() (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
