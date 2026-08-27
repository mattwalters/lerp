//go:build unix

package loop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/linear"
)

func TestVerifyStatusesPassesWhenEveryStatusExists(t *testing.T) {
	fake := linear.NewFake()
	fake.SetTeamStates("LERP", "Backlog", "Todo", "Done", "Needs Help")
	if err := verifyStatuses(context.Background(), fake, testRepo()); err != nil {
		t.Fatalf("verifyStatuses = %v, want nil", err)
	}
}

func TestVerifyStatusesNamesEveryMiss(t *testing.T) {
	fake := linear.NewFake()
	// The queue status exists; both move targets are missing.
	fake.SetTeamStates("LERP", "Backlog", "Todo", "Doen", "Halp")
	err := verifyStatuses(context.Background(), fake, testRepo())
	if err == nil {
		t.Fatal("verifyStatuses = nil, want an error")
	}
	msg := err.Error()
	for _, want := range []string{
		// The lead line counts the misses (plural).
		"team LERP is missing 2 statuses referenced by lerp.toml:",
		// One line per missing status, naming the reference that points at it.
		`"Done" (todo.on_success)`,
		`"Needs Help" (todo.on_failure)`,
		// The team's actual names, so the operator sees the near-miss.
		"team LERP has: Backlog, Todo, Doen, Halp",
		// The way out.
		"edit lerp.toml or run `lerp init`",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q\nmissing %q", msg, want)
		}
	}
}

func TestVerifyStatusesGroupsReferencesByMissingStatus(t *testing.T) {
	fake := linear.NewFake()
	// Two queues point at the same missing status; the report gets one line
	// for it listing both references, and the team's status list once.
	fake.SetTeamStates("LERP", "Todo", "Done")
	repo := testRepo()
	review := repo.Queues["todo"]
	review.Status = "Done"
	repo.Queues["review"] = review
	err := verifyStatuses(context.Background(), fake, repo)
	if err == nil {
		t.Fatal("verifyStatuses = nil, want an error")
	}
	msg := err.Error()
	if want := `"Needs Help" (review.on_failure, todo.on_failure)`; !strings.Contains(msg, want) {
		t.Errorf("error %q\nmissing %q", msg, want)
	}
	if got := strings.Count(msg, "team LERP has: Todo, Done"); got != 1 {
		t.Errorf("status list printed %d times, want once\nerror %q", got, msg)
	}
}

func TestVerifyStatusesReportsMissingQueueStatus(t *testing.T) {
	fake := linear.NewFake()
	fake.SetTeamStates("LERP", "Backlog", "Doing", "Done", "Needs Help")
	err := verifyStatuses(context.Background(), fake, testRepo())
	if err == nil {
		t.Fatal("verifyStatuses = nil, want an error")
	}
	for _, want := range []string{
		// A single miss reads singular and still names its reference.
		"team LERP is missing 1 status referenced by lerp.toml:",
		`"Todo" (todo.status)`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q\nmissing %q", err, want)
		}
	}
}

func TestVerifyStatusesToleratesAbsentOnFailure(t *testing.T) {
	fake := linear.NewFake()
	fake.SetTeamStates("LERP", "Todo", "Done")
	repo := testRepo()
	q := repo.Queues["todo"]
	q.OnFailure = ""
	repo.Queues["todo"] = q
	if err := verifyStatuses(context.Background(), fake, repo); err != nil {
		t.Fatalf("verifyStatuses = %v, want nil", err)
	}
}

func TestVerifyStatusesFailsOnUnknownTeam(t *testing.T) {
	err := verifyStatuses(context.Background(), linear.NewFake(), testRepo())
	if err == nil || !strings.Contains(err.Error(), `team "LERP"`) {
		t.Fatalf("error = %v, want the unknown team named", err)
	}
}

// countingStates wraps the fake to count TeamStates reads.
type countingStates struct {
	linear.Client
	calls atomic.Int64
}

func (c *countingStates) TeamStates(ctx context.Context, teamKey string) ([]string, error) {
	c.calls.Add(1)
	return c.Client.TeamStates(ctx, teamKey)
}

