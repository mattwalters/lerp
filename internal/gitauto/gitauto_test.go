package gitauto

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/linear"
)

func verifyRepo() *config.RepoConfig {
	return &config.RepoConfig{
		Teams: []string{"LERP"},
		Queues: map[string]config.Queue{
			"plan": {
				Status: "Planning", Prompt: "plan it", Runner: "agent",
				OnSuccess: "Plan Review", OnFailure: "Needs Help",
			},
			"todo": {
				Status: "Todo", Prompt: "do it", Runner: "agent",
				OnSuccess: "Done", OnFailure: "Needs Help",
			},
		},
	}
}

type fakeClient struct {
	automations []linear.GitAutomation
	err         error
}

func (f fakeClient) TeamGitAutomations(_ context.Context, _ string) ([]linear.GitAutomation, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.automations, nil
}

func TestFindingsWarnsOnMidStageAutomation(t *testing.T) {
	automations := []linear.GitAutomation{
		{Event: linear.GitEventStart, Status: "In Progress"},
	}
	warnings := Findings(verifyRepo(), "LERP", automations, nil)
	msg := strings.Join(warnings, "\n")
	for _, want := range []string{
		// Team, trigger and target, in the operator's own vocabulary.
		`team LERP: "On PR open" moves tickets to "In Progress", which the repo config never names:`,
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

func TestFindingsSaysNothingWhenThePipelineNamesTheTarget(t *testing.T) {
	// The deliberate configuration: a queue is pointed at the automation's
	// target on purpose, so the move is one the pipeline understands.
	automations := []linear.GitAutomation{
		{Event: linear.GitEventStart, Status: "Plan Review"},
	}
	warnings := Findings(verifyRepo(), "LERP", automations, nil)
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none: the pipeline names that status", warnings)
	}
}

func TestFindingsSaysNothingAboutMergeAutomations(t *testing.T) {
	// Merge fires after the pipeline is done with the ticket — benign by
	// construction, however far its target is from anything lerp.toml names.
	automations := []linear.GitAutomation{
		{Event: linear.GitEventMerge, Status: "In Progress"},
	}
	warnings := Findings(verifyRepo(), "LERP", automations, nil)
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none: a merge automation is benign", warnings)
	}
}

func TestFindingsSaysNothingAboutAutomationsSetToNoAction(t *testing.T) {
	// Linear keeps a switched-off rule as a row with no target state.
	automations := []linear.GitAutomation{
		{Event: linear.GitEventStart},
	}
	warnings := Findings(verifyRepo(), "LERP", automations, nil)
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none: the automation takes no action", warnings)
	}
}

func TestFindingsWarnsOncePerMidStageEvent(t *testing.T) {
	automations := []linear.GitAutomation{
		{Event: linear.GitEventMergeable, Status: "In Progress"},
		{Event: linear.GitEventReview, Status: "In Progress"},
		{Event: linear.GitEventDraft, Status: "In Progress"},
		{Event: linear.GitEventMerge, Status: "In Progress"},
	}
	warnings := Findings(verifyRepo(), "LERP", automations, nil)
	var leads []string
	for _, line := range warnings {
		if strings.HasPrefix(line, "team ") {
			leads = append(leads, line)
		}
	}
	// Reported in the order the events fire, whatever order Linear lists
	// them in, and merge is not among them.
	want := []string{
		`team LERP: "On draft PR open" moves tickets to "In Progress", which the repo config never names:`,
		`team LERP: "On PR review request or activity" moves tickets to "In Progress", which the repo config never names:`,
		`team LERP: "On PR ready for merge" moves tickets to "In Progress", which the repo config never names:`,
	}
	if !slices.Equal(leads, want) {
		t.Errorf("lead lines =\n%s\nwant\n%s", strings.Join(leads, "\n"), strings.Join(want, "\n"))
	}
}

func TestFindingsNamesTheTargetBranchOfAScopedAutomation(t *testing.T) {
	// A branch-scoped rule is its own settings row, overriding the team-wide
	// one for pull requests targeting that branch — so the warning has to say
	// which row to open.
	automations := []linear.GitAutomation{
		{Event: linear.GitEventStart, Status: "In Progress", Branch: "main"},
	}
	warnings := Findings(verifyRepo(), "LERP", automations, nil)
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

func TestFindingsSaysNothingAboutEventsLerpDoesNotKnow(t *testing.T) {
	// Linear may add an event; lerp cannot say when an unknown one fires, so
	// it cannot honestly name the stage it would break.
	automations := []linear.GitAutomation{
		{Event: "closed", Status: "In Progress"},
	}
	warnings := Findings(verifyRepo(), "LERP", automations, nil)
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none: lerp cannot place that event in a run", warnings)
	}
}

