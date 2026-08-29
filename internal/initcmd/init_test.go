package initcmd

import (
	"bytes"
	"context"
	"errors"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/initui"
	"github.com/mattwalters/lerp/internal/linear"
)

func stubWizardResult(t *testing.T, res initui.Result, err error) {
	t.Helper()
	restore := SetWizardRunner(func(ctx context.Context, opts initui.Options) (initui.Result, error) {
		if err != nil {
			return initui.Result{}, err
		}
		r := res
		if r.TeamKey == "" {
			r.TeamKey = opts.TeamKey
			if r.TeamKey == "" && len(opts.WorkspaceTeams) > 0 {
				r.TeamKey = opts.WorkspaceTeams[0].Key
			}
		}
		if r.TeamName == "" {
			r.TeamName = opts.TeamName
		}
		if r.Stock.Teams == nil && r.TeamKey != "" {
			r.Stock.Teams = []string{r.TeamKey}
		}
		if opts.Preview != nil {
			_, _ = opts.Preview(initui.Choices{
				TeamKey:    r.TeamKey,
				TeamName:   r.TeamName,
				CreateTeam: r.CreateTeam,
				Stock:      r.Stock,
				MCPIntent:  r.MCPIntent,
			})
		}
		return r, nil
	})
	t.Cleanup(restore)
}

type fakeBoard struct {
	teamKey, teamName string
	teamStatesKey     string
	teamNotFound      bool
	ensureTeamCalled  bool
	ensureCalls       int
	automations       []linear.GitAutomation
	automationsErr    error
	teams             []linear.TeamRef
	teamsErr          error
	// existing plays the statuses the team already has, in board order.
	existing []linear.WorkflowState
	states   []linear.StateSpec
	// categories plays Linear's category for existing states, name →
	// category; existing states it does not name use their WorkflowState
	// Category (or "unstarted" if empty). Requested states not on the board
	// come back as created, in their requested category — the contract of
	// the real EnsureWorkflowStates.
	categories map[string]string
	err        error
}

func (b *fakeBoard) EnsureTeam(_ context.Context, key, name string) error {
	b.ensureCalls++
	b.ensureTeamCalled = true
	b.teamKey, b.teamName = key, name
	return b.err
}

func (b *fakeBoard) Teams(_ context.Context) ([]linear.TeamRef, error) {
	if b.teamsErr != nil {
		return nil, b.teamsErr
	}
	if b.teamNotFound && b.teams == nil {
		return nil, nil
	}
	if b.teams == nil {
		return []linear.TeamRef{
			{Key: "LERP", Name: "Lerp"},
		}, nil
	}
	return b.teams, nil
}

func (b *fakeBoard) TeamWorkflowStates(_ context.Context, key string) ([]linear.WorkflowState, error) {
	b.teamStatesKey = key
	if b.err != nil {
		return nil, b.err
	}
	if b.teamNotFound {
		return nil, linear.ErrNotFound
	}
	return b.existing, nil
}

func (b *fakeBoard) EnsureWorkflowStates(_ context.Context, key string, states []linear.StateSpec) (map[string]string, error) {
	b.teamKey = key
	b.states = states
	if b.err != nil {
		return nil, b.err
	}
	existingCat := map[string]string{}
	for _, s := range b.existing {
		cat := s.Category
		if cat == "" {
			cat = "unstarted"
		}
		existingCat[s.Name] = cat
	}
	res := map[string]string{}
	for _, s := range states {
		if cat, ok := b.categories[s.Name]; ok {
			res[s.Name] = cat
			continue
		}
		if cat, ok := existingCat[s.Name]; ok {
			res[s.Name] = cat
			continue
		}
		res[s.Name] = s.Type
	}
	return res, nil
}

func (b *fakeBoard) TeamGitAutomations(_ context.Context, key string) ([]linear.GitAutomation, error) {
	if b.automationsErr != nil {
		return nil, b.automationsErr
	}
	return b.automations, nil
}

func boardWorkflowStates(names ...string) []linear.WorkflowState {
	states := make([]linear.WorkflowState, len(names))
	for i, name := range names {
		states[i] = linear.WorkflowState{Name: name, Category: "started"}
	}
	return states
}

var linearDefaults = []linear.WorkflowState{
	{Name: "Backlog", Category: "backlog"},
	{Name: "Todo", Category: "unstarted"},
	{Name: "In Progress", Category: "started"},
	{Name: "Done", Category: "completed"},
	{Name: "Canceled", Category: "canceled"},
}

const existingConfig = `
teams = ["LERP"]
provision = "mine"
dispose = "mine"

[runners.mine]
command = "mine {{prompt}}"

[queues.plan]
status = "Planning"
prompt = "Plan {{ticket}}."
runner = "mine"
on_success = "Implementing"

[queues.code]
status = "Implementing"
prompt = "Implement {{ticket}}."
runner = "mine"
on_success = "Review"
on_failure = "Human Review"
`

func TestInitCreatesConfigAndStates(t *testing.T) {
	dir := t.TempDir()
	b := &fakeBoard{existing: linearDefaults, teamName: "Lerp"}
	var out bytes.Buffer

	stubWizardResult(t, initui.Result{
		TeamKey:  "LERP",
		TeamName: "Lerp",
		Stock: config.Stock{
			Teams:  []string{"LERP"},
			Plan:   true,
			Review: true,
			Bypass: true,
		},
	}, nil)

	answers := strings.NewReader("interactive")
	created, err := Init(context.Background(), b, &out, answers, dir, "LERP", "Lerp")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("created = false, want true")
	}
	want := []linear.StateSpec{
		{Name: "Implementing", Type: "started"},
		{Name: "In Review", Type: "started"},
		{Name: "Needs Attention", Type: "started"},
		{Name: "Plan Review", Type: "started"},
		{Name: "Planning", Type: "started"},
	}
	if !reflect.DeepEqual(b.states, want) {
		t.Errorf("states = %+v, want %+v", b.states, want)
	}
	c, err := config.LoadRepoConfig(filepath.Join(dir, config.RepoConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Teams, []string{"LERP"}) {
		t.Errorf("teams = %v", c.Teams)
	}
	if !strings.Contains(c.Runners["claude"].Command, "bypassPermissions") {
		t.Errorf("accepted grant missing from %q", c.Runners["claude"].Command)
	}
	gate := c.Queues["plan"].OnSuccess
	if gate != "Plan Review" {
		t.Errorf("plan.on_success = %q, want Plan Review", gate)
	}
	for name, q := range c.Queues {
		if q.Status == gate {
			t.Errorf("queue %q watches %q, so nothing waits for the operator", name, gate)
		}
	}
}

func TestInitFastPathConversation(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer

	stubWizardResult(t, initui.Result{
		TeamKey:  "LERP",
		TeamName: "Lerp",
		Stock: config.Stock{
			Teams:  []string{"LERP"},
			Plan:   true,
			Review: true,
		},
	}, nil)

	answers := strings.NewReader("interactive")
	if _, err := Init(context.Background(), &fakeBoard{existing: linearDefaults}, &out, answers, dir, "LERP", ""); err != nil {
		t.Fatal(err)
	}
	transcript := out.String()
	for _, wanted := range []string{
		"creating on team LERP: Implementing, In Review, Needs Attention, Plan Review, Planning",
		"writing " + filepath.Join(dir, config.RepoConfigFile),
		"adding .lerp/ to .gitignore",
	} {
		if !strings.Contains(transcript, wanted) {
			t.Errorf("transcript missing %q:\n%s", wanted, transcript)
		}
	}
	if strings.Contains(transcript, "using existing") {
		t.Errorf("transcript claims existing statuses were used:\n%s", transcript)
	}
}