func TestVerifyStatusesReadsEachTeamOnce(t *testing.T) {
	fake := linear.NewFake()
	fake.SetTeamStates("LERP", "Todo", "Done", "Needs Help")
	repo := testRepo()
	// A second queue on the same team must not cost a second read.
	repo.Queues["review"] = repo.Queues["todo"]
	q := repo.Queues["review"]
	q.Status = "Done"
	repo.Queues["review"] = q
	client := &countingStates{Client: fake}
	if err := verifyStatuses(context.Background(), client, repo); err != nil {
		t.Fatalf("verifyStatuses = %v, want nil", err)
	}
	if got := client.calls.Load(); got != 1 {
		t.Errorf("TeamStates reads = %d, want 1", got)
	}
}

// verifyRepo is testRepo with a second queue, so the warnings assert on a
// pipeline with more than one stage to lose — which is the shape lerp ships.
func verifyRepo() *config.RepoConfig {
	repo := testRepo()
	repo.Queues["plan"] = config.Queue{
		Status: "Planning", Prompt: "plan it", Runner: "agent",
		OnSuccess: "Plan Review", OnFailure: "Needs Help",
	}
	return repo
}

// verifyFake is a fake whose board carries every status verifyRepo names, so
// the statuses check passes and Verify reaches the automations.
func verifyFake() *linear.Fake {
	fake := linear.NewFake()
	fake.SetTeamStates("LERP", "Todo", "Planning", "Plan Review", "Done", "Needs Help", "In Progress")
	return fake
}

func TestVerifyWarnsOnMidStageAutomation(t *testing.T) {
	fake := verifyFake()
	fake.SetGitAutomations("LERP", linear.GitAutomation{
		Event: linear.GitEventStart, Status: "In Progress",
	})
	warnings, err := Verify(context.Background(), fake, verifyRepo(), "")
	if err != nil {
		t.Fatalf("Verify = %v, want nil — a colliding automation warns, it does not refuse", err)
	}
	msg := strings.Join(warnings, "\n")
	for _, want := range []string{
		// Team, trigger and target, in the operator's own vocabulary.
		`team LERP: "On PR open" moves tickets to "In Progress", which lerp.toml never names:`,
		// Every stage the move costs, and the hop it costs it.
		`  a run in "Planning" that opens a pull request will be moved there mid-stage, losing its on_success hop to "Plan Review"`,
		`  a run in "Todo" that opens a pull request will be moved there mid-stage, losing its on_success hop to "Done"`,
		// Both ways out.
		`fix: set that automation to No action for team LERP, or point a queue at "In Progress"`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("warnings\n%s\nmissing %q", msg, want)
		}
	}
}

func TestVerifySaysNothingWhenThePipelineNamesTheTarget(t *testing.T) {
	fake := verifyFake()
	// The deliberate configuration: a queue is pointed at the automation's
	// target on purpose, so the move is one the pipeline understands.
	fake.SetGitAutomations("LERP", linear.GitAutomation{
		Event: linear.GitEventStart, Status: "Plan Review",
	})
	warnings, err := Verify(context.Background(), fake, verifyRepo(), "")
	if err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none: the pipeline names that status", warnings)
	}
}

func TestVerifySaysNothingAboutMergeAutomations(t *testing.T) {
	fake := verifyFake()
	// Merge fires after the pipeline is done with the ticket — benign by
	// construction, however far its target is from anything lerp.toml names.
	fake.SetGitAutomations("LERP", linear.GitAutomation{
		Event: linear.GitEventMerge, Status: "In Progress",
	})
	warnings, err := Verify(context.Background(), fake, verifyRepo(), "")
	if err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none: a merge automation is benign", warnings)
	}
}

func TestVerifySaysNothingAboutAutomationsSetToNoAction(t *testing.T) {
	fake := verifyFake()
	// Linear keeps a switched-off rule as a row with no target state.
	fake.SetGitAutomations("LERP", linear.GitAutomation{Event: linear.GitEventStart})
	warnings, err := Verify(context.Background(), fake, verifyRepo(), "")
	if err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none: the automation takes no action", warnings)
	}
}

