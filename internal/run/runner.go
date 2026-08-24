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

// Execute expands runner.Command and runs it in workdir. It writes the
// runner's combined stdout and stderr to logPath. {{prompt}}, {{workdir}}, and
// {{session}} are shell-escaped before substitution. A session ID is generated
// and returned only when the command uses {{session}}.
//
// The command runs in its own process group. Cancelling ctx kills that entire
// group, so child processes do not outlive the runner.
func Execute(ctx context.Context, runner config.Runner, prompt, workdir, logPath string) (Result, error) {
	result := Result{ExitCode: -1}

	sessionID := ""
	if strings.Contains(runner.Command, "{{session}}") {
		var err error
		sessionID, err = newSessionID()
		if err != nil {
			return result, fmt.Errorf("generating runner session ID: %w", err)
		}
		result.SessionID = sessionID
	}

	command := expand(runner.Command, prompt, workdir, sessionID)
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return result, fmt.Errorf("opening runner log: %w", err)
	}
	defer log.Close()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = workdir
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

// expand replaces the three supported command-template placeholders. Values
// are quoted for /bin/sh so ticket text cannot alter the configured command.
func expand(command, prompt, workdir, sessionID string) string {
	command = strings.ReplaceAll(command, "{{prompt}}", shellQuote(prompt))
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
