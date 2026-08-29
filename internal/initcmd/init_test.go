package initcmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/linear"
)

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

func (b *fakeBoard) TeamWorkflowStates(_ context.Context, teamKey string) ([]linear.WorkflowState, error) {
	b.teamStatesKey = teamKey
	if b.err != nil {
		return nil, b.err
	}
	if b.teamNotFound {
		return nil, linear.ErrNotFound
	}
	return b.existing, nil
}

func (b *fakeBoard) TeamGitAutomations(_ context.Context, _ string) ([]linear.GitAutomation, error) {
	if b.automationsErr != nil {
		return nil, b.automationsErr
	}
	return b.automations, nil
}

func (b *fakeBoard) EnsureWorkflowStates(_ context.Context, _ string, states []linear.StateSpec) (map[string]string, error) {
	b.states = append([]linear.StateSpec(nil), states...)
	if b.err != nil {
		return nil, b.err
	}
	categories := map[string]string{}
	for _, state := range b.existing {
		cat := state.Category
		if cat == "" {
			cat = "unstarted"
		}
		categories[state.Name] = cat
	}
	for name, category := range b.categories {
		categories[name] = category
	}
	for _, s := range states {
		if _, ok := categories[s.Name]; !ok {
			categories[s.Name] = s.Type
		}
	}
	return categories, nil
}

// linearDefaults is the board a fresh Linear team comes with.
var linearDefaults = []linear.WorkflowState{
	{Name: "Backlog", Category: "backlog"},
	{Name: "Todo", Category: "unstarted"},
	{Name: "In Progress", Category: "started"},
	{Name: "Done", Category: "completed"},
	{Name: "Canceled", Category: "canceled"},
}

func boardWorkflowStates(names ...string) []linear.WorkflowState {
	states := make([]linear.WorkflowState, len(names))
	for i, name := range names {
		states[i] = linear.WorkflowState{Name: name, Category: "started"}
	}
	return states
}

// existingConfig is a hand-rolled lerp.toml whose queues differ from the
// stock pipeline, so tests can tell which config drove the board setup.
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
	// Fast path with every default, then an explicit yes to the grant.
	answers := strings.NewReader("\n\n\ny\n")
	created, err := Init(context.Background(), b, &out, answers, dir, "LERP", "Lerp")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("created = false, want true")
	}
	// Every status the stock pipeline names, all "started": init never infers
	// a completed category — "In Review" is only ever an on_success target,
	// but whether it ends work is the operator's call, reported by init, not
	// guessed.
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
	// A fresh adopter gets the gated pipeline: the plan lands in a status
	// init created and no queue serves, so it waits for a human promote.
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

// The conversation orients before it asks, asks the three short questions,
// and reports created-vs-found before acting — never silently.
func TestInitFastPathConversation(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	answers := strings.NewReader("\n\n\n\n")
	if _, err := Init(context.Background(), &fakeBoard{existing: linearDefaults}, &out, answers, dir, "LERP", ""); err != nil {
		t.Fatal(err)
	}
	transcript := out.String()
	inOrder := []string{
		"team LERP has:\n  backlog    Backlog\n  unstarted  Todo\n  started    In Progress\n  completed  Done\n  canceled   Canceled",
		"Include a planning stage? [Y/n]",
		"Review each change before it exits? [Y/n]",
		"the pipeline references: Planning, Plan Review, Implementing, In Review, Needs Attention",
		"Create these 5 statuses on team LERP? [Y]es / [c]ustomize",
		"Include --permission-mode bypassPermissions? [y/N]",
		"creating on team LERP: Implementing, In Review, Needs Attention, Plan Review, Planning",
	}
	rest := transcript
	for _, wanted := range inOrder {
		i := strings.Index(rest, wanted)
		if i < 0 {
			t.Fatalf("transcript missing (or out of order) %q:\n%s", wanted, transcript)
		}
		rest = rest[i+len(wanted):]
	}
	// The board question comes after init has said what it will do — the
	// grant question is the last one, so it must follow the stage questions.
	if strings.Index(transcript, "bypassPermissions?") < strings.Index(transcript, "Create these 5 statuses") {
		t.Error("bypass question asked before the board plan")
	}
	// Nothing on the fresh board matched, so nothing reads "using existing".
	if strings.Contains(transcript, "using existing") {
		t.Errorf("transcript claims existing statuses were used:\n%s", transcript)
	}
}

