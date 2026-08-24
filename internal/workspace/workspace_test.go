package workspace

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
