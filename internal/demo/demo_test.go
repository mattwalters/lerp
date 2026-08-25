package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/mattwalters/lerp/internal/config"
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
