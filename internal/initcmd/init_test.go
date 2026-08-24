package initcmd

import (
	"bytes"
	"context"
	"errors"
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
	states            []linear.StateSpec
	// categories plays the states Linear already has, name → category.
	// Requested states it does not name come back as created, in their
	// requested category — the contract of the real EnsureWorkflowStates.
	categories map[string]string
	err        error
}

func (b *fakeBoard) EnsureTeam(_ context.Context, key, name string) error {
	b.teamKey, b.teamName = key, name
	return b.err
}
func (b *fakeBoard) EnsureWorkflowStates(_ context.Context, _ string, states []linear.StateSpec) (map[string]string, error) {
	b.states = append([]linear.StateSpec(nil), states...)
	if b.err != nil {
		return nil, b.err
	}
	categories := map[string]string{}
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
	b := &fakeBoard{}
	created, err := Init(context.Background(), b, nil, dir, "LERP", "Lerp", func() bool { return true })
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
		{Name: "Agent Review", Type: "started"},
		{Name: "Implementing", Type: "started"},
		{Name: "In Review", Type: "started"},
		{Name: "Needs Attention", Type: "started"},
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
}

func TestInitWithoutBypassGrant(t *testing.T) {
	for name, confirm := range map[string]func() bool{
		"declined": func() bool { return false },
		"nil":      nil,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := Init(context.Background(), &fakeBoard{}, nil, dir, "LERP", "", confirm); err != nil {
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
	b := &fakeBoard{}
	asked := false
	created, err := Init(context.Background(), b, nil, dir, "LERP", "", func() bool { asked = true; return true })
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("created = true, want false for existing config")
	}
	if asked {
		t.Error("confirmBypass consulted although no config was written")
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
	_, err := Init(context.Background(), &fakeBoard{}, nil, dir, "LERP", "", nil)
	if err == nil || !strings.Contains(err.Error(), `does not serve team "LERP"`) {
		t.Fatalf("Init error = %v", err)
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
			if _, err := Init(context.Background(), &fakeBoard{categories: tc.categories}, &out, dir, "LERP", "", nil); err != nil {
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
	_, err := Init(context.Background(), &fakeBoard{err: errors.New("no access")}, nil, dir, "LERP", "", nil)
	if err == nil || !strings.Contains(err.Error(), "no access") {
		t.Fatalf("Init error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, config.RepoConfigFile)); !os.IsNotExist(statErr) {
		t.Fatalf("config was created: %v", statErr)
	}
}