func TestVerifyWarnsOncePerMidStageEvent(t *testing.T) {
	fake := verifyFake()
	fake.SetGitAutomations("LERP",
		linear.GitAutomation{Event: linear.GitEventMergeable, Status: "In Progress"},
		linear.GitAutomation{Event: linear.GitEventReview, Status: "In Progress"},
		linear.GitAutomation{Event: linear.GitEventDraft, Status: "In Progress"},
		linear.GitAutomation{Event: linear.GitEventMerge, Status: "In Progress"},
	)
	warnings, err := Verify(context.Background(), fake, verifyRepo(), "")
	if err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	var leads []string
	for _, line := range warnings {
		if strings.HasPrefix(line, "team ") {
			leads = append(leads, line)
		}
	}
	// Reported in the order the events fire, whatever order Linear lists
	// them in, and merge is not among them.
	want := []string{
		`team LERP: "On draft PR open" moves tickets to "In Progress", which lerp.toml never names:`,
		`team LERP: "On PR review request or activity" moves tickets to "In Progress", which lerp.toml never names:`,
		`team LERP: "On PR ready for merge" moves tickets to "In Progress", which lerp.toml never names:`,
	}
	if !slices.Equal(leads, want) {
		t.Errorf("lead lines =\n%s\nwant\n%s", strings.Join(leads, "\n"), strings.Join(want, "\n"))
	}
}

func TestVerifyNamesTheTargetBranchOfAScopedAutomation(t *testing.T) {
	fake := verifyFake()
	// A branch-scoped rule is its own settings row, overriding the team-wide
	// one for pull requests targeting that branch — so the warning has to say
	// which row to open.
	fake.SetGitAutomations("LERP", linear.GitAutomation{
		Event: linear.GitEventStart, Status: "In Progress", Branch: "main",
	})
	warnings, err := Verify(context.Background(), fake, verifyRepo(), "")
	if err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	msg := strings.Join(warnings, "\n")
	for _, want := range []string{
		`team LERP: "On PR open" (target branch "main") moves tickets to "In Progress"`,
		`fix: set that automation to No action for target branch "main" on team LERP`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("warnings\n%s\nmissing %q", msg, want)
		}
	}
}

func TestVerifySaysNothingAboutEventsLerpDoesNotKnow(t *testing.T) {
	fake := verifyFake()
	// Linear may add an event; lerp cannot say when an unknown one fires, so
	// it cannot honestly name the stage it would break.
	fake.SetGitAutomations("LERP", linear.GitAutomation{
		Event: "closed", Status: "In Progress",
	})
	warnings, err := Verify(context.Background(), fake, verifyRepo(), "")
	if err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none: lerp cannot place that event in a run", warnings)
	}
}

func TestVerifyRefusesBeforeItWarns(t *testing.T) {
	fake := linear.NewFake()
	// "Done" is missing, so the statuses check refuses — and the automation
	// warning never gets a chance to describe a board lerp cannot work on.
	fake.SetTeamStates("LERP", "Todo", "Planning", "Plan Review", "Needs Help", "In Progress")
	fake.SetGitAutomations("LERP", linear.GitAutomation{
		Event: linear.GitEventStart, Status: "In Progress",
	})
	warnings, err := Verify(context.Background(), fake, verifyRepo(), "")
	if err == nil {
		t.Fatal("Verify = nil, want the missing status refused")
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none alongside a refusal", warnings)
	}
}

// failingAutomations wraps the fake to fail the automations read.
type failingAutomations struct {
	linear.Client
}

func (failingAutomations) TeamGitAutomations(context.Context, string) ([]linear.GitAutomation, error) {
	return nil, errors.New("boom")
}

func TestVerifyWarnsRatherThanRefusingWhenAutomationsCannotBeRead(t *testing.T) {
	client := failingAutomations{Client: verifyFake()}
	warnings, err := Verify(context.Background(), client, verifyRepo(), "")
	if err != nil {
		t.Fatalf("Verify = %v, want nil — an unreadable check must not block the run", err)
	}
	want := "team LERP: could not read git automations"
	if len(warnings) != 1 || !strings.Contains(warnings[0], want) {
		t.Errorf("warnings = %v, want one line containing %q", warnings, want)
	}
}