func TestInitCustomizeMapsOntoExistingStatuses(t *testing.T) {
	dir := t.TempDir()
	existing := []linear.WorkflowState{
		{Name: "Backlog", Category: "backlog"},
		{Name: "Todo", Category: "unstarted"},
		{Name: "In Progress", Category: "started"},
		{Name: "In Review", Category: "started"},
		{Name: "Done", Category: "completed"},
	}
	var out bytes.Buffer

	stubWizardResult(t, initui.Result{
		TeamKey:  "LERP",
		TeamName: "Lerp",
		Stock: config.Stock{
			Teams:            []string{"LERP"},
			Plan:             true,
			Review:           true,
			PlanStatus:       "Planning",
			PlanReviewStatus: "Plan Review",
			ImplementStatus:  "Todo",
			ExitStatus:       "In Review",
			AttentionStatus:  "Needs Attention",
		},
	}, nil)

	answers := strings.NewReader("interactive")
	b := &fakeBoard{existing: existing}
	if _, err := Init(context.Background(), b, &out, answers, dir, "LERP", ""); err != nil {
		t.Fatal(err)
	}
	c, err := config.LoadRepoConfig(filepath.Join(dir, config.RepoConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Queues["implement"].Status; got != "Todo" {
		t.Errorf("implement.status = %q, want Todo", got)
	}
	if got := c.Queues["plan"].OnSuccess; got != "Plan Review" {
		t.Errorf("plan.on_success = %q, want the approval gate Plan Review", got)
	}
	if got := c.Queues["implement"].OnSuccess; got != "In Review" {
		t.Errorf("implement.on_success = %q, want In Review", got)
	}
	want := []linear.StateSpec{
		{Name: "In Review", Type: "started"},
		{Name: "Needs Attention", Type: "started"},
		{Name: "Plan Review", Type: "started"},
		{Name: "Planning", Type: "started"},
		{Name: "Todo", Type: "started"},
	}
	if !reflect.DeepEqual(b.states, want) {
		t.Errorf("states = %+v, want %+v", b.states, want)
	}
	transcript := out.String()
	wanted := "creating on team LERP: Needs Attention, Plan Review, Planning  ·  using existing: In Review (implement exit), Todo (implement)"
	if !strings.Contains(transcript, wanted) {
		t.Errorf("transcript missing %q:\n%s", wanted, transcript)
	}
}

func TestInitDeclinedReviewDropsThePassNotAQueue(t *testing.T) {
	dir := t.TempDir()
	stubWizardResult(t, initui.Result{
		TeamKey:  "LERP",
		TeamName: "Lerp",
		Stock: config.Stock{
			Teams:  []string{"LERP"},
			Plan:   true,
			Review: false,
		},
	}, nil)

	answers := strings.NewReader("interactive")
	b := &fakeBoard{existing: linearDefaults}
	if _, err := Init(context.Background(), b, io.Discard, answers, dir, "LERP", ""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, config.RepoConfigFile)
	c, err := config.LoadRepoConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Queues["review"]; ok {
		t.Error("a review queue was written: review runs inside implement")
	}
	if got := c.Queues["implement"].OnSuccess; got != "In Review" {
		t.Errorf("implement.on_success = %q, want the exit In Review", got)
	}
	if strings.Contains(c.Queues["implement"].Prompt, "three rounds") {
		t.Error("implement prompt still reviews its own work after the pass was declined")
	}
	for _, ending := range []string{"ends one of exactly two ways", "marked ready for review", "looking finished when it is not"} {
		if !strings.Contains(c.Queues["implement"].Prompt, ending) {
			t.Errorf("declined-review implement prompt lost its exit contract: no %q", ending)
		}
	}
	flat := strings.Join(strings.Fields(c.Queues["implement"].Prompt), " ")
	if !strings.Contains(flat, "Title the pull request you open with {{ticket}}, a colon") {
		t.Error("declined-review implement prompt lost the pull request title convention")
	}
	want := []linear.StateSpec{
		{Name: "Implementing", Type: "started"},
		{Name: "In Review", Type: "started"},
		{Name: "Needs Attention", Type: "started"},
		{Name: "Plan Review", Type: "started"},
		{Name: "Planning", Type: "started"},
	}
	if !reflect.DeepEqual(b.states, want) {
		t.Errorf("states = %+v, want %+v", b.states, want)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{"# Check this file in", "{{on_failure}}", "[queues.plan]"} {
		if !strings.Contains(string(raw), wanted) {
			t.Errorf("written file missing %q", wanted)
		}
	}
}

func TestInitDeclinedPlanningDropsPlanQueue(t *testing.T) {
	dir := t.TempDir()
	stubWizardResult(t, initui.Result{
		TeamKey:  "LERP",
		TeamName: "Lerp",
		Stock: config.Stock{
			Teams:  []string{"LERP"},
			Plan:   false,
			Review: true,
		},
	}, nil)

	answers := strings.NewReader("interactive")
	b := &fakeBoard{existing: linearDefaults}
	if _, err := Init(context.Background(), b, io.Discard, answers, dir, "LERP", ""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, config.RepoConfigFile)
	c, err := config.LoadRepoConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Queues["plan"]; ok {
		t.Error("declined plan queue was written anyway")
	}
	if got := c.Queues["implement"].OnSuccess; got != "In Review" {
		t.Errorf("implement.on_success = %q, want In Review", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "[queues.plan]") {
		t.Error("written file still contains the plan queue section")
	}
	want := []linear.StateSpec{
		{Name: "Implementing", Type: "started"},
		{Name: "In Review", Type: "started"},
		{Name: "Needs Attention", Type: "started"},
	}
	if !reflect.DeepEqual(b.states, want) {
		t.Errorf("states = %+v, want %+v", b.states, want)
	}
}

func TestInitNonInteractiveStockAnswers(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	b := &fakeBoard{existing: linearDefaults}
	created, err := Init(context.Background(), b, &out, nil, dir, "LERP", "")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("created = false, want true")
	}
	c, err := config.LoadRepoConfig(filepath.Join(dir, config.RepoConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Queues) != 2 {
		t.Errorf("queues = %d, want the full stock pipeline of 2", len(c.Queues))
	}
	for name, r := range c.Runners {
		if strings.Contains(r.Command, "bypassPermissions") {
			t.Errorf("runner %q carries the grant nobody accepted: %q", name, r.Command)
		}
	}
	if strings.Contains(out.String(), "?") {
		t.Errorf("non-interactive init asked a question:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "creating on team LERP: Implementing, In Review, Needs Attention, Plan Review, Planning") {
		t.Errorf("no loud created report:\n%s", out.String())
	}
}

func TestInitWithoutBypassGrant(t *testing.T) {
	for name, answers := range map[string]io.Reader{
		"declined": strings.NewReader("interactive"),
		"nil":      nil,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if answers != nil {
				stubWizardResult(t, initui.Result{
					TeamKey:  "LERP",
					TeamName: "Lerp",
					Stock: config.Stock{
						Teams:  []string{"LERP"},
						Plan:   true,
						Review: true,
						Bypass: false,
					},
				}, nil)
			}
			if _, err := Init(context.Background(), &fakeBoard{}, nil, answers, dir, "LERP", ""); err != nil {
				t.Fatal(err)
			}
			c, err := config.LoadRepoConfig(filepath.Join(dir, config.RepoConfigFile))
			if err != nil {
				t.Fatal(err)
			}
			for name, r := range c.Runners {
				if strings.Contains(r.Command, "bypassPermissions") {
					t.Errorf("runner %q kept the declined grant: %q", name, r.Command)
				}
			}
		})
	}
}

func TestInitIsIdempotentAndDoesNotReplaceConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, config.RepoConfigFile)
	if err := os.WriteFile(path, []byte(existingConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	b := &fakeBoard{existing: boardWorkflowStates("Planning", "Implementing")}
	var out bytes.Buffer

	stubWizardResult(t, initui.Result{
		TeamKey:  "LERP",
		TeamName: "Lerp",
	}, nil)

	answers := strings.NewReader("interactive")
	created, err := Init(context.Background(), b, &out, answers, dir, "LERP", "")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("created = true, want false for existing config")
	}
	want := []linear.StateSpec{
		{Name: "Human Review", Type: "started"},
		{Name: "Implementing", Type: "started"},
		{Name: "Planning", Type: "started"},
		{Name: "Review", Type: "started"},
	}
	if !reflect.DeepEqual(b.states, want) {
		t.Errorf("states = %+v, want %+v", b.states, want)
	}
	report := "creating on team LERP: Human Review, Review  ·  using existing: Implementing (code), Planning (plan)"
	if !strings.Contains(out.String(), report) {
		t.Errorf("out %q\nmissing %q", out.String(), report)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != existingConfig {
		t.Errorf("config changed to %q", got)
	}
}

func TestInitRejectsExistingConfigForOtherTeam(t *testing.T) {
	dir := t.TempDir()
	other := strings.ReplaceAll(existingConfig, `teams = ["LERP"]`, `teams = ["OTHER"]`)
	if err := os.WriteFile(filepath.Join(dir, config.RepoConfigFile), []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}
	b := &fakeBoard{}
	_, err := Init(context.Background(), b, nil, nil, dir, "LERP", "")
	if err == nil || !strings.Contains(err.Error(), `does not serve team "LERP"`) {
		t.Fatalf("Init error = %v", err)
	}
	if b.teamKey != "" {
		t.Error("board touched although the config was rejected")
	}
}

func TestInitVerifiesExistingYAMLConfig(t *testing.T) {
	dir := t.TempDir()
	yamlConfig := `
teams:
  - LERP
provision: mine
dispose: mine
runners:
  mine:
    command: mine {{prompt}}
queues:
  plan:
    status: Planning
    prompt: Plan {{ticket}}.
    runner: mine
    on_success: Implementing
  code:
    status: Implementing
    prompt: Implement {{ticket}}.
    runner: mine
    on_success: Review
    on_failure: Human Review
`
	yamlPath := filepath.Join(dir, "lerp.yaml")
	if err := os.WriteFile(yamlPath, []byte(yamlConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	b := &fakeBoard{existing: boardWorkflowStates("Planning", "Implementing")}
	var out bytes.Buffer
	created, err := Init(context.Background(), b, &out, nil, dir, "LERP", "")
	if err != nil {
		t.Fatalf("Init error = %v", err)
	}
	if created {
		t.Error("created = true, want false for existing lerp.yaml")
	}
	if _, err := os.Stat(filepath.Join(dir, config.RepoConfigFile)); !os.IsNotExist(err) {
		t.Error("lerp.toml was written alongside lerp.yaml")
	}
	want := []linear.StateSpec{
		{Name: "Human Review", Type: "started"},
		{Name: "Implementing", Type: "started"},
		{Name: "Planning", Type: "started"},
		{Name: "Review", Type: "started"},
	}
	if !reflect.DeepEqual(b.states, want) {
		t.Errorf("states = %+v, want %+v", b.states, want)
	}
}

func TestInitRefusesMultipleConfigsBeforeBoardCall(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lerp.toml"), []byte("teams = ['LERP']\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lerp.yaml"), []byte("teams:\n  - LERP\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := &fakeBoard{}
	_, err := Init(context.Background(), b, nil, nil, dir, "LERP", "")
	if err == nil {
		t.Fatal("want error on multiple configs, got nil")
	}
	for _, want := range []string{"lerp.toml", "lerp.yaml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err.Error(), want)
		}
	}
	if b.teamKey != "" {
		t.Errorf("board called (%q) before refusing multi-config", b.teamKey)
	}
}

func TestInitNormalizesTeamKeyCase(t *testing.T) {
	for _, teamInput := range []string{"lerp", "  lerp  ", "lErP"} {
		t.Run(teamInput, func(t *testing.T) {
			dir := t.TempDir()
			b := &fakeBoard{teamNotFound: true}
			created, err := Init(context.Background(), b, io.Discard, nil, dir, teamInput, "")
			if err != nil {
				t.Fatalf("Init error = %v", err)
			}
			if !created {
				t.Error("created = false, want true")
			}
			if b.teamKey != "LERP" {
				t.Errorf("EnsureTeam key = %q, want LERP", b.teamKey)
			}
			if b.teamName != "LERP" {
				t.Errorf("EnsureTeam name = %q, want LERP default", b.teamName)
			}
			c, err := config.LoadRepoConfig(filepath.Join(dir, config.RepoConfigFile))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(c.Teams, []string{"LERP"}) {
				t.Errorf("teams = %v, want [LERP]", c.Teams)
			}
		})
	}
}

func TestInitRejectsEmptyOrWhitespaceTeamKey(t *testing.T) {
	for _, empty := range []string{"", "   ", "\t\n"} {
		t.Run(empty, func(t *testing.T) {
			dir := t.TempDir()
			b := &fakeBoard{}
			_, err := Init(context.Background(), b, nil, nil, dir, empty, "")
			if err == nil || !strings.Contains(err.Error(), "team key must not be empty") {
				t.Fatalf("Init(%q) error = %v, want team key must not be empty", empty, err)
			}
			if b.teamKey != "" {
				t.Error("board touched although team key was empty")
			}
		})
	}
}

func TestInitNormalizesTeamKeyOnRepeat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, config.RepoConfigFile)
	if err := os.WriteFile(path, []byte(existingConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	b := &fakeBoard{existing: boardWorkflowStates("Planning", "Implementing")}
	created, err := Init(context.Background(), b, io.Discard, nil, dir, "  lerp  ", "")
	if err != nil {
		t.Fatalf("Init error = %v", err)
	}
	if created {
		t.Error("created = true, want false for existing config")
	}
	if b.teamStatesKey != "LERP" {
		t.Errorf("TeamStates key = %q, want LERP", b.teamStatesKey)
	}
}

func TestInitRejectsMappingTwoQueuesOntoOneStatus(t *testing.T) {
	dir := t.TempDir()
	restore := SetWizardRunner(func(ctx context.Context, opts initui.Options) (initui.Result, error) {
		_, err := opts.Preview(initui.Choices{
			TeamKey: "LERP",
			Stock: config.Stock{
				Teams:           []string{"LERP"},
				Plan:            true,
				Review:          true,
				PlanStatus:      "Todo",
				ImplementStatus: "Todo",
			},
		})
		if err != nil {
			return initui.Result{}, err
		}
		return initui.Result{}, nil
	})
	defer restore()

	answers := strings.NewReader("interactive")
	b := &fakeBoard{existing: linearDefaults}
	_, err := Init(context.Background(), b, io.Discard, answers, dir, "LERP", "")
	if err == nil || !strings.Contains(err.Error(), `both watch status "Todo"`) {
		t.Fatalf("Init error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, config.RepoConfigFile)); !os.IsNotExist(statErr) {
		t.Fatalf("config was created: %v", statErr)
	}
	if b.teamKey != "" {
		t.Errorf("board touched (%q) before rejection: EnsureTeam must not run before validation", b.teamKey)
	}
}

func TestInitCreatesNonExistentTeam(t *testing.T) {
	dir := t.TempDir()
	b := &fakeBoard{teamNotFound: true}
	var out bytes.Buffer

	stubWizardResult(t, initui.Result{
		TeamKey:    "NEWTEAM",
		TeamName:   "New Team",
		CreateTeam: true,
		Stock: config.Stock{
			Teams:  []string{"NEWTEAM"},
			Plan:   true,
			Review: true,
			Bypass: true,
		},
	}, nil)

	answers := strings.NewReader("interactive")
	created, err := Init(context.Background(), b, &out, answers, dir, "NEWTEAM", "New Team")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("created = false, want true")
	}
	if b.teamKey != "NEWTEAM" || b.teamName != "New Team" {
		t.Errorf("EnsureTeam = (%q, %q), want (NEWTEAM, New Team)", b.teamKey, b.teamName)
	}
	output := out.String()
	for _, want := range []string{
		"creating team NEWTEAM (New Team)",
		"creating on team NEWTEAM: Implementing, In Review, Needs Attention, Plan Review, Planning",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q:\n%s", want, output)
		}
	}
}

type orderBoard struct {
	fakeBoard
	out                 *bytes.Buffer
	reportLenAtTeam     int
	reportLenAtWorkflow int
}

func (b *orderBoard) EnsureTeam(ctx context.Context, key, name string) error {
	b.reportLenAtTeam = b.out.Len()
	return b.fakeBoard.EnsureTeam(ctx, key, name)
}

func (b *orderBoard) EnsureWorkflowStates(ctx context.Context, key string, states []linear.StateSpec) (map[string]string, error) {
	b.reportLenAtWorkflow = b.out.Len()
	return b.fakeBoard.EnsureWorkflowStates(ctx, key, states)
}

func TestInitConfirmPrecedesExecute(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answers io.Reader
	}{
		{"interactive", strings.NewReader("interactive")},
		{"non-interactive", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var out bytes.Buffer
			b := &orderBoard{
				fakeBoard: fakeBoard{teamNotFound: true},
				out:       &out,
			}
			if tc.answers != nil {
				stubWizardResult(t, initui.Result{
					TeamKey:    "LERP",
					TeamName:   "Lerp",
					CreateTeam: true,
					Stock: config.Stock{
						Teams:  []string{"LERP"},
						Plan:   true,
						Review: true,
						Bypass: true,
					},
				}, nil)
			}
			_, err := Init(context.Background(), b, &out, tc.answers, dir, "LERP", "Lerp")
			if err != nil {
				t.Fatal(err)
			}
			output := out.String()
			if b.reportLenAtTeam <= 0 {
				t.Fatalf("EnsureTeam was not called or called before report")
			}
			if b.reportLenAtWorkflow <= 0 {
				t.Fatalf("EnsureWorkflowStates was not called or called before report")
			}
			reportPrefix := output[:b.reportLenAtWorkflow]
			for _, want := range []string{
				"creating team LERP (Lerp)",
				"creating on team LERP: Implementing, In Review, Needs Attention, Plan Review, Planning",
				"writing " + filepath.Join(dir, config.RepoConfigFile),
				"adding .lerp/ to .gitignore",
			} {
				if !strings.Contains(reportPrefix, want) {
					t.Errorf("confirm report missing %q before execute:\n%s", want, reportPrefix)
				}
			}
			afterWorkflow := output[b.reportLenAtWorkflow:]
			for _, want := range []string{
				"pipeline exit \"In Review\"",
				"added .lerp/ to .gitignore",
			} {
				if !strings.Contains(afterWorkflow, want) {
					t.Errorf("post-execute report missing %q:\n%s", want, afterWorkflow)
				}
			}
		})
	}
}

func TestInitConfirmNamesEveryWriteClass(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	restore := SetCommandRunner(func(ctx context.Context, name string, args ...string) error {
		return nil
	})
	defer restore()

	stubWizardResult(t, initui.Result{
		TeamKey:  "LERP",
		TeamName: "Lerp",
		Stock: config.Stock{
			Teams:            []string{"LERP"},
			Plan:             true,
			Review:           true,
			PlanStatus:       "Planning",
			PlanReviewStatus: "Plan Review",
			ImplementStatus:  "Todo",
			ExitStatus:       "In Review",
			AttentionStatus:  "Needs Attention",
			Bypass:           false,
		},
		MCPIntent: initui.MCPIntentHTTP,
	}, nil)

	existing := []linear.WorkflowState{
		{Name: "Backlog", Category: "backlog"},
		{Name: "Todo", Category: "unstarted"},
		{Name: "In Progress", Category: "started"},
		{Name: "In Review", Category: "started"},
		{Name: "Done", Category: "completed"},
	}
	var out bytes.Buffer
	b := &orderBoard{
		fakeBoard: fakeBoard{
			existing: existing,
		},
		out: &out,
	}
	answers := strings.NewReader("interactive")
	_, err := Init(context.Background(), b, &out, answers, dir, "LERP", "Lerp")
	if err != nil {
		t.Fatal(err)
	}
	output := out.String()
	reportPrefix := output[:b.reportLenAtWorkflow]
	for _, want := range []string{
		"creating on team LERP: Needs Attention, Plan Review, Planning  ·  using existing: In Review (implement exit), Todo (implement)",
		"writing " + filepath.Join(dir, config.RepoConfigFile),
		"adding .lerp/ to .gitignore",
		"registering claude Linear MCP",
	} {
		if !strings.Contains(reportPrefix, want) {
			t.Errorf("confirm report missing write class %q:\n%s", want, reportPrefix)
		}
	}
}

func TestInitReportsPipelineExits(t *testing.T) {
	for name, tc := range map[string]struct {
		categories map[string]string
		want       string
		dontWant   string
	}{
		"created as started": {
			want: `pipeline exit "Review": Linear categorises it as started, not completed.`,
		},
		"existing unstarted": {
			categories: map[string]string{"Review": "unstarted"},
			want:       `pipeline exit "Review": Linear categorises it as unstarted, not completed.`,
		},
		"existing completed": {
			categories: map[string]string{"Review": "completed"},
			want:       `pipeline exit "Review": Linear categorises it as completed; tickets that land there stop blocking their dependents.`,
			dontWant:   "not completed",
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, config.RepoConfigFile), []byte(existingConfig), 0o644); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if _, err := Init(context.Background(), &fakeBoard{categories: tc.categories}, &out, nil, dir, "LERP", ""); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Errorf("report %q\nmissing %q", out.String(), tc.want)
			}
			if tc.dontWant != "" && strings.Contains(out.String(), tc.dontWant) {
				t.Errorf("report %q\ncontains %q", out.String(), tc.dontWant)
			}
			for _, notAnExit := range []string{`exit "Implementing"`, `exit "Human Review"`} {
				if strings.Contains(out.String(), notAnExit) {
					t.Errorf("report %q\nnames a non-exit: %s", out.String(), notAnExit)
				}
			}
		})
	}
}