// Customize maps the pipeline onto statuses the operator already has;
// existing statuses are used, never modified, and only the rest are created.
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
	// plan yes, review yes, customize; plan → create, plan review → create,
	// implement → 2) Todo, exit → default (In Review already exists),
	// failures → create; decline the grant. The review pass asks for no
	// status of its own — it runs inside implement.
	answers := strings.NewReader("\n\nc\nc\nc\n2\n\nc\nn\n")
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
	for _, wanted := range []string{
		`implement runs in:  1) Backlog (backlog)  2) Todo (unstarted) `,
		`c) create "Implementing"`,
		`plans wait for approval in:  1) Backlog (backlog)  2) Todo (unstarted) `,
		`c) create "Plan Review"`,
		// The stock exit already exists, so its pick defaults to that status
		// and offers nothing to create.
		`finished work exits to:  1) Backlog (backlog)  2) Todo (unstarted)  3) In Progress (started)  4) In Review (started)  5) Done (completed)  [4]`,
		"creating on team LERP: Needs Attention, Plan Review, Planning  ·  using existing: In Review (implement exit), Todo (implement)",
	} {
		if !strings.Contains(transcript, wanted) {
			t.Errorf("transcript missing %q:\n%s", wanted, transcript)
		}
	}
}

// Declining the review pass takes paragraphs out of the implement prompt and
// nothing else: reviewing is not a queue, so it has no status to rewire and
// the board is the same shape either way.
func TestInitDeclinedReviewDropsThePassNotAQueue(t *testing.T) {
	dir := t.TempDir()
	answers := strings.NewReader("\nn\n\n\n")
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
	// The exit contract is not part of the review pass: a run that never
	// reviews still owes the board one of the two endings. Its prose is all
	// verdict-comment and draft-state vocabulary, so it is the paragraph most
	// likely to be tidied into the review section by mistake.
	for _, ending := range []string{"ends one of exactly two ways", "marked ready for review", "looking finished when it is not"} {
		if !strings.Contains(c.Queues["implement"].Prompt, ending) {
			t.Errorf("declined-review implement prompt lost its exit contract: no %q", ending)
		}
	}
	// The title convention is not part of the review pass either, and it is one
	// of the paragraphs abutting a `#{{review}}` marker — the marker's
	// neighbours are what a tidy-up folds into the section by accident.
	// Matched against the prompt with its wrapping flattened, so rewrapping the
	// sentence is not a failure and dropping the colon is.
	flat := strings.Join(strings.Fields(c.Queues["implement"].Prompt), " ")
	if !strings.Contains(flat, "Title the pull request you open with {{ticket}}, a colon") {
		t.Error("declined-review implement prompt lost the pull request title convention")
	}
	// The declined pass costs the board nothing: no status disappears with it.
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
	// The explanatory comments are part of the product and must survive
	// assembly, as must the prompts' run-time placeholders.
	for _, wanted := range []string{"# Check this file in", "{{on_failure}}", "[queues.plan]"} {
		if !strings.Contains(string(raw), wanted) {
			t.Errorf("written file missing %q", wanted)
		}
	}
}