func TestVerifyOrdersTheBranchScopedRulesOfOneEvent(t *testing.T) {
	fake := verifyFake()
	// One event, the team-wide rule and two branch overrides of it, declared
	// in the arbitrary order Linear is free to list them in.
	fake.SetGitAutomations("LERP",
		linear.GitAutomation{Event: linear.GitEventStart, Status: "In Progress", Branch: "release/*"},
		linear.GitAutomation{Event: linear.GitEventStart, Status: "In Progress"},
		linear.GitAutomation{Event: linear.GitEventStart, Status: "In Progress", Branch: "main"},
	)
	warnings, err := Verify(context.Background(), fake, verifyRepo(), "")
	if err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	var leads []string
	for _, line := range warnings {
		if strings.HasPrefix(line, "team ") {
			leads = append(leads, line)
		}
	}
	// The team-wide rule first, then its overrides in branch order — a stable
	// report whatever order the connection came back in.
	want := []string{
		`team LERP: "On PR open" moves tickets to "In Progress", which lerp.toml never names:`,
		`team LERP: "On PR open" (target branch "main") moves tickets to "In Progress", which lerp.toml never names:`,
		`team LERP: "On PR open" (target branch "release/*") moves tickets to "In Progress", which lerp.toml never names:`,
	}
	if !slices.Equal(leads, want) {
		t.Errorf("lead lines =\n%s\nwant\n%s", strings.Join(leads, "\n"), strings.Join(want, "\n"))
	}
}

func TestVerifyWarnsPerTeam(t *testing.T) {
	fake := verifyFake()
	fake.SetTeamStates("DOCS", "Todo", "Planning", "Plan Review", "Done", "Needs Help", "In Progress")
	// One served team collides; the other's automation targets a status the
	// pipeline names, so it stays out of the report entirely.
	fake.SetGitAutomations("DOCS", linear.GitAutomation{
		Event: linear.GitEventStart, Status: "In Progress",
	})
	fake.SetGitAutomations("LERP", linear.GitAutomation{
		Event: linear.GitEventStart, Status: "Plan Review",
	})
	repo := verifyRepo()
	repo.Teams = []string{"LERP", "DOCS"}
	warnings, err := Verify(context.Background(), fake, repo, "")
	if err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	msg := strings.Join(warnings, "\n")
	if !strings.Contains(msg, `team DOCS: "On PR open" moves tickets to "In Progress"`) {
		t.Errorf("warnings\n%s\ndo not name team DOCS's collision", msg)
	}
	if strings.Contains(msg, "team LERP") {
		t.Errorf("warnings\n%s\nname team LERP, whose automation the pipeline understands", msg)
	}
	if !strings.Contains(msg, "fix: set that automation to No action for team DOCS") {
		t.Errorf("warnings\n%s\ndo not point the fix at the team it belongs to", msg)
	}
}

func TestVerifyWarnsWhenVendorRunnerLacksLinearMCP(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	for _, tc := range []struct {
		name       string
		runnerName string
		vendor     string
		cliName    string
		wantFix    string
		wantAuth   string
	}{
		{
			name:       "antigravity",
			runnerName: "antigravity-implement",
			vendor:     "antigravity",
			cliName:    "agy",
			wantFix:    "agy mcp add linear https://mcp.linear.app/mcp",
			wantAuth:   "the /mcp overlay in agy",
		},
		{
			name:       "claude",
			runnerName: "claude-code",
			vendor:     "claude",
			cliName:    "claude",
			wantFix:    "claude mcp add --transport http linear https://mcp.linear.app/mcp",
			wantAuth:   "/mcp in claude",
		},
		{
			name:       "codex",
			runnerName: "codex-runner",
			vendor:     "codex",
			cliName:    "codex",
			wantFix:    "codex mcp add linear --url https://mcp.linear.app/mcp",
			wantAuth:   "codex mcp login linear",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := verifyFake()
			repo := verifyRepo()
			repo.Runners[tc.runnerName] = config.Runner{Vendor: tc.vendor}
			repo.Queues["implement"] = config.Queue{
				Status:    "Implementing",
				Prompt:    "do the work",
				Runner:    tc.runnerName,
				OnSuccess: "Done",
				OnFailure: "Needs Help",
			}
			fake.SetTeamStates("LERP", "Todo", "Planning", "Plan Review", "Done", "Needs Help", "Implementing")

			warnings, err := Verify(context.Background(), fake, repo, "")
			if err != nil {
				t.Fatalf("Verify = %v, want nil", err)
			}
			msg := strings.Join(warnings, "\n")
			wantWarning := fmt.Sprintf(
				`runner %q (queue "Implementing") names the %s CLI, which has no Linear MCP server configured — its runs cannot read tickets or leave verdicts. Fix: %s, then authenticate via %s.`,
				tc.runnerName, tc.cliName, tc.wantFix, tc.wantAuth,
			)
			if !strings.Contains(msg, wantWarning) {
				t.Errorf("warnings\n%s\nmissing %q", msg, wantWarning)
			}
		})
	}
}

