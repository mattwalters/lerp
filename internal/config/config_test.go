package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeFile writes contents to a temp file and returns its path.
func writeFile(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const validGlobal = `
lanes = 3

[runners.claude]
command = "claude -p {{prompt}} --cwd {{workdir}}"
resume = "claude --resume {{session}}"

[runners.codex]
command = "codex exec {{prompt}}"

[queues.plan]
status = "Planning"
prompt = "Write a plan."
runner = "claude"
on_success = "Implementing"

[queues.implement]
status = "Implementing"
prompt = "Implement the plan."
runner = "codex"
on_success = "In Review"
on_failure = "Blocked"
`

func TestLoadGlobal(t *testing.T) {
	path := writeFile(t, "config.toml", validGlobal)
	g, err := LoadGlobal(path)
	if err != nil {
		t.Fatal(err)
	}
	if g.Lanes != 3 {
		t.Errorf("Lanes = %d, want 3", g.Lanes)
	}
	if got := g.Runners["claude"].Resume; got != "claude --resume {{session}}" {
		t.Errorf("Runners[claude].Resume = %q", got)
	}
	if got := g.Runners["codex"].Resume; got != "" {
		t.Errorf("Runners[codex].Resume = %q, want empty", got)
	}
	q := g.Queues["implement"]
	if q.Status != "Implementing" || q.Runner != "codex" ||
		q.OnSuccess != "In Review" || q.OnFailure != "Blocked" {
		t.Errorf("Queues[implement] = %+v", q)
	}
	if got := g.Queues["plan"].OnFailure; got != "" {
		t.Errorf("Queues[plan].OnFailure = %q, want empty", got)
	}
}

func TestLoadGlobalDefaultLanes(t *testing.T) {
	path := writeFile(t, "config.toml", `
[runners.claude]
command = "claude -p {{prompt}}"

[queues.plan]
status = "Planning"
prompt = "Write a plan."
runner = "claude"
on_success = "Implementing"
`)
	g, err := LoadGlobal(path)
	if err != nil {
		t.Fatal(err)
	}
	if g.Lanes != 5 {
		t.Errorf("Lanes = %d, want default 5", g.Lanes)
	}
}

func TestLoadOrCreateGlobal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lerp", "config.toml")
	g, created, err := LoadOrCreateGlobal(path)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("created = false, want true")
	}
	if g.Runners["claude"].Command == "" || len(g.Queues) == 0 {
		t.Errorf("stock global = %+v, want Claude runner and queues", g)
	}

	contents := "[runners.mine]\ncommand = \"mine\"\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	_, created, err = LoadOrCreateGlobal(path)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("created = true, want false for existing config")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != contents {
		t.Errorf("existing config changed to %q", got)
	}
}

func TestStockGlobalMatchesExample(t *testing.T) {
	stockPath := filepath.Join(t.TempDir(), "config.toml")
	if _, err := WriteStockGlobal(stockPath); err != nil {
		t.Fatal(err)
	}
	stock, err := LoadGlobal(stockPath)
	if err != nil {
		t.Fatal(err)
	}
	example, err := LoadGlobal(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stock, example) {
		t.Error("embedded stock config differs from config.example.toml")
	}
}

