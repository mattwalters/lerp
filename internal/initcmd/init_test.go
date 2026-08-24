package initcmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/linear"
)

type fakeBoard struct {
	teamKey, teamName string
	// existing plays the statuses the team already has, in board order.
	existing []string
	states   []linear.StateSpec
	// categories plays Linear's category for existing states, name →
	// category; existing states it does not name are "unstarted". Requested
	// states not on the board come back as created, in their requested
	// category — the contract of the real EnsureWorkflowStates.
	categories map[string]string
	err        error
}

func (b *fakeBoard) EnsureTeam(_ context.Context, key, name string) error {
	b.teamKey, b.teamName = key, name
	return b.err
}

func (b *fakeBoard) TeamStates(_ context.Context, _ string) ([]string, error) {
	if b.err != nil {
		return nil, b.err
	}
	return b.existing, nil
}

func (b *fakeBoard) EnsureWorkflowStates(_ context.Context, _ string, states []linear.StateSpec) (map[string]string, error) {
	b.states = append([]linear.StateSpec(nil), states...)
	if b.err != nil {
		return nil, b.err
	}
	categories := map[string]string{}
	for _, name := range b.existing {
		categories[name] = "unstarted"
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
var linearDefaults = []string{"Backlog", "Todo", "In Progress", "Done", "Canceled"}

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
	b := &fakeBoard{existing: linearDefaults}
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
	if b.teamKey != "LERP" || b.teamName != "Lerp" {
		t.Errorf("EnsureTeam = (%q, %q)", b.teamKey, b.teamName)
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
		"team LERP has: Backlog, Todo, In Progress, Done, Canceled",
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
	existing := []string{"Backlog", "Todo", "In Progress", "In Review", "Done"}
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
		`implement runs in:  1) Backlog  2) Todo `,
		`c) create "Implementing"`,
		`plans wait for approval in:  1) Backlog  2) Todo `,
		`c) create "Plan Review"`,
		// The stock exit already exists, so its pick defaults to that status
		// and offers nothing to create.
		`finished work exits to:  1) Backlog  2) Todo  3) In Progress  4) In Review  5) Done  [4]`,
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
	b := &fakeBoard{existing: []string{"Planning", "Implementing"}}
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

// A mapping that folds two queues onto one status is caught by the config
// loader before anything is written.
func TestInitRejectsMappingTwoQueuesOntoOneStatus(t *testing.T) {
	dir := t.TempDir()
	// Customize: plan → 2) Todo, plan review → create, implement → 2) Todo,
	// then defaults.
	answers := strings.NewReader("\n\nc\n2\nc\n2\nc\nc\nc\nn\n")
	_, err := Init(context.Background(), &fakeBoard{existing: linearDefaults}, io.Discard, answers, dir, "LERP", "")
	if err == nil || !strings.Contains(err.Error(), `both watch status "Todo"`) {
		t.Fatalf("Init error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, config.RepoConfigFile)); !os.IsNotExist(statErr) {
		t.Fatalf("config was created: %v", statErr)
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
