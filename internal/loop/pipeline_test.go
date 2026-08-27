//go:build unix

package loop

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/linear"
)

// conclude is the one place that decides whether a finished run releases its
// claim, so the rule is tested here rather than through whichever caller
// happens to reach it. An assigned ticket is never eligible: a ticket that
// keeps its claim through a move into a served status is stranded there
// permanently, with nothing reporting an error (LERP-50, LERP-59, LERP-113).

// A ticket that fails with nowhere to go keeps its claim, so the next pass
// does not pick it straight back up and re-run it forever.
func TestConcludeFailureWithoutRouteKeepsTheClaim(t *testing.T) {
	ctx := context.Background()
	fake := linear.NewFake()
	fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})
	repo := testRepo()
	queue := repo.Queues["todo"]
	queue.OnFailure = ""
	repo.Queues["todo"] = queue

	issue, viewerID := claimed(t, fake, "one", queue.Status)
	if _, _, err := conclude(ctx, fake, issue, queue, repo, 3, viewerID, nil); err != nil {
		t.Fatalf("conclude: %v", err)
	}
	got, _ := fake.GetIssue(ctx, "one")
	if got.Status != "Todo" {
		t.Errorf("unrouted failure status = %q, want Todo", got.Status)
	}
	if got.AssigneeID == "" {
		t.Error("unrouted failure released the claim, so the ticket would be re-run immediately")
	}
	if Eligible(got, map[string]bool{"Todo": true}) {
		t.Error("unrouted failure left the ticket eligible, which spins the reconciler")
	}
}

// conclude's status return is telemetry's account of where a run's ticket
// came to rest, so it has to agree with what the board itself shows in every
// shape a run can settle: no route, a normal hop, a hop skipped for a status
// nobody named, and a takeover.
func TestConcludeReportsWhereTheTicketRested(t *testing.T) {
	ctx := context.Background()

	t.Run("no failure route", func(t *testing.T) {
		fake := linear.NewFake()
		fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})
		repo := testRepo()
		queue := repo.Queues["todo"]
		queue.OnFailure = ""
		repo.Queues["todo"] = queue

		issue, viewerID := claimed(t, fake, "one", queue.Status)
		_, final, err := conclude(ctx, fake, issue, queue, repo, 3, viewerID, nil)
		if err != nil {
			t.Fatalf("conclude: %v", err)
		}
		if final != "Todo" {
			t.Errorf("final = %q, want Todo", final)
		}
	})

	t.Run("the queue's own move rule", func(t *testing.T) {
		fake := linear.NewFake()
		fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})
		repo := testRepo()
		queue := repo.Queues["todo"]

		issue, viewerID := claimed(t, fake, "one", queue.Status)
		_, final, err := conclude(ctx, fake, issue, queue, repo, 0, viewerID, nil)
		if err != nil {
			t.Fatalf("conclude: %v", err)
		}
		if final != "Done" {
			t.Errorf("final = %q, want Done", final)
		}
	})

	t.Run("an agent's own move into another served status", func(t *testing.T) {
		fake := linear.NewFake()
		fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Planning"})
		repo := gatedRepo()
		queue := repo.Queues["plan"]

		issue, viewerID := claimed(t, fake, "one", queue.Status)
		if err := fake.MoveIssue(ctx, "one", "Implementing"); err != nil {
			t.Fatal(err)
		}
		_, final, err := conclude(ctx, fake, issue, queue, repo, 0, viewerID, nil)
		if err != nil {
			t.Fatalf("conclude: %v", err)
		}
		if final != "Implementing" {
			t.Errorf("final = %q, want Implementing", final)
		}
	})

	t.Run("a takeover mid-run", func(t *testing.T) {
		fake := linear.NewFake()
		fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})
		repo := testRepo()
		queue := repo.Queues["todo"]

		issue, viewerID := claimed(t, fake, "one", queue.Status)
		if err := fake.AssignIssue(ctx, "one", "somebody-else"); err != nil {
			t.Fatal(err)
		}
		_, final, err := conclude(ctx, fake, issue, queue, repo, 0, viewerID, nil)
		if err != nil {
			t.Fatalf("conclude: %v", err)
		}
		if final != "Todo" {
			t.Errorf("final = %q, want Todo (unmoved)", final)
		}
	})
}

