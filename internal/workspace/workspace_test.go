package workspace

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattwalters/lerp/internal/childenv"
)

func TestProvisionPassesIdentityAndLogsOutput(t *testing.T) {
	repoDir := t.TempDir()
	repoDir, err := filepath.EvalSymlinks(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	script := writeScript(t, repoDir, "provision", `
printf 'identity=%s,%s,%s\n' "$LERP_LANE" "$LERP_TICKET_ID" "$LERP_WORKSPACE"
printf 'directory=%s\n' "$PWD"
printf 'provision stderr\n' >&2
`)

	var log bytes.Buffer
	id := Identity{Lane: 2, TicketID: "issue-123", Workspace: "/tmp/lerp-2"}
	if err := Provision(context.Background(), repoDir, script, id, &log); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	got := log.String()
	for _, want := range []string{
		"identity=2,issue-123,/tmp/lerp-2",
		"directory=" + repoDir,
		"provision stderr",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log = %q, want %q", got, want)
		}
	}
}

func TestProvisionFailureIsReturnedAndLogged(t *testing.T) {
	script := writeScript(t, t.TempDir(), "fail", `
printf 'cannot provision\n' >&2
exit 7
`)

	var log bytes.Buffer
	err := Provision(context.Background(), t.TempDir(), script, Identity{}, &log)
	if err == nil {
		t.Fatal("Provision returned nil, want error")
	}
	if !strings.Contains(log.String(), "cannot provision") || !strings.Contains(log.String(), "provision workspace") {
		t.Errorf("log = %q, want command and provision failures", log.String())
	}
}

func TestDisposeFailureIsLoggedAndIgnored(t *testing.T) {
	script := writeScript(t, t.TempDir(), "fail", `
printf 'cannot dispose\n' >&2
exit 9
`)

	var log bytes.Buffer
	Dispose(context.Background(), t.TempDir(), script, Identity{}, &log)
	if got := log.String(); !strings.Contains(got, "cannot dispose") || !strings.Contains(got, "dispose workspace") {
		t.Errorf("log = %q, want command and dispose failures", got)
	}
}

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// Provision and dispose run whatever the repo configures, from the repo root,
// before and after every agent — so the operator's Linear credential must be
// as absent from their environment as it is from the runner's. The stub
// prints its whole environment, and nothing here prints that log back: a
// failure in CI would publish every other variable lerp was running with.
func TestWorkspaceCommandsDropTheLinearAPIKey(t *testing.T) {
	t.Setenv(childenv.LinearAPIKeyEnv, "lin_api_secret")
	repoDir := t.TempDir()
	script := writeScript(t, repoDir, "print-env.sh", "env\n")

	var log bytes.Buffer
	id := Identity{Lane: 2, TicketID: "issue-123", Workspace: "/tmp/lerp-2"}
	if err := Provision(context.Background(), repoDir, script, id, &log); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	Dispose(context.Background(), repoDir, script, id, &log)

	got := log.String()
	for _, line := range strings.Split(got, "\n") {
		if name, _, ok := strings.Cut(line, "="); ok && name == childenv.LinearAPIKeyEnv {
			t.Errorf("workspace environment contains %s, want it dropped", childenv.LinearAPIKeyEnv)
		}
	}
	if strings.Contains(got, "lin_api_secret") {
		t.Error("workspace environment contains the Linear API key's value")
	}
	// The same environment still carries what lerp does hand the command, and
	// what it inherited: an Inherited that returned only the extras would
	// pass every assertion above and leave provision without a PATH.
	if !strings.Contains(got, TicketIDEnv+"=issue-123") {
		t.Errorf("workspace environment does not carry %s=issue-123", TicketIDEnv)
	}
	if !strings.Contains(got, "\nPATH=") {
		t.Error("workspace environment has no PATH; it inherited nothing")
	}
}

// Done-when (LERP-110): no LINEAR_* credential reaches workspace commands
// under either auth mode (API key or OAuth).
func TestWorkspaceCommandsNoLinearCredentialsUnderEitherAuthMode(t *testing.T) {
	for _, mode := range []string{"api_key", "oauth"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "api_key" {
				t.Setenv(childenv.LinearAPIKeyEnv, "lin_api_secret")
			} else {
				t.Setenv(childenv.LinearAPIKeyEnv, "")
			}
			repoDir := t.TempDir()
			script := writeScript(t, repoDir, "print-env.sh", "env\n")

			var log bytes.Buffer
			id := Identity{Lane: 2, TicketID: "issue-123", Workspace: "/tmp/lerp-2"}
			if err := Provision(context.Background(), repoDir, script, id, &log); err != nil {
				t.Fatalf("Provision: %v", err)
			}
			Dispose(context.Background(), repoDir, script, id, &log)

			got := log.String()
			for _, line := range strings.Split(got, "\n") {
				if name, _, ok := strings.Cut(line, "="); ok && strings.HasPrefix(name, "LINEAR_") {
					t.Errorf("workspace environment contains %s under %s mode", name, mode)
				}
			}
			if strings.Contains(got, "lin_api_secret") {
				t.Errorf("workspace environment contains API key secret under %s mode", mode)
			}
		})
	}
}