func TestInitReportsStatusOwnership(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir string)
	}{
		{name: "fresh"},
		{
			name: "repeat",
			setup: func(t *testing.T, dir string) {
				path := filepath.Join(dir, config.RepoConfigFile)
				if err := os.WriteFile(path, []byte(existingConfig), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.setup != nil {
				tc.setup(t, dir)
			}
			var out bytes.Buffer
			board := &fakeBoard{existing: linearDefaults}
			if _, err := Init(context.Background(), board, &out, nil, dir, "LERP", ""); err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{
				"lerp now drives team LERP by moving tickets between statuses",
			} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("report %q\nmissing %q", out.String(), want)
				}
			}
			for _, dontWant := range []string{
				"team LERP's workflow settings",
				"No action",
				"unless your pipeline has a stage that runs after the merge",
			} {
				if strings.Contains(out.String(), dontWant) {
					t.Errorf("report %q\ncontains trimmed text %q", out.String(), dontWant)
				}
			}
		})
	}
}

func TestStateSpecsCoverExactlyTheReportedStatuses(t *testing.T) {
	cfg, err := config.ParseRepoConfig(existingConfig, "lerp.toml")
	if err != nil {
		t.Fatal(err)
	}
	var created []string
	for _, spec := range stateSpecs(cfg) {
		if spec.Type != "started" {
			t.Errorf("state %q created as %q, want started", spec.Name, spec.Type)
		}
		created = append(created, spec.Name)
	}
	var reported []string
	for name := range statusRoles(cfg) {
		reported = append(reported, name)
	}
	sort.Strings(reported)
	if !reflect.DeepEqual(created, reported) {
		t.Errorf("stateSpecs = %v, statusRoles keys = %v", created, reported)
	}
	want := []string{"Human Review", "Implementing", "Planning", "Review"}
	if !reflect.DeepEqual(created, want) {
		t.Errorf("stateSpecs = %v, want %v", created, want)
	}
}