// Declining planning drops the plan queue entirely; routing-by-placement
// means nothing else changes.
func TestInitDeclinedPlanningDropsPlanQueue(t *testing.T) {
	dir := t.TempDir()
	answers := strings.NewReader("n\n\n\n\n")
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

// No answer source — a piped init, or --yes — takes the stock answer to
// everything: the full pipeline under the stock names, with the grant
// declined, and still says loudly what it creates.
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
		"declined": strings.NewReader("\n\n\nn\n"),
		"nil":      nil,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
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
	answers := strings.NewReader("n\nn\nn\nn\n")
	created, err := Init(context.Background(), b, &out, answers, dir, "LERP", "")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("created = true, want false for existing config")
	}
	if answers.Len() != len("n\nn\nn\nn\n") {
		t.Error("conversation consumed answers although no config was written")
	}
	// Board setup follows the existing config's queues, not the stock ones.
	want := []linear.StateSpec{
		{Name: "Human Review", Type: "started"},
		{Name: "Implementing", Type: "started"},
		{Name: "Planning", Type: "started"},
		{Name: "Review", Type: "started"},
	}
	if !reflect.DeepEqual(b.states, want) {
		t.Errorf("states = %+v, want %+v", b.states, want)
	}
	// Re-running init is loud about created vs found too.
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
	// Verify no lerp.toml was created
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