func TestVerifySaysNothingWhenLinearMCPIsConfigured(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	// Configure Claude in ~/.claude.json
	claudeCfg := homeDir + "/.claude.json"
	if err := os.WriteFile(claudeCfg, []byte(`{"mcpServers":{"linear":{"type":"http","url":"https://mcp.linear.app/mcp"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	fake := verifyFake()
	repo := verifyRepo()
	repo.Runners["claude"] = config.Runner{Vendor: "claude"}
	repo.Queues["implement"] = config.Queue{
		Status:    "Implementing",
		Prompt:    "do the work",
		Runner:    "claude",
		OnSuccess: "Done",
		OnFailure: "Needs Help",
	}
	fake.SetTeamStates("LERP", "Todo", "Planning", "Plan Review", "Done", "Needs Help", "Implementing")

	warnings, err := Verify(context.Background(), fake, repo, "")
	if err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none when MCP is configured", warnings)
	}
}

func TestVerifyLinearMCPSkipsCommandRunners(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir) // empty home

	fake := verifyFake()
	repo := verifyRepo()
	repo.Runners["cmd-runner"] = config.Runner{Command: "echo hello"}
	repo.Queues["implement"] = config.Queue{
		Status:    "Implementing",
		Prompt:    "do the work",
		Runner:    "cmd-runner",
		OnSuccess: "Done",
		OnFailure: "Needs Help",
	}
	fake.SetTeamStates("LERP", "Todo", "Planning", "Plan Review", "Done", "Needs Help", "Implementing")

	warnings, err := Verify(context.Background(), fake, repo, "")
	if err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none for command runners", warnings)
	}
}

func TestVerifyLinearMCPListsMultipleQueues(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	fake := verifyFake()
	repo := verifyRepo()
	repo.Runners["claude"] = config.Runner{Vendor: "claude"}
	repo.Queues["plan"] = config.Queue{
		Status:    "Planning",
		Prompt:    "plan it",
		Runner:    "claude",
		OnSuccess: "Plan Review",
		OnFailure: "Needs Help",
	}
	repo.Queues["implement"] = config.Queue{
		Status:    "Implementing",
		Prompt:    "do the work",
		Runner:    "claude",
		OnSuccess: "Done",
		OnFailure: "Needs Help",
	}
	fake.SetTeamStates("LERP", "Todo", "Planning", "Plan Review", "Done", "Needs Help", "Implementing")

	warnings, err := Verify(context.Background(), fake, repo, "")
	if err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	msg := strings.Join(warnings, "\n")
	want := `runner "claude" (queues "Implementing", "Planning") names the claude CLI, which has no Linear MCP server configured — its runs cannot read tickets or leave verdicts. Fix: claude mcp add --transport http linear https://mcp.linear.app/mcp, then authenticate via /mcp in claude.`
	if !strings.Contains(msg, want) {
		t.Errorf("warnings\n%s\nmissing %q", msg, want)
	}
}

func TestVerifyLinearMCPUnusedRunner(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	fake := verifyFake()
	repo := verifyRepo()
	// Defined in [runners] but not referenced by any queue
	repo.Runners["claude"] = config.Runner{Vendor: "claude"}

	warnings, err := Verify(context.Background(), fake, repo, "")
	if err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	msg := strings.Join(warnings, "\n")
	want := `runner "claude" names the claude CLI, which has no Linear MCP server configured — its runs cannot read tickets or leave verdicts. Fix: claude mcp add --transport http linear https://mcp.linear.app/mcp, then authenticate via /mcp in claude.`
	if !strings.Contains(msg, want) {
		t.Errorf("warnings\n%s\nmissing %q", msg, want)
	}
}