func TestLoadGlobalErrors(t *testing.T) {
	tests := []struct {
		name    string
		toml    string
		wantErr string
	}{
		{
			name:    "malformed toml",
			toml:    "lanes = ",
			wantErr: "toml",
		},
		{
			name:    "unknown top-level key",
			toml:    "lanez = 5",
			wantErr: `unknown key(s): lanez`,
		},
		{
			name: "unknown runner key",
			toml: `
[runners.claude]
command = "claude"
timeout = 30
`,
			wantErr: "unknown key(s): runners.claude.timeout",
		},
		{
			name: "unknown queue key",
			toml: `
[runners.claude]
command = "claude"

[queues.plan]
status = "Planning"
prompt = "p"
runner = "claude"
on_success = "Done"
when = "always"
`,
			wantErr: "unknown key(s): queues.plan.when",
		},
		{
			name:    "zero lanes",
			toml:    "lanes = 0",
			wantErr: "lanes must be at least 1, got 0",
		},
		{
			name:    "negative lanes",
			toml:    "lanes = -2",
			wantErr: "lanes must be at least 1, got -2",
		},
		{
			name: "runner without command",
			toml: `
[runners.claude]
resume = "claude --resume {{session}}"
`,
			wantErr: `runner "claude": command must not be empty`,
		},
		{
			name: "queue without status",
			toml: `
[runners.claude]
command = "claude"

[queues.plan]
prompt = "p"
runner = "claude"
on_success = "Done"
`,
			wantErr: `queue "plan": status must not be empty`,
		},
		{
			name: "queue without prompt",
			toml: `
[runners.claude]
command = "claude"

[queues.plan]
status = "Planning"
runner = "claude"
on_success = "Done"
`,
			wantErr: `queue "plan": prompt must not be empty`,
		},
		{
			name: "queue without runner",
			toml: `
[runners.claude]
command = "claude"

[queues.plan]
status = "Planning"
prompt = "p"
on_success = "Done"
`,
			wantErr: `queue "plan": runner must not be empty`,
		},
		{
			name: "queue without on_success",
			toml: `
[runners.claude]
command = "claude"

[queues.plan]
status = "Planning"
prompt = "p"
runner = "claude"
`,
			wantErr: `queue "plan": on_success must not be empty`,
		},
		{
			name: "queue names undefined runner",
			toml: `
[runners.claude]
command = "claude"

[queues.plan]
status = "Planning"
prompt = "p"
runner = "codex"
on_success = "Done"
`,
			wantErr: `queue "plan": runner "codex" is not defined under [runners]`,
		},
		{
			name: "two queues share a status",
			toml: `
[runners.claude]
command = "claude"

[queues.plan]
status = "Planning"
prompt = "p"
runner = "claude"
on_success = "Done"

[queues.replan]
status = "Planning"
prompt = "p2"
runner = "claude"
on_success = "Done"
`,
			wantErr: `queues "plan" and "replan" both watch status "Planning"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeFile(t, "config.toml", tt.toml)
			_, err := LoadGlobal(path)
			if err == nil {
				t.Fatal("want error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err, tt.wantErr)
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error %q does not carry the file path %q", err, path)
			}
		})
	}
}

func TestLoadGlobalMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	_, err := LoadGlobal(path)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not carry the file path %q", err, path)
	}
}

func TestLoadRepoConfig(t *testing.T) {
	path := writeFile(t, "lerp.toml", `
teams = ["LERP", "OPS"]
provision = "git worktree add {{workdir}}"
dispose = "git worktree remove {{workdir}}"
`)
	c, err := LoadRepoConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Teams) != 2 || c.Teams[0] != "LERP" || c.Teams[1] != "OPS" {
		t.Errorf("Teams = %v", c.Teams)
	}
	if c.Provision == "" || c.Dispose == "" {
		t.Errorf("Provision = %q, Dispose = %q", c.Provision, c.Dispose)
	}
}

func TestLoadRepoConfigErrors(t *testing.T) {
	tests := []struct {
		name    string
		toml    string
		wantErr string
	}{
		{
			name:    "malformed toml",
			toml:    "teams = [",
			wantErr: "toml",
		},
		{
			name: "unknown key",
			toml: `
teams = ["LERP"]
provision = "p"
dispose = "d"
team_lead = "matt"
`,
			wantErr: "unknown key(s): team_lead",
		},
		{
			name: "missing teams",
			toml: `
provision = "p"
dispose = "d"
`,
			wantErr: "teams must list at least one Linear team key",
		},
		{
			name: "empty teams",
			toml: `
teams = []
provision = "p"
dispose = "d"
`,
			wantErr: "teams must list at least one Linear team key",
		},
		{
			name: "empty team key",
			toml: `
teams = ["LERP", ""]
provision = "p"
dispose = "d"
`,
			wantErr: "teams must not contain an empty team key",
		},
		{
			name: "duplicate team",
			toml: `
teams = ["LERP", "OPS", "LERP"]
provision = "p"
dispose = "d"
`,
			wantErr: `team "LERP" is listed more than once`,
		},
		{
			name: "missing provision",
			toml: `
teams = ["LERP"]
dispose = "d"
`,
			wantErr: "provision must not be empty",
		},
		{
			name: "missing dispose",
			toml: `
teams = ["LERP"]
provision = "p"
`,
			wantErr: "dispose must not be empty",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeFile(t, "lerp.toml", tt.toml)
			_, err := LoadRepoConfig(path)
			if err == nil {
				t.Fatal("want error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err, tt.wantErr)
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error %q does not carry the file path %q", err, path)
			}
		})
	}
}

func TestGlobalPath(t *testing.T) {
	t.Run("respects XDG_CONFIG_HOME", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "/xdg")
		got, err := GlobalPath()
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join("/xdg", "lerp", "config.toml"); got != want {
			t.Errorf("GlobalPath() = %q, want %q", got, want)
		}
	})
	t.Run("falls back to ~/.config", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", "/home/matt")
		got, err := GlobalPath()
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join("/home/matt", ".config", "lerp", "config.toml"); got != want {
			t.Errorf("GlobalPath() = %q, want %q", got, want)
		}
	})
}

// The shipped example is what a new operator copies into place, so it has to
// load and validate like any other config — and its topology has to actually
// connect.
func TestExampleConfigIsUsable(t *testing.T) {
	path := filepath.Join("..", "..", "config.example.toml")
	g, err := LoadGlobal(path)
	if err != nil {
		t.Fatal(err)
	}
	if g.Lanes < 1 {
		t.Errorf("lanes = %d, want a positive default", g.Lanes)
	}

	byStatus := map[string]Queue{}
	for _, q := range g.Queues {
		byStatus[q.Status] = q
	}
	for name, q := range g.Queues {
		// A prompt that never names its ticket reaches every agent as the same
		// anonymous instruction, and lerp would still advance the ticket.
		if !strings.Contains(q.Prompt, "{{ticket}}") {
			t.Errorf("queue %q prompt does not name {{ticket}}", name)
		}
		if _, ok := g.Runners[q.Runner]; !ok {
			t.Errorf("queue %q names undefined runner %q", name, q.Runner)
		}
		// Failures must land somewhere no queue watches, or a ticket that fails
		// every time is retried forever.
		if _, watched := byStatus[q.OnFailure]; q.OnFailure != "" && watched {
			t.Errorf("queue %q routes failures to %q, which another queue picks up", name, q.OnFailure)
		}
	}

	// The chain: planning hands off to implementing, which hands off to review,
	// which hands off to a status no queue watches — a human.
	plan, ok := byStatus["Planning"]
	if !ok {
		t.Fatal("no queue watches Planning")
	}
	implement, ok := byStatus[plan.OnSuccess]
	if !ok {
		t.Fatalf("Planning hands off to %q, which no queue watches", plan.OnSuccess)
	}
	review, ok := byStatus[implement.OnSuccess]
	if !ok {
		t.Fatalf("Implementing hands off to %q, which no queue watches", implement.OnSuccess)
	}
	if _, watched := byStatus[review.OnSuccess]; watched {
		t.Errorf("review hands off to %q, which a queue picks up: work never reaches a human", review.OnSuccess)
	}
}