// Finishing into a status no queue serves releases the claim. The gate is the
// status — no queue picks up from it, so the ticket rests there either way —
// and the inbox lists it unassigned exactly as it listed it claimed. What the
// claim did do was strand the ticket the moment anybody moved it on (LERP-113).
func TestConcludeReleasesTheClaimAtAGate(t *testing.T) {
	ctx := context.Background()
	fake := linear.NewFake()
	fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Todo"})
	repo := testRepo()
	queue := repo.Queues["todo"]

	issue, viewerID := claimed(t, fake, "one", queue.Status)
	if _, _, err := conclude(ctx, fake, issue, queue, repo, 0, viewerID, nil); err != nil {
		t.Fatalf("conclude: %v", err)
	}
	got, _ := fake.GetIssue(ctx, "one")
	if got.Status != "Done" {
		t.Fatalf("status = %q, want Done", got.Status)
	}
	if got.AssigneeID != "" {
		t.Error("a ticket parked at a gate kept its claim: every later move into a served status is stranded")
	}
}

// An agent that moves its own ticket into another queue's status must not
// leave it stranded: lerp respects the move and still releases the claim, so
// the queue serving that status can pick the ticket up.
func TestConcludeReleasesTheClaimWhenTheAgentMovedIntoAServedStatus(t *testing.T) {
	ctx := context.Background()
	fake := linear.NewFake()
	fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Planning"})
	// The agent's destination is not where the queue's rule would have sent
	// it: this run's clean exit routes to the "Plan Review" gate, so a
	// conclude that forced its hop would overwrite the move rather than
	// respect it, and the assertion below would catch it.
	repo := gatedRepo()
	queue := repo.Queues["plan"]

	issue, viewerID := claimed(t, fake, "one", queue.Status)
	// The agent's own move, made while the run was still going, into the
	// status the implement queue serves.
	if err := fake.MoveIssue(ctx, "one", "Implementing"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conclude(ctx, fake, issue, queue, repo, 0, viewerID, nil); err != nil {
		t.Fatalf("conclude: %v", err)
	}
	got, _ := fake.GetIssue(ctx, "one")
	if got.Status != "Implementing" {
		t.Fatalf("agent move was overwritten: status = %q", got.Status)
	}
	if !Eligible(got, map[string]bool{"Implementing": true}) {
		t.Errorf("ticket is stranded after the agent's move: %+v", got)
	}
}

// LERP-113's acceptance, from the pipeline's own two halves: a plan run parks
// its ticket at a gate, a human reads it and moves it on in Linear itself —
// the routing the manual documents, and the move `p` is not — and the next
// pass finds it as a candidate. Before the gate released its claim the
// listing came back empty and nothing reported why.
func TestATicketMovedOnFromAGateIsACandidateAgain(t *testing.T) {
	ctx := context.Background()
	fake := linear.NewFake()
	fake.AddIssue("LERP", linear.Issue{ID: "one", Identifier: "LERP-1", Status: "Planning"})
	repo := gatedRepo()
	queue := repo.Queues["plan"]

	issue, viewerID := claimed(t, fake, "one", queue.Status)
	if _, _, err := conclude(ctx, fake, issue, queue, repo, 0, viewerID, nil); err != nil {
		t.Fatalf("conclude: %v", err)
	}
	parked, _ := fake.GetIssue(ctx, "one")
	if parked.Status != "Plan Review" || parked.AssigneeID != "" {
		t.Fatalf("ticket at the gate = %+v, want it resting in Plan Review unclaimed", parked)
	}

	// The human promotes it the way Linear promotes anything: by moving it.
	if err := fake.MoveIssue(ctx, "one", "Implementing"); err != nil {
		t.Fatal(err)
	}
	listings, err := listQueues(ctx, fake, repo)
	if err != nil {
		t.Fatal(err)
	}
	cands := candidatesFrom(listings)
	if len(cands) != 1 || cands[0].issue.ID != "one" || cands[0].name != "implement" {
		t.Fatalf("candidates = %+v, want the ticket picked up by the implement queue", cands)
	}
}

// gatedRepo is chainedRepo's other shape: two stages with a gate between
// them, as the stock pipeline has. The plan queue's clean exit rests in
// "Plan Review", which no queue serves, and the implement queue serves only
// what a human moves on from there.
func gatedRepo() *config.RepoConfig {
	repo := testRepo()
	repo.Queues = map[string]config.Queue{
		"plan":      {Status: "Planning", Prompt: "plan it", Runner: "agent", OnSuccess: "Plan Review", OnFailure: "Needs Help"},
		"implement": {Status: "Implementing", Prompt: "build it", Runner: "agent", OnSuccess: "Done", OnFailure: "Needs Help"},
	}
	return repo
}

// claimed runs the claim protocol the way a lane does and returns the ticket
// as the run saw it, with the operating user conclude settles against.
func claimed(t *testing.T, fake *linear.Fake, issueID, status string) (linear.Issue, string) {
	t.Helper()
	ctx := context.Background()
	viewerID, won, err := claimForQueue(ctx, fake, issueID, status)
	if err != nil || !won {
		t.Fatalf("claimForQueue = (%v, %v), want the claim won", won, err)
	}
	issue, err := fake.GetIssue(ctx, issueID)
	if err != nil {
		t.Fatal(err)
	}
	return issue, viewerID
}

// statusFieldPage is the manual page carrying "Lerp needs the status field" —
// the adopter-facing account of an automation eating a stage's hop, and the
// only page that quotes the warning verbatim. It moved out of the README when
// the manual was written (LERP-123); the two tests below follow it.
const statusFieldPage = "docs/content/docs/install.md"

// The manual quotes this note as what an adopter sees when an automation has
// eaten a stage's hop — the string they will have in front of them, and the
// one they grep for. A quoted string with nothing holding it to its source
// goes stale on the first reword, with a green gate, and the page a surprised
// adopter reads is exactly the wrong place for a line that no longer matches.
// So it is pinned the way lerp.example.toml is pinned to the stock config: the
// whole quote, both ways.
//
// Both ways matters. Containment alone would catch a page that drifted from
// the code and miss the note losing its second sentence — the "an external
// automation may be moving tickets" hint, which is the whole diagnostic that
// page exists to explain — while it went on quoting it.
func TestSkippedHopNoteIsWhatTheManualQuotes(t *testing.T) {
	note := skippedHopNote(
		linear.Issue{Identifier: "LERP-42"},
		config.Queue{Status: "Implementing", OnSuccess: "In Review"},
		"on_success", "In Review", "In Progress",
		map[string]bool{"Implementing": true, "In Review": true},
	)
	quote := pageBlockquote(t, statusFieldPage)
	if quote != flatten(note) {
		t.Errorf("%s's blockquote is not what skippedHopNote produces.\ncode: %s\npage: %s\n\n"+
			"pipeline.go is the source. Change the note there, then update the\n"+
			"blockquote under \"Lerp needs the status field\" in %s.",
			statusFieldPage, flatten(note), quote, statusFieldPage)
	}
}

// The four trigger names the manual tells an adopter to look for are the same
// four the startup warning prints. They were wrong once already, in the code
// (LERP-55), and a rename that fixes one side and not the other sends the
// adopter to a settings row under a name the screen does not use.
func TestTheManualNamesTheMidStageTriggers(t *testing.T) {
	page := flatten(string(readFile(t, statusFieldPage)))
	for _, ev := range midStageEvents {
		if !strings.Contains(page, ev.label) {
			t.Errorf("%s never names the %q trigger the startup warning prints —\n"+
				"the settings row an adopter is sent to find must carry one name, not two",
				statusFieldPage, ev.label)
		}
	}
}

// pageBlockquote returns the page's one blockquote — the quoted status-bar
// line — flattened onto a single line, with its "> " markers and wrapping
// removed. A page with no blockquote, or more than one, fails here rather
// than passing vacuously.
func pageBlockquote(t *testing.T, name string) string {
	t.Helper()
	var quoted []string
	var blocks []string
	for _, line := range strings.Split(string(readFile(t, name)), "\n") {
		if after, ok := strings.CutPrefix(line, "> "); ok {
			quoted = append(quoted, after)
			continue
		}
		if len(quoted) > 0 {
			blocks = append(blocks, flatten(strings.Join(quoted, " ")))
			quoted = nil
		}
	}
	if len(quoted) > 0 {
		blocks = append(blocks, flatten(strings.Join(quoted, " ")))
	}
	if len(blocks) != 1 {
		t.Fatalf("%s has %d blockquotes, want the one holding the skipped-hop line", name, len(blocks))
	}
	return blocks[0]
}

// flatten collapses wrapping so a comparison is about words, not line breaks:
// the manual wraps its prose and the code does not.
func flatten(s string) string { return strings.Join(strings.Fields(s), " ") }

func readFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