func TestInitStopsWhenBoardFails(t *testing.T) {
	dir := t.TempDir()
	_, err := Init(context.Background(), &fakeBoard{err: errors.New("no access")}, nil, nil, dir, "LERP", "")
	if err == nil || !strings.Contains(err.Error(), "no access") {
		t.Fatalf("Init error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, config.RepoConfigFile)); !os.IsNotExist(statErr) {
		t.Fatalf("config was created: %v", statErr)
	}
}

func TestInitIgnoresStateDir(t *testing.T) {
	for _, tc := range []struct {
		name    string
		before  string
		want    string
		message string
	}{
		{
			name:    "creates the file when there is none",
			want:    stateDirBlock,
			message: "added .lerp/ to .gitignore",
		},
		{
			name:    "appends after a trailing newline",
			before:  "*.out\ncoverage.*\n",
			want:    "*.out\ncoverage.*\n\n" + stateDirBlock,
			message: "added .lerp/ to .gitignore",
		},
		{
			name:    "appends after a file with no trailing newline",
			before:  "*.out",
			want:    "*.out\n\n" + stateDirBlock,
			message: "added .lerp/ to .gitignore",
		},
		{
			name:    "appends after a file that already ends blank",
			before:  "*.out\n\n",
			want:    "*.out\n\n" + stateDirBlock,
			message: "added .lerp/ to .gitignore",
		},
		{
			name:    "leaves a repo that already ignores it alone",
			before:  "*.out\n.lerp/\n",
			want:    "*.out\n.lerp/\n",
			message: ".gitignore already ignores .lerp/",
		},
		{
			name:    "recognises the rooted spelling",
			before:  "  /.lerp  \n",
			want:    "  /.lerp  \n",
			message: ".gitignore already ignores .lerp/",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ".gitignore")
			if tc.before != "" {
				if err := os.WriteFile(path, []byte(tc.before), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			if _, err := Init(context.Background(), &fakeBoard{existing: linearDefaults}, &out, nil, dir, "LERP", ""); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Errorf(".gitignore = %q, want %q", got, tc.want)
			}
			if !strings.Contains(out.String(), tc.message) {
				t.Errorf("out %q\nmissing %q", out.String(), tc.message)
			}
		})
	}
}