// A mapping that folds two queues onto one status is caught by the config
// loader before anything is written.
func TestInitRejectsMappingTwoQueuesOntoOneStatus(t *testing.T) {
	dir := t.TempDir()
	// Customize: plan → 2) Todo, plan review → create, implement → 2) Todo,
	// then defaults.
	answers := strings.NewReader("\n\nc\n2\nc\n2\nc\nc\nc\nn\n")
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

// A fresh init against a team that does not exist runs converse against an
// empty status list, reports team creation, and calls EnsureTeam in execute.
func TestInitCreatesNonExistentTeam(t *testing.T) {
	dir := t.TempDir()
	b := &fakeBoard{teamNotFound: true}
	var out bytes.Buffer
	answers := strings.NewReader("y\n\n\n\ny\n")
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
		"workspace has no team \"NEWTEAM\"",
		"Create team NEWTEAM? [y/N]",
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

// Confirm precedes execute: the entire pre-write report is printed before
// the first board write or file modification happens.
func TestInitConfirmPrecedesExecute(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answers io.Reader
	}{
		{"interactive", strings.NewReader("y\n\n\n\n")},
		{"non-interactive", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var out bytes.Buffer
			b := &orderBoard{
				fakeBoard: fakeBoard{teamNotFound: true},
				out:       &out,
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
			// Pre-write report is before the first write
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
			// Post-execute reports appear after
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

// Confirm names every write class: team creation, created statuses, adopted
// statuses, config path, .gitignore line, and MCP registrations.
func TestInitConfirmNamesEveryWriteClass(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	var commandsRun [][]string
	restore := SetCommandRunner(func(ctx context.Context, name string, args ...string) error {
		commandsRun = append(commandsRun, append([]string{name}, args...))
		return nil
	})
	defer restore()

	// Customize: plan -> create, plan review -> create, implement -> 2) Todo, exit -> 4) In Review, failures -> create; bypass -> n; MCP -> y
	answers := strings.NewReader("\n\nc\nc\nc\n2\n\nc\nn\ny\n")
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
			teamNotFound: true,
			existing:     existing,
		},
		out: &out,
	}
	// For testing all write classes together:
	// A new team where existing statuses are adopted and some created:
	// If teamNotFound is false, team creation is tested in TestInitCreatesNonExistentTeam and TestInitConfirmPrecedesExecute.
	// Here with teamNotFound: false, we have created statuses + adopted statuses + config + gitignore + MCP.
	b.teamNotFound = false
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

// The report covers exactly the on_success targets no queue watches. In
// existingConfig that is "Review" alone: "Implementing" is watched by a
// queue, and "Human Review" is only a failure route.
func TestInitReportsPipelineExits(t *testing.T) {
	for name, tc := range map[string]struct {
		categories map[string]string
		want       string
		dontWant   string
	}{
		// Init just created "Review" as started, so the report must flag it.
		"created as started": {
			want: `pipeline exit "Review": Linear categorises it as started, not completed.`,
		},
		// A pre-existing human column keeps its category and gets the nudge.
		"existing unstarted": {
			categories: map[string]string{"Review": "unstarted"},
			want:       `pipeline exit "Review": Linear categorises it as unstarted, not completed.`,
		},
		// A properly terminal exit is confirmed, not warned about.
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

// The prerequisite lerp cannot satisfy from here: the status field on the
// team it serves. Every init says it — a fresh one and a repeat alike, since
// a repo set up by an earlier lerp only hears it by repeating init, and the
// team's automations can change long after setup.
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

// What init reports and what it creates are one set. stateSpecs is derived
// from statusRoles' keys, so this holds by construction and the assertion
// cannot fail as written — it is a tripwire against someone giving stateSpecs
// a second walk of the queues again, because the pair drifting apart is
// exactly the failure that survives compilation: a status created but never
// reported, or reported but never created and failing loop.Verify on the
// first run.
//
// existingConfig is the fixture worth keeping here: plan's on_success is
// "Implementing", which queues.code watches. A watched on_success is the one
// input two hand-written loops diverge on — statusRoles skips it as an exit
// but must still name it as a queue's own status — so a simpler pipeline
// would leave the interesting case untested.
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
	// And that set is every status the queues name, watched or exit.
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

// A first run fills .lerp with run records, logs and a worktree per
// workspace, so init makes the repository ignore it — appending to whatever
// ignore list is already there, and saying so like it says everything else.
func TestInitIgnoresStateDir(t *testing.T) {
	for _, tc := range []struct {
		name    string
		before  string // "" means no .gitignore at all
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
			// Nothing gets appended to somebody's last line.
			name:    "appends after a file with no trailing newline",
			before:  "*.out",
			want:    "*.out\n\n" + stateDirBlock,
			message: "added .lerp/ to .gitignore",
		},
		{
			// One blank line of separation, never a second.
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
			// The other spellings of the same rule at the repo root.
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

// Repeating init on a repo that already has a config still ignores the state
// directory — that is how a repo set up by an earlier lerp picks it up — and
// a second run adds nothing further.
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

// A .gitignore lerp cannot get at is reported and survived, whether it is
// the read that fails or the write: init still writes the config it exists
// to write, rather than leaving a repo whose board is set up and whose
// lerp.toml never arrived.
func TestInitSurvivesUnusableGitignore(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{
			// A directory where the file goes: the read fails first.
			name: "unreadable",
			setup: func(t *testing.T, path string) {
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			// Readable, so the failure lands on the append instead.
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
	t.Setenv("HOME", dir) // empty home -> no MCP configured
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

	// 4 fast-path answers + 'n' for MCP
	answers := strings.NewReader("\n\n\n\nn\n")
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
		"Each runner CLI needs its own Linear MCP server",
		"Alternative single-auth bridge:",
		"Register Linear MCP for unconfigured CLIs?",
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

	// 4 fast-path answers + 'y' for MCP
	answers := strings.NewReader("\n\n\n\ny\n")
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

	// 4 fast-path answers + 'b' for bridge
	answers := strings.NewReader("\n\n\n\nb\n")
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
	// Write ~/.claude.json with Linear MCP configured
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

	answers := strings.NewReader("\n\n\n\n")
	b := &fakeBoard{existing: linearDefaults}
	_, err := Init(context.Background(), b, &out, answers, dir, "LERP", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(commandsRun) != 0 {
		t.Errorf("commands run = %v, want none when already configured", commandsRun)
	}
	output := out.String()
	if strings.Contains(output, "Register Linear MCP") {
		t.Error("init asked to register MCP when already configured")
	}
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

	answers := strings.NewReader("y\n")
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

	answers := strings.NewReader("\n\n\n\ny\n")
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
	// Confirm before execute: the collision finding is printed before EnsureWorkflowStates runs
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
	// Target is "Plan Review", which stock config names
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
	answers := strings.NewReader("\n\n\ny\n")
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
	if strings.Contains(out.String(), "Create team") {
		t.Errorf("asked to create existing team:\n%s", out.String())
	}
}

func TestInitTeamGivenAndMissingTerminalConfirmsCreate(t *testing.T) {
	dir := t.TempDir()
	b := &fakeBoard{
		teams:    []linear.TeamRef{{Key: "ENG", Name: "Engineering"}},
		existing: linearDefaults,
	}
	var out bytes.Buffer
	answers := strings.NewReader("y\n\n\n\ny\n")
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
	for _, want := range []string{
		`workspace has no team "ACEM"; workspace has: ENG`,
		"Create team ACEM? [y/N]",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestInitTeamGivenAndMissingTerminalDeclinesCreate(t *testing.T) {
	dir := t.TempDir()
	b := &fakeBoard{
		teams:    []linear.TeamRef{{Key: "ENG", Name: "Engineering"}},
		existing: linearDefaults,
	}
	var out bytes.Buffer
	answers := strings.NewReader("n\n")
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
	// Pick row 2: LERP, then defaults for plan, review, map, bypass.
	answers := strings.NewReader("2\n\n\n\ny\n")
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
	for _, want := range []string{
		"  1) ENG  Engineering",
		"  2) LERP  Lerp",
		"  3) create a new team",
		"Pick a team [1]:",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
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
	// Pick row 2 (create a new team), enter key ACEM, name Acme Marketing, then plan, review, map, bypass.
	answers := strings.NewReader("2\nACEM\nAcme Marketing\n\n\n\ny\n")
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
	for _, want := range []string{
		"  2) create a new team",
		"Team key:",
		"Team name [ACEM]:",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
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
	// Pick row 2 (create), enter key ACEM, press Enter for default name from --team-name
	answers := strings.NewReader("2\nACEM\n\n\n\n\ny\n")
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
	if !strings.Contains(out.String(), "Team name [Provided Name]:") {
		t.Errorf("output missing Provided Name prompt:\n%s", out.String())
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
	// Straight to create prompts: key ACEM, name Acme, then plan, review, map, bypass.
	answers := strings.NewReader("ACEM\nAcme\n\n\n\ny\n")
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
	for _, want := range []string{
		"Team key:",
		"Team name [ACEM]:",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "Pick a team") {
		t.Errorf("output should not contain picker when workspace has no teams:\n%s", out.String())
	}
}

func TestInitTeamQuestionEOFErrorsWithoutCreating(t *testing.T) {
	dir := t.TempDir()
	b := &fakeBoard{
		teams:    []linear.TeamRef{{Key: "LERP", Name: "Lerp"}},
		existing: linearDefaults,
	}
	answers := strings.NewReader("")
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
	answers := strings.NewReader("")
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
	if strings.Contains(out.String(), "Pick a team") {
		t.Errorf("asked for team on repeat init with single configured team:\n%s", out.String())
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
	answers := strings.NewReader("2\n")
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
	for _, want := range []string{
		"  1) ENG  Engineering",
		"  2) LERP  Lerp",
		"Pick a team [1]:",
		"on team LERP:",
	} {
		if !strings.Contains(transcript, want) {
			t.Errorf("output missing %q:\n%s", want, transcript)
		}
	}
	if strings.Contains(transcript, "create a new team") {
		t.Errorf("picker offered create a new team on repeat init:\n%s", transcript)
	}
	if strings.Contains(transcript, "ACEM") {
		t.Errorf("picker offered team ACEM not in repo config:\n%s", transcript)
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
	answers := strings.NewReader("")
	created, err := Init(context.Background(), b, &out, answers, dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("created = true, want false for repeat init")
	}
	if strings.Contains(out.String(), "Pick a team") {
		t.Errorf("asked for team when only one configured team exists in workspace:\n%s", out.String())
	}
}
