package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/evidence"
	"github.com/mattwalters/lerp/internal/logfmt"
	"github.com/mattwalters/lerp/internal/loop"
)

// The failure worth guarding against is a tape that silently stops working
// while nobody notices. Rendering needs vhs, so `make check` cannot run the
// cast; what it can do cheaply is hold the two fixtures the render depends on
// to the contracts the rest of lerp reads them under.

// TestBoardConfigLoads pins the embedded pipeline to the loader the harness
// puts it through, and to the board it is seeded against. A status renamed in
// one place and not the other would otherwise show up as a queue that is
// simply empty for the whole recording.
func TestBoardConfigLoads(t *testing.T) {
	repo, err := config.ParseRepoConfig(boardTOML, "board.toml")
	if err != nil {
		t.Fatalf("parse board.toml: %v", err)
	}
	if !slices.Equal(repo.Teams, []string{demoTeam}) {
		t.Fatalf("board.toml serves %v, want [%s]", repo.Teams, demoTeam)
	}
	warnings, err := loop.Verify(context.Background(), seedBoard(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("Verify warned about the demo board: %v", warnings)
	}
}

// kindNames makes a failure here name the kind that went missing rather than
// its ordinal.
var kindNames = map[logfmt.Kind]string{
	logfmt.KindInit: "init", logfmt.KindThinking: "thinking", logfmt.KindText: "text",
	logfmt.KindToolCall: "tool call", logfmt.KindToolResult: "tool result",
	logfmt.KindResult: "result",
}

// TestFixtureDecodesAsClaude pins the replayed log to the decoder the log pane
// picks for it. A fixture that stopped being detected would still render — as
// raw JSON — which is exactly the kind of rot a cast nobody re-watches hides.
func TestFixtureDecodesAsClaude(t *testing.T) {
	var s logfmt.Stream
	events := s.Feed([]byte(agentFixture))
	if s.Raw() {
		t.Fatal("the fixture sniffed as raw text; the log pane would show JSON")
	}
	var kinds []logfmt.Kind
	for _, ev := range events {
		kinds = append(kinds, ev.Kind)
	}
	for kind, name := range kindNames {
		if !slices.Contains(kinds, kind) {
			t.Errorf("the fixture decodes to no %s event; the cast would not show one", name)
		}
	}
	// Every lane replays the same file, so the ticket is a placeholder
	// agent.sh substitutes per run. One left in would put "__TICKET__" on the
	// pane in front of a reader.
	if !strings.Contains(agentFixture, "__TICKET__") {
		t.Error("the fixture names no ticket; agent.sh has nothing to substitute")
	}
}

// TestTheHarnessWiresEveryOptionTheTUIRequires is the guard for the rot that
// took docs/demo.gif out once already: internal/demo stopped passing a
// Starter when force-start landed, tui.Run refused the Options, and the cast
// became a recording of a shell error — which vhs exits 0 on, so the demo job
// stayed green and nothing said the README's asset was blank.
//
// Options is a struct, so the next required field is not a compile error
// either. This holds the harness's own wiring to the same validation Run
// runs, without needing a terminal.
func TestTheHarnessWiresEveryOptionTheTUIRequires(t *testing.T) {
	repo, err := config.ParseRepoConfig(boardTOML, "board.toml")
	if err != nil {
		t.Fatalf("parse board.toml: %v", err)
	}
	rec, err := loop.NewReconciler(loop.ReconcilerOptions{
		Client:   seedBoard(),
		Repo:     repo,
		RepoDir:  t.TempDir(),
		Evidence: evidence.New(t.TempDir()),
		Lanes:    lanes,
		Interval: interval,
		Events:   func(loop.Event) {},
	})
	if err != nil {
		t.Fatalf("build the reconciler the harness builds: %v", err)
	}
	events := make(chan loop.Event)
	if err := tuiOptions(rec, repo, events).Validate(); err != nil {
		t.Fatalf("the harness would render a cast of this error instead of lerp: %v", err)
	}
}

// TestTheHarnessReportsItsExitStatus pins the file `make demo` gates on. The
// harness runs inside the terminal vhs records, so its exit code stops at
// bash and vhs exits 0 either way; this file is the only thing carrying the
// harness's own verdict out of the recording, and a render whose status says
// anything but 0 is a cast of an error message.
func TestTheHarnessReportsItsExitStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exit")
	t.Setenv(exitEnv, path)

	reportExit(nil)
	if got := readReport(t, path); got != "0\n" {
		t.Errorf("a run that returned no error reported %q, want %q", got, "0\n")
	}
	reportExit(errors.New("the harness died at startup"))
	if got := readReport(t, path); got != "1\n" {
		t.Errorf("a failed run reported %q, want %q — make demo would pass it", got, "1\n")
	}

	// Unset, nothing is written: `go run ./internal/demo` by hand leaves no
	// file behind, and the Makefile's own rm is what makes a stale one from a
	// previous render unable to answer for this one.
	//
	// Unsetenv rather than Setenv(""): those are different states, and only
	// the first is the by-hand run this is pinning. t.Setenv above has already
	// registered the restore, so removing it here is still undone at the end.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Unsetenv(exitEnv); err != nil {
		t.Fatal(err)
	}
	reportExit(nil)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a run nobody asked about wrote a status anyway: %v", err)
	}
}

func readReport(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the harness reported no exit status: %v", err)
	}
	return string(b)
}