func TestInitIgnoresStateDirOnRepeat(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.RepoConfigFile), []byte(existingConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	b := &fakeBoard{existing: boardWorkflowStates("Planning", "Implementing")}
	if _, err := Init(context.Background(), b, io.Discard, nil, dir, "LERP", ""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".gitignore")
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != stateDirBlock {
		t.Fatalf(".gitignore = %q, want %q", after, stateDirBlock)
	}
	if _, err := Init(context.Background(), b, io.Discard, nil, dir, "LERP", ""); err != nil {
		t.Fatal(err)
	}
	twice, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(twice) != string(after) {
		t.Errorf("second init changed .gitignore to %q", twice)
	}
}

func TestInitSurvivesUnusableGitignore(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{
			name: "unreadable",
			setup: func(t *testing.T, path string) {
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "unwritable",
			setup: func(t *testing.T, path string) {
				if os.Geteuid() == 0 {
					t.Skip("root writes a read-only file whatever its mode says")
				}
				if err := os.WriteFile(path, []byte("*.out\n"), 0o444); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.setup(t, filepath.Join(dir, ".gitignore"))
			var out bytes.Buffer
			created, err := Init(context.Background(), &fakeBoard{existing: linearDefaults}, &out, nil, dir, "LERP", "")
			if err != nil {
				t.Fatalf("Init error = %v", err)
			}
			if !created {
				t.Error("created = false: the config was not written")
			}
			if _, statErr := os.Stat(filepath.Join(dir, config.RepoConfigFile)); statErr != nil {
				t.Fatalf("config missing: %v", statErr)
			}
			for _, wanted := range []string{"could not ignore .lerp/", "Add .lerp/ to .gitignore yourself"} {
				if !strings.Contains(out.String(), wanted) {
					t.Errorf("out %q\nmissing %q", out.String(), wanted)
				}
			}
		})
	}
}

func TestInitMCPDeclinedOnNonInteractive(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	var out bytes.Buffer
	var commandsRun [][]string
	restore := SetCommandRunner(func(ctx context.Context, name string, args ...string) error {
		commandsRun = append(commandsRun, append([]string{name}, args...))
		return nil
	})
	defer restore()

	b := &fakeBoard{existing: linearDefaults}
	_, err := Init(context.Background(), b, &out, nil, dir, "LERP", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(commandsRun) != 0 {
		t.Errorf("commands run = %v, want none on non-interactive", commandsRun)
	}
	output := out.String()
	for _, want := range []string{
		"claude: Linear MCP not configured",
		"register: claude mcp add --transport http linear https://mcp.linear.app/mcp",
		"alternative (shared OAuth): claude mcp add linear -- npx -y mcp-remote https://mcp.linear.app/mcp",
		"then authenticate: /mcp in claude",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q:\n%s", want, output)
		}
	}
}

func TestInitMCPDeclinedInteractively(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	var out bytes.Buffer
	var commandsRun [][]string
	restore := SetCommandRunner(func(ctx context.Context, name string, args ...string) error {
		commandsRun = append(commandsRun, append([]string{name}, args...))
		return nil
	})
	defer restore()

	stubWizardResult(t, initui.Result{
		TeamKey:  "LERP",
		TeamName: "Lerp",
		Stock: config.Stock{
			Teams:  []string{"LERP"},
			Plan:   true,
			Review: true,
		},
		MCPIntent: initui.MCPIntentNone,
	}, nil)

	answers := strings.NewReader("interactive")
	b := &fakeBoard{existing: linearDefaults}
	_, err := Init(context.Background(), b, &out, answers, dir, "LERP", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(commandsRun) != 0 {
		t.Errorf("commands run = %v, want none on declined", commandsRun)
	}
	output := out.String()
	for _, want := range []string{
		"claude: Linear MCP not configured",
		"register: claude mcp add --transport http linear https://mcp.linear.app/mcp",
		"then authenticate: /mcp in claude",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q:\n%s", want, output)
		}
	}
}

func TestInitMCPRegisteredOnYes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	var out bytes.Buffer
	var commandsRun [][]string
	restore := SetCommandRunner(func(ctx context.Context, name string, args ...string) error {
		commandsRun = append(commandsRun, append([]string{name}, args...))
		return nil
	})
	defer restore()

	stubWizardResult(t, initui.Result{
		TeamKey:  "LERP",
		TeamName: "Lerp",
		Stock: config.Stock{
			Teams:  []string{"LERP"},
			Plan:   true,
			Review: true,
		},
		MCPIntent: initui.MCPIntentHTTP,
	}, nil)

	answers := strings.NewReader("interactive")
	b := &fakeBoard{existing: linearDefaults}
	_, err := Init(context.Background(), b, &out, answers, dir, "LERP", "")
	if err != nil {
		t.Fatal(err)
	}
	wantCmd := []string{"claude", "mcp", "add", "--transport", "http", "linear", "https://mcp.linear.app/mcp"}
	if len(commandsRun) != 1 || !reflect.DeepEqual(commandsRun[0], wantCmd) {
		t.Errorf("commands run = %v, want [%v]", commandsRun, wantCmd)
	}
	output := out.String()
	wantReport := "claude: registered Linear MCP — one-time authentication still needed: /mcp in claude"
	if !strings.Contains(output, wantReport) {
		t.Errorf("output missing %q:\n%s", wantReport, output)
	}
}

func TestInitMCPRegisteredOnBridge(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	var out bytes.Buffer
	var commandsRun [][]string
	restore := SetCommandRunner(func(ctx context.Context, name string, args ...string) error {
		commandsRun = append(commandsRun, append([]string{name}, args...))
		return nil
	})
	defer restore()

	stubWizardResult(t, initui.Result{
		TeamKey:  "LERP",
		TeamName: "Lerp",
		Stock: config.Stock{
			Teams:  []string{"LERP"},
			Plan:   true,
			Review: true,
		},
		MCPIntent: initui.MCPIntentBridge,
	}, nil)

	answers := strings.NewReader("interactive")
	b := &fakeBoard{existing: linearDefaults}
	_, err := Init(context.Background(), b, &out, answers, dir, "LERP", "")
	if err != nil {
		t.Fatal(err)
	}
	wantCmd := []string{"claude", "mcp", "add", "linear", "--", "npx", "-y", "mcp-remote", "https://mcp.linear.app/mcp"}
	if len(commandsRun) != 1 || !reflect.DeepEqual(commandsRun[0], wantCmd) {
		t.Errorf("commands run = %v, want [%v]", commandsRun, wantCmd)
	}
	output := out.String()
	wantReport := "claude: registered Linear MCP — one-time authentication still needed: /mcp in claude"
	if !strings.Contains(output, wantReport) {
		t.Errorf("output missing %q:\n%s", wantReport, output)
	}
}

