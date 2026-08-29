package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mattwalters/lerp/internal/childenv"
)

var buildBinary = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "lerp-cli-test-*")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "lerp")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("building lerp binary: %w\n%s", err, out)
	}
	return bin, nil
})

func lerpBinary(t *testing.T) string {
	t.Helper()
	bin, err := buildBinary()
	if err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestCLIDispatchGuards(t *testing.T) {
	bin := lerpBinary(t)

	tests := []struct {
		name         string
		args         []string
		wantExit     int
		wantStderr   []string
		stdoutPrefix string
	}{
		{
			name:       "bogus command",
			args:       []string{"bogus"},
			wantExit:   2,
			wantStderr: []string{`unknown command "bogus"`, "usage:"},
		},
		{
			name:       "zero concurrency",
			args:       []string{"-concurrency", "0"},
			wantExit:   2,
			wantStderr: []string{"-concurrency must be at least 1"},
		},
		{
			name:       "stray arg after flags",
			args:       []string{"-concurrency", "2", "stray"},
			wantExit:   2,
			wantStderr: []string{"usage:"},
		},
		{
			name:       "bare lerp without terminal",
			args:       nil,
			wantExit:   2,
			wantStderr: []string{"the TUI needs a terminal"},
		},
		{
			name:         "version",
			args:         []string{"version"},
			wantExit:     0,
			stdoutPrefix: "lerp",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(bin, tc.args...)
			cmd.Dir = t.TempDir()
			cmd.Env = childenv.Inherited()
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			err := cmd.Run()
			exitCode := 0
			if err != nil {
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					exitCode = exitErr.ExitCode()
				} else {
					t.Fatalf("running lerp: %v", err)
				}
			}

			if exitCode != tc.wantExit {
				t.Errorf("exit code = %d, want %d (stderr: %q, stdout: %q)", exitCode, tc.wantExit, stderr.String(), stdout.String())
			}
			for _, want := range tc.wantStderr {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("stderr %q does not contain %q", stderr.String(), want)
				}
			}
			if tc.stdoutPrefix != "" {
				if !strings.HasPrefix(strings.TrimSpace(stdout.String()), tc.stdoutPrefix) {
					t.Errorf("stdout %q does not start with %q", stdout.String(), tc.stdoutPrefix)
				}
			}
		})
	}
}

func TestInitNoCredentialsNonInteractive(t *testing.T) {
	bin := lerpBinary(t)

	tests := []struct {
		name string
		args []string
	}{
		{
			name: "yes flag",
			args: []string{"init", "--team", "LERP", "--yes"},
		},
		{
			name: "piped stdin",
			args: []string{"init", "--team", "LERP"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeHome := t.TempDir()
			cmd := exec.Command(bin, tc.args...)
			cmd.Dir = t.TempDir()
			cmd.Env = append(childenv.Inherited(),
				"HOME="+fakeHome,
				"XDG_CONFIG_HOME="+fakeHome,
			)
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			err := cmd.Run()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("exit = %v, want exit code 1", err)
			}
			if !strings.Contains(stderr.String(), "no Linear credentials") {
				t.Errorf("stderr %q does not contain %q", stderr.String(), "no Linear credentials")
			}
			if strings.Contains(stdout.String(), "Sign in to Linear") || strings.Contains(stderr.String(), "Sign in to Linear") {
				t.Errorf("prompt printed in non-interactive run: stdout=%q, stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}
