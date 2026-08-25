package main

import (
	"context"
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
	if err := loop.VerifyStatuses(context.Background(), seedBoard(), repo); err != nil {
		t.Fatal(err)
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