func TestInitMCPAlreadyConfigured(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if err := os.WriteFile(dir+"/.claude.json", []byte(`{"mcpServers":{"linear":{"type":"http"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	var commandsRun [][]string
	restore := SetCommandRunner(func(ctx context.Context, name string, args ...string) error {
		commandsRun = append(commandsRun, append([]string{name}, args...))
		return nil
	})
	defer restore()

	stubWizardResult(t, initui.Result{
		TeamKey:  "LERP",
		TeamName: "Lerp",
		Stock: config.Stock{
			Teams:  []string{"LERP"},
			Plan:   true,
			Review: true,
		},
	}, nil)

	answers := strings.NewReader("interactive")
	b := &fakeBoard{existing: linearDefaults}
	_, err := Init(context.Background(), b, &out, answers, dir, "LERP", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(commandsRun) != 0 {
		t.Errorf("commands run = %v, want none when already configured", commandsRun)
	}
	output := out.String()
	if !strings.Contains(output, "claude: Linear MCP already configured") {
		t.Errorf("output missing already configured report:\n%s", output)
	}
}

func TestInitMCPMultipleVendorsOnRepeat(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	multiVendorConfig := `
teams = ["LERP"]
provision = "mine"
dispose = "mine"

[runners.agy-implement]
vendor = "antigravity"

[runners.codex-plan]
vendor = "codex"

[queues.plan]
status = "Planning"
prompt = "Plan {{ticket}}."
runner = "codex-plan"
on_success = "Implementing"

[queues.code]
status = "Implementing"
prompt = "Implement {{ticket}}."
runner = "agy-implement"
on_success = "In Review"
`
	if err := os.WriteFile(filepath.Join(dir, config.RepoConfigFile), []byte(multiVendorConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	var commandsRun [][]string
	restore := SetCommandRunner(func(ctx context.Context, name string, args ...string) error {
		commandsRun = append(commandsRun, append([]string{name}, args...))
		return nil
	})
	defer restore()

	stubWizardResult(t, initui.Result{
		TeamKey:   "LERP",
		TeamName:  "Lerp",
		MCPIntent: initui.MCPIntentHTTP,
	}, nil)

	answers := strings.NewReader("interactive")
	b := &fakeBoard{existing: boardWorkflowStates("Planning", "Implementing", "In Review")}
	_, err := Init(context.Background(), b, &out, answers, dir, "LERP", "")
	if err != nil {
		t.Fatal(err)
	}

	wantAgy := []string{"agy", "mcp", "add", "linear", "https://mcp.linear.app/mcp"}
	wantCodex := []string{"codex", "mcp", "add", "linear", "--url", "https://mcp.linear.app/mcp"}
	if len(commandsRun) != 2 || !reflect.DeepEqual(commandsRun[0], wantAgy) || !reflect.DeepEqual(commandsRun[1], wantCodex) {
		t.Errorf("commands run = %v, want [%v, %v]", commandsRun, wantAgy, wantCodex)
	}

	output := out.String()
	for _, want := range []string{
		"antigravity (agy): registered Linear MCP — one-time authentication still needed: the /mcp overlay in agy",
		"codex: registered Linear MCP — one-time authentication still needed: codex mcp login linear",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q:\n%s", want, output)
		}
	}
}

func TestInitMCPRegistrationFailureReportsError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	var out bytes.Buffer
	restore := SetCommandRunner(func(ctx context.Context, name string, args ...string) error {
		return errors.New("command not found in PATH")
	})
	defer restore()

	stubWizardResult(t, initui.Result{
		TeamKey:  "LERP",
		TeamName: "Lerp",
		Stock: config.Stock{
			Teams:  []string{"LERP"},
			Plan:   true,
			Review: true,
		},
		MCPIntent: initui.MCPIntentHTTP,
	}, nil)

	answers := strings.NewReader("interactive")
	b := &fakeBoard{existing: linearDefaults}
	_, err := Init(context.Background(), b, &out, answers, dir, "LERP", "")
	if err != nil {
		t.Fatal(err)
	}
	output := out.String()
	for _, want := range []string{
		"could not register claude MCP: command not found in PATH",
		"claude: Linear MCP not configured",
		"register: claude mcp add --transport http linear https://mcp.linear.app/mcp",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q:\n%s", want, output)
		}
	}
}

func TestInitReportsCollidingAutomationOnConfirm(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	b := &orderBoard{
		fakeBoard: fakeBoard{
			existing: linearDefaults,
			automations: []linear.GitAutomation{
				{Event: linear.GitEventStart, Status: "In Progress"},
			},
		},
		out: &out,
	}
	_, err := Init(context.Background(), b, &out, nil, dir, "LERP", "")
	if err != nil {
		t.Fatal(err)
	}
	output := out.String()
	if b.reportLenAtWorkflow <= 0 {
		t.Fatal("EnsureWorkflowStates was not called")
	}
	reportPrefix := output[:b.reportLenAtWorkflow]
	for _, want := range []string{
		`team LERP: "On PR open" moves tickets to "In Progress", which the repo config never names:`,
		`  a run in "Planning" that opens a pull request will be moved there mid-stage, losing its on_success hop to "Plan Review"`,
		`fix: set that automation to No action for team LERP, or point a queue at "In Progress"`,
	} {
		if !strings.Contains(reportPrefix, want) {
			t.Errorf("confirm report missing %q:\n%s", want, reportPrefix)
		}
	}
}

func TestInitReportsCleanWhenPipelineNamesTarget(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	b := &fakeBoard{
		existing: linearDefaults,
		automations: []linear.GitAutomation{
			{Event: linear.GitEventStart, Status: "Plan Review"},
		},
	}
	_, err := Init(context.Background(), b, &out, nil, dir, "LERP", "")
	if err != nil {
		t.Fatal(err)
	}
	want := "team LERP has no pull-request automation that would move a ticket mid-stage"
	if !strings.Contains(out.String(), want) {
		t.Errorf("output missing clean line %q:\n%s", want, out.String())
	}
	if strings.Contains(out.String(), "fix: set that automation") {
		t.Errorf("output contains unexpected finding:\n%s", out.String())
	}
}

func TestInitReportsCleanWhenNoAutomations(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	b := &fakeBoard{existing: linearDefaults}
	_, err := Init(context.Background(), b, &out, nil, dir, "LERP", "")
	if err != nil {
		t.Fatal(err)
	}
	want := "team LERP has no pull-request automation that would move a ticket mid-stage"
	if !strings.Contains(out.String(), want) {
		t.Errorf("output missing clean line %q:\n%s", want, out.String())
	}
}

func TestInitSurvivesUnreadableAutomations(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	b := &fakeBoard{
		existing:       linearDefaults,
		automationsErr: errors.New("network timeout"),
	}
	created, err := Init(context.Background(), b, &out, nil, dir, "LERP", "")
	if err != nil {
		t.Fatalf("Init = %v, want success despite unreadable automations", err)
	}
	if !created {
		t.Error("created = false, want true")
	}
	want := "team LERP: could not read git automations, so they are not checked: network timeout"
	if !strings.Contains(out.String(), want) {
		t.Errorf("output missing warning %q:\n%s", want, out.String())
	}
}

func TestInitReportsAutomationsOnCreatedTeam(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	b := &fakeBoard{
		teamNotFound: true,
		automations: []linear.GitAutomation{
			{Event: linear.GitEventStart, Status: "In Progress"},
		},
	}
	created, err := Init(context.Background(), b, &out, nil, dir, "NEWTEAM", "New Team")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("created = false, want true")
	}
	output := out.String()
	for _, want := range []string{
		"creating team NEWTEAM (New Team)",
		"lerp now drives team NEWTEAM by moving tickets between statuses",
		`team NEWTEAM: "On PR open" moves tickets to "In Progress", which the repo config never names:`,
		`fix: set that automation to No action for team NEWTEAM, or point a queue at "In Progress"`,
	} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q:\n%s", want, output)
		}
	}
}

func TestInitTeamGivenAndExistsDoesNotAskOrCallEnsureTeam(t *testing.T) {
	dir := t.TempDir()
	b := &fakeBoard{
		teams:    []linear.TeamRef{{Key: "LERP", Name: "Lerp"}},
		existing: linearDefaults,
	}
	var out bytes.Buffer
	stubWizardResult(t, initui.Result{
		TeamKey:  "LERP",
		TeamName: "Lerp",
		Stock: config.Stock{
			Teams:  []string{"LERP"},
			Plan:   true,
			Review: true,
			Bypass: true,
		},
	}, nil)

	answers := strings.NewReader("interactive")
	created, err := Init(context.Background(), b, &out, answers, dir, "LERP", "Lerp")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("created = false, want true")
	}
	if b.ensureCalls != 0 {
		t.Errorf("ensureCalls = %d, want 0 when key already exists", b.ensureCalls)
	}
}

func TestInitTeamGivenAndMissingTerminalConfirmsCreate(t *testing.T) {
	dir := t.TempDir()
	b := &fakeBoard{
		teams:    []linear.TeamRef{{Key: "ENG", Name: "Engineering"}},
		existing: linearDefaults,
	}
	var out bytes.Buffer
	stubWizardResult(t, initui.Result{
		TeamKey:    "ACEM",
		TeamName:   "Acme Marketing",
		CreateTeam: true,
		Stock: config.Stock{
			Teams:  []string{"ACEM"},
			Plan:   true,
			Review: true,
			Bypass: true,
		},
	}, nil)

	answers := strings.NewReader("interactive")
	created, err := Init(context.Background(), b, &out, answers, dir, "ACEM", "Acme Marketing")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("created = false, want true")
	}
	if b.ensureCalls != 1 || b.teamKey != "ACEM" || b.teamName != "Acme Marketing" {
		t.Errorf("EnsureTeam = (%q, %q), calls = %d", b.teamKey, b.teamName, b.ensureCalls)
	}
}

func TestInitTeamGivenAndMissingTerminalDeclinesCreate(t *testing.T) {
	dir := t.TempDir()
	b := &fakeBoard{
		teams:    []linear.TeamRef{{Key: "ENG", Name: "Engineering"}},
		existing: linearDefaults,
	}
	var out bytes.Buffer
	stubWizardResult(t, initui.Result{}, errors.New("team \"ACEM\" not created"))

	answers := strings.NewReader("interactive")
	_, err := Init(context.Background(), b, &out, answers, dir, "ACEM", "Acme Marketing")
	if err == nil || !strings.Contains(err.Error(), `team "ACEM" not created`) {
		t.Fatalf("error = %v, want team ACEM not created", err)
	}
	if b.ensureCalls != 0 {
		t.Errorf("ensureCalls = %d, want 0 when declined", b.ensureCalls)
	}
	if _, statErr := os.Stat(filepath.Join(dir, config.RepoConfigFile)); !os.IsNotExist(statErr) {
		t.Fatalf("config created despite decline: %v", statErr)
	}
}

func TestInitTeamGivenAndMissingNonInteractiveCreatesSilently(t *testing.T) {
	dir := t.TempDir()
	b := &fakeBoard{
		teams:    []linear.TeamRef{{Key: "ENG", Name: "Engineering"}},
		existing: linearDefaults,
	}
	var out bytes.Buffer
	created, err := Init(context.Background(), b, &out, nil, dir, "ACEM", "Acme Marketing")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("created = false, want true")
	}
	if b.ensureCalls != 1 || b.teamKey != "ACEM" || b.teamName != "Acme Marketing" {
		t.Errorf("EnsureTeam = (%q, %q), calls = %d", b.teamKey, b.teamName, b.ensureCalls)
	}
	if strings.Contains(out.String(), "?") {
		t.Errorf("non-interactive init asked a question:\n%s", out.String())
	}
}

func TestInitTeamAbsentTerminalPicksFromNumberedList(t *testing.T) {
	dir := t.TempDir()
	b := &fakeBoard{
		teams: []linear.TeamRef{
			{Key: "ENG", Name: "Engineering"},
			{Key: "LERP", Name: "Lerp"},
		},
		existing: linearDefaults,
	}
	var out bytes.Buffer
	stubWizardResult(t, initui.Result{
		TeamKey:    "LERP",
		TeamName:   "Lerp",
		CreateTeam: false,
		Stock: config.Stock{
			Teams:  []string{"LERP"},
			Plan:   true,
			Review: true,
			Bypass: true,
		},
	}, nil)

	answers := strings.NewReader("interactive")
	created, err := Init(context.Background(), b, &out, answers, dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("created = false, want true")
	}
	if b.ensureCalls != 0 {
		t.Errorf("ensureCalls = %d, want 0 for existing team pick", b.ensureCalls)
	}
	c, err := config.LoadRepoConfig(filepath.Join(dir, config.RepoConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Teams, []string{"LERP"}) {
		t.Errorf("teams = %v, want [LERP]", c.Teams)
	}
}

func TestInitTeamAbsentTerminalSelectsCreateRow(t *testing.T) {
	dir := t.TempDir()
	b := &fakeBoard{
		teams: []linear.TeamRef{
			{Key: "LERP", Name: "Lerp"},
		},
		existing: linearDefaults,
	}
	var out bytes.Buffer
	stubWizardResult(t, initui.Result{
		TeamKey:    "ACEM",
		TeamName:   "Acme Marketing",
		CreateTeam: true,
		Stock: config.Stock{
			Teams:  []string{"ACEM"},
			Plan:   true,
			Review: true,
			Bypass: true,
		},
	}, nil)

	answers := strings.NewReader("interactive")
	created, err := Init(context.Background(), b, &out, answers, dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("created = false, want true")
	}
	if b.ensureCalls != 1 || b.teamKey != "ACEM" || b.teamName != "Acme Marketing" {
		t.Errorf("EnsureTeam = (%q, %q), calls = %d", b.teamKey, b.teamName, b.ensureCalls)
	}
	c, err := config.LoadRepoConfig(filepath.Join(dir, config.RepoConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Teams, []string{"ACEM"}) {
		t.Errorf("teams = %v, want [ACEM]", c.Teams)
	}
}

func TestInitTeamAbsentTerminalCreateRowUsesDefaultTeamName(t *testing.T) {
	dir := t.TempDir()
	b := &fakeBoard{
		teams: []linear.TeamRef{
			{Key: "LERP", Name: "Lerp"},
		},
		existing: linearDefaults,
	}
	var out bytes.Buffer
	stubWizardResult(t, initui.Result{
		TeamKey:    "ACEM",
		TeamName:   "Provided Name",
		CreateTeam: true,
		Stock: config.Stock{
			Teams:  []string{"ACEM"},
			Plan:   true,
			Review: true,
			Bypass: true,
		},
	}, nil)

	answers := strings.NewReader("interactive")
	created, err := Init(context.Background(), b, &out, answers, dir, "", "Provided Name")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("created = false, want true")
	}
	if b.ensureCalls != 1 || b.teamKey != "ACEM" || b.teamName != "Provided Name" {
		t.Errorf("EnsureTeam = (%q, %q), calls = %d", b.teamKey, b.teamName, b.ensureCalls)
	}
}

func TestInitTeamAbsentNonInteractiveErrors(t *testing.T) {
	dir := t.TempDir()
	b := &fakeBoard{
		teams:    []linear.TeamRef{{Key: "LERP", Name: "Lerp"}},
		existing: linearDefaults,
	}
	_, err := Init(context.Background(), b, nil, nil, dir, "", "")
	if err == nil || !strings.Contains(err.Error(), "--team is required") {
		t.Fatalf("error = %v, want --team is required", err)
	}
	if b.ensureCalls != 0 {
		t.Errorf("ensureCalls = %d, want 0", b.ensureCalls)
	}
}

func TestInitWorkspaceHasNoTeamsGoesStraightToCreate(t *testing.T) {
	dir := t.TempDir()
	b := &fakeBoard{
		teams:    []linear.TeamRef{},
		existing: linearDefaults,
	}
	var out bytes.Buffer
	stubWizardResult(t, initui.Result{
		TeamKey:    "ACEM",
		TeamName:   "Acme",
		CreateTeam: true,
		Stock: config.Stock{
			Teams:  []string{"ACEM"},
			Plan:   true,
			Review: true,
			Bypass: true,
		},
	}, nil)

	answers := strings.NewReader("interactive")
	created, err := Init(context.Background(), b, &out, answers, dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("created = false, want true")
	}
	if b.ensureCalls != 1 || b.teamKey != "ACEM" || b.teamName != "Acme" {
		t.Errorf("EnsureTeam = (%q, %q), calls = %d", b.teamKey, b.teamName, b.ensureCalls)
	}
}

func TestInitTeamQuestionEOFErrorsWithoutCreating(t *testing.T) {
	dir := t.TempDir()
	b := &fakeBoard{
		teams:    []linear.TeamRef{{Key: "LERP", Name: "Lerp"}},
		existing: linearDefaults,
	}
	stubWizardResult(t, initui.Result{}, ErrCanceled)

	answers := strings.NewReader("interactive")
	_, err := Init(context.Background(), b, nil, answers, dir, "", "")
	if err == nil {
		t.Fatal("want error on EOF at team question, got nil")
	}
	if b.ensureCalls != 0 {
		t.Errorf("ensureCalls = %d, want 0", b.ensureCalls)
	}
	if _, statErr := os.Stat(filepath.Join(dir, config.RepoConfigFile)); !os.IsNotExist(statErr) {
		t.Fatalf("config created on EOF: %v", statErr)
	}
}

func TestInitRepeatInitSingleConfiguredTeamSeedsWithoutAsking(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, config.RepoConfigFile)
	if err := os.WriteFile(path, []byte(existingConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	b := &fakeBoard{
		teams: []linear.TeamRef{
			{Key: "ENG", Name: "Engineering"},
			{Key: "LERP", Name: "Lerp"},
		},
		existing: []linear.WorkflowState{{Name: "Planning", Category: "started"}, {Name: "Implementing", Category: "started"}},
	}
	var out bytes.Buffer
	stubWizardResult(t, initui.Result{
		TeamKey:  "LERP",
		TeamName: "Lerp",
	}, nil)

	answers := strings.NewReader("interactive")
	created, err := Init(context.Background(), b, &out, answers, dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("created = true, want false for repeat init")
	}
	if b.ensureCalls != 0 {
		t.Errorf("ensureCalls = %d, want 0", b.ensureCalls)
	}
	if !strings.Contains(out.String(), "on team LERP:") {
		t.Errorf("output missing LERP report:\n%s", out.String())
	}
}

func TestInitRepeatInitMultipleConfiguredTeamsSeedsPickerList(t *testing.T) {
	dir := t.TempDir()
	multiConfig := strings.ReplaceAll(existingConfig, `teams = ["LERP"]`, `teams = ["ENG", "LERP"]`)
	path := filepath.Join(dir, config.RepoConfigFile)
	if err := os.WriteFile(path, []byte(multiConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	b := &fakeBoard{
		teams: []linear.TeamRef{
			{Key: "ACEM", Name: "Acme Marketing"},
			{Key: "ENG", Name: "Engineering"},
			{Key: "LERP", Name: "Lerp"},
		},
		existing: []linear.WorkflowState{{Name: "Planning", Category: "started"}, {Name: "Implementing", Category: "started"}},
	}
	var out bytes.Buffer
	stubWizardResult(t, initui.Result{
		TeamKey:  "LERP",
		TeamName: "Lerp",
	}, nil)

	answers := strings.NewReader("interactive")
	created, err := Init(context.Background(), b, &out, answers, dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("created = true, want false for repeat init")
	}
	if b.ensureCalls != 0 {
		t.Errorf("ensureCalls = %d, want 0", b.ensureCalls)
	}
	transcript := out.String()
	if !strings.Contains(transcript, "on team LERP:") {
		t.Errorf("output missing LERP report:\n%s", transcript)
	}
}

func TestInitRepeatInitSingleConfiguredTeamNoneExistInWorkspaceErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, config.RepoConfigFile)
	if err := os.WriteFile(path, []byte(existingConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	b := &fakeBoard{
		teams: []linear.TeamRef{
			{Key: "ACEM", Name: "Acme Marketing"},
		},
		existing: []linear.WorkflowState{{Name: "Planning", Category: "started"}, {Name: "Implementing", Category: "started"}},
	}
	var out bytes.Buffer
	answers := strings.NewReader("")
	_, err := Init(context.Background(), b, &out, answers, dir, "", "")
	if err == nil || !strings.Contains(err.Error(), "none of the teams configured in") {
		t.Fatalf("error = %v, want none of the teams configured in ... exist in workspace", err)
	}
}

func TestInitRepeatInitMultipleConfiguredTeamsNoneExistInWorkspaceErrors(t *testing.T) {
	dir := t.TempDir()
	multiConfig := strings.ReplaceAll(existingConfig, `teams = ["LERP"]`, `teams = ["ENG", "LERP"]`)
	path := filepath.Join(dir, config.RepoConfigFile)
	if err := os.WriteFile(path, []byte(multiConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	b := &fakeBoard{
		teams: []linear.TeamRef{
			{Key: "ACEM", Name: "Acme Marketing"},
		},
		existing: []linear.WorkflowState{{Name: "Planning", Category: "started"}, {Name: "Implementing", Category: "started"}},
	}
	var out bytes.Buffer
	answers := strings.NewReader("1\n")
	_, err := Init(context.Background(), b, &out, answers, dir, "", "")
	if err == nil || !strings.Contains(err.Error(), "none of the teams configured in") {
		t.Fatalf("error = %v, want none of the teams configured in ... exist in workspace", err)
	}
}

func TestInitRepeatInitMultipleConfiguredTeamsOneExistsAutoPicks(t *testing.T) {
	dir := t.TempDir()
	multiConfig := strings.ReplaceAll(existingConfig, `teams = ["LERP"]`, `teams = ["ENG", "LERP"]`)
	path := filepath.Join(dir, config.RepoConfigFile)
	if err := os.WriteFile(path, []byte(multiConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	b := &fakeBoard{
		teams: []linear.TeamRef{
			{Key: "ACEM", Name: "Acme Marketing"},
			{Key: "LERP", Name: "Lerp"},
		},
		existing: []linear.WorkflowState{{Name: "Planning", Category: "started"}, {Name: "Implementing", Category: "started"}},
	}
	var out bytes.Buffer
	stubWizardResult(t, initui.Result{
		TeamKey:  "LERP",
		TeamName: "Lerp",
	}, nil)

	answers := strings.NewReader("interactive")
	created, err := Init(context.Background(), b, &out, answers, dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("created = true, want false for repeat init")
	}
	if !strings.Contains(out.String(), "on team LERP:") {
		t.Errorf("output missing LERP report:\n%s", out.String())
	}
}

func TestInitLookPathReachesWizardOptions(t *testing.T) {
	dir := t.TempDir()
	b := &fakeBoard{existing: linearDefaults}

	var capturedOptions initui.Options
	restoreLook := SetLookPath(func(file string) (string, error) {
		if file == "codex" {
			return "/bin/codex", nil
		}
		return "", errors.New("not found")
	})
	defer restoreLook()

	restoreWiz := SetWizardRunner(func(ctx context.Context, opts initui.Options) (initui.Result, error) {
		capturedOptions = opts
		return initui.Result{
			TeamKey:  "LERP",
			TeamName: "Lerp",
			Stock: config.Stock{
				Teams:  []string{"LERP"},
				Runner: "claude",
				Plan:   true,
				Review: true,
			},
		}, nil
	})
	defer restoreWiz()

	answers := strings.NewReader("interactive")
	if _, err := Init(context.Background(), b, io.Discard, answers, dir, "LERP", ""); err != nil {
		t.Fatal(err)
	}

	if !capturedOptions.CLIInstalled["codex"] {
		t.Error("CLIInstalled[codex] = false, want true from stubbed LookPath")
	}
	if capturedOptions.CLIInstalled["claude"] {
		t.Error("CLIInstalled[claude] = true, want false from stubbed LookPath")
	}
}

func TestInitWithCodexRunnerWritesCodexConfigAndRegistersMCP(t *testing.T) {
	dir := t.TempDir()
	b := &fakeBoard{existing: linearDefaults}

	stubWizardResult(t, initui.Result{
		TeamKey:  "LERP",
		TeamName: "Lerp",
		Stock: config.Stock{
			Teams:  []string{"LERP"},
			Runner: "codex",
			Plan:   true,
			Review: true,
			Bypass: true,
		},
		MCPIntent: initui.MCPIntentHTTP,
	}, nil)

	var runCmds [][]string
	restoreCmd := SetCommandRunner(func(ctx context.Context, name string, args ...string) error {
		runCmds = append(runCmds, append([]string{name}, args...))
		return nil
	})
	defer restoreCmd()

	var out bytes.Buffer
	answers := strings.NewReader("interactive")
	created, err := Init(context.Background(), b, &out, answers, dir, "LERP", "")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("created = false, want true")
	}

	c, err := config.LoadRepoConfig(filepath.Join(dir, config.RepoConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Runners["codex"]; !ok {
		t.Fatalf("expected codex in Runners, got %v", c.Runners)
	}
	if got := c.Runners["codex"].Vendor; got != "codex" {
		t.Errorf("Runners[codex].Vendor = %q, want codex", got)
	}
	if !strings.Contains(c.Runners["codex"].Command, "--dangerously-bypass-approvals-and-sandbox") {
		t.Errorf("codex command %q missing bypass flag", c.Runners["codex"].Command)
	}
	for qname, q := range c.Queues {
		if q.Runner != "codex" {
			t.Errorf("queue %q runner = %q, want codex", qname, q.Runner)
		}
	}

	// Verify MCP command was driven at codex
	if len(runCmds) != 1 {
		t.Fatalf("expected 1 command run, got %d: %v", len(runCmds), runCmds)
	}
	wantCmd := []string{"codex", "mcp", "add", "linear", "--url", "https://mcp.linear.app/mcp"}
	if !reflect.DeepEqual(runCmds[0], wantCmd) {
		t.Errorf("command run = %v, want %v", runCmds[0], wantCmd)
	}
}

func TestPackageImports(t *testing.T) {
	for _, pkgDir := range []string{".", "../initui"} {
		fset := token.NewFileSet()
		pkgs, err := parser.ParseDir(fset, pkgDir, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", pkgDir, err)
		}
		for pkgName, pkg := range pkgs {
			for fileName, file := range pkg.Files {
				for _, imp := range file.Imports {
					path := strings.Trim(imp.Path.Value, `"`)
					if strings.Contains(path, "/internal/loop") || strings.Contains(path, "/internal/tui") {
						t.Errorf("package %s file %s illegally imports %s — init must have no dependency on loop or tui",
							pkgName, fileName, path)
					}
				}
			}
		}
	}
}