func TestFindingsOrdersTheBranchScopedRulesOfOneEvent(t *testing.T) {
	// One event, the team-wide rule and two branch overrides of it, declared
	// in the arbitrary order Linear is free to list them in.
	automations := []linear.GitAutomation{
		{Event: linear.GitEventStart, Status: "In Progress", Branch: "release/*"},
		{Event: linear.GitEventStart, Status: "In Progress"},
		{Event: linear.GitEventStart, Status: "In Progress", Branch: "main"},
	}
	warnings := Findings(verifyRepo(), "LERP", automations, nil)
	var leads []string
	for _, line := range warnings {
		if strings.HasPrefix(line, "team ") {
			leads = append(leads, line)
		}
	}
	// The team-wide rule first, then its overrides in branch order — a stable
	// report whatever order the connection came back in.
	want := []string{
		`team LERP: "On PR open" moves tickets to "In Progress", which the repo config never names:`,
		`team LERP: "On PR open" (target branch "main") moves tickets to "In Progress", which the repo config never names:`,
		`team LERP: "On PR open" (target branch "release/*") moves tickets to "In Progress", which the repo config never names:`,
	}
	if !slices.Equal(leads, want) {
		t.Errorf("lead lines =\n%s\nwant\n%s", strings.Join(leads, "\n"), strings.Join(want, "\n"))
	}
}

func TestFindingsReportsReadError(t *testing.T) {
	readErr := errors.New("connection reset")
	warnings := Findings(verifyRepo(), "LERP", nil, readErr)
	want := "team LERP: could not read git automations, so they are not checked: connection reset"
	if len(warnings) != 1 || warnings[0] != want {
		t.Errorf("warnings = %v, want [%q]", warnings, want)
	}
}

func TestCheckReadsTeamAutomations(t *testing.T) {
	client := fakeClient{
		automations: []linear.GitAutomation{
			{Event: linear.GitEventStart, Status: "In Progress"},
		},
	}
	warnings := Check(context.Background(), client, verifyRepo(), "LERP")
	if len(warnings) == 0 || !strings.Contains(warnings[0], `team LERP: "On PR open" moves tickets to "In Progress"`) {
		t.Errorf("Check warnings = %v", warnings)
	}
}

func TestCollisions(t *testing.T) {
	automations := []linear.GitAutomation{
		{ID: "auto-1", Event: linear.GitEventMerge, Status: "In Progress"},                      // merge (benign)
		{ID: "auto-2", Event: linear.GitEventStart},                                             // No action
		{ID: "auto-3", Event: "closed", Status: "In Progress"},                                  // unknown event
		{ID: "auto-4", Event: linear.GitEventStart, Status: "Plan Review"},                      // named by queue
		{ID: "auto-5", Event: linear.GitEventMergeable, Status: "In Progress"},                  // mid-stage 4
		{ID: "auto-6", Event: linear.GitEventStart, Status: "In Progress", Branch: "release/*"}, // mid-stage 2 (branch)
		{ID: "auto-7", Event: linear.GitEventStart, Status: "In Progress"},                      // mid-stage 2 (team-wide)
		{ID: "auto-8", Event: linear.GitEventDraft, Status: "In Progress"},                      // mid-stage 1
	}
	got := Collisions(verifyRepo(), automations)
	want := []Collision{
		{Automation: linear.GitAutomation{ID: "auto-8", Event: linear.GitEventDraft, Status: "In Progress"}, Label: "On draft PR open"},
		{Automation: linear.GitAutomation{ID: "auto-7", Event: linear.GitEventStart, Status: "In Progress"}, Label: "On PR open"},
		{Automation: linear.GitAutomation{ID: "auto-6", Event: linear.GitEventStart, Status: "In Progress", Branch: "release/*"}, Label: "On PR open"},
		{Automation: linear.GitAutomation{ID: "auto-5", Event: linear.GitEventMergeable, Status: "In Progress"}, Label: "On PR ready for merge"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Collisions =\n%+v\nwant\n%+v", got, want)
	}
}
