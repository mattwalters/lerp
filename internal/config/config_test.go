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

const validRepo = `
teams = ["LERP", "OPS"]
provision = "git worktree add {{workdir}}"
dispose = "git worktree remove {{workdir}}"

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

func TestLoadRepoConfig(t *testing.T) {
	path := writeFile(t, "lerp.toml", validRepo)
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
	if got := c.Runners["claude"].Resume; got != "claude --resume {{session}}" {
		t.Errorf("Runners[claude].Resume = %q", got)
	}
	if got := c.Runners["codex"].Resume; got != "" {
		t.Errorf("Runners[codex].Resume = %q, want empty", got)
	}
	q := c.Queues["implement"]
	if q.Status != "Implementing" || q.Runner != "codex" ||
		q.OnSuccess != "In Review" || q.OnFailure != "Blocked" {
		t.Errorf("Queues[implement] = %+v", q)
	}
	if got := c.Queues["plan"].OnFailure; got != "" {
		t.Errorf("Queues[plan].OnFailure = %q, want empty", got)
	}
}

func TestLoadRepoConfigErrors(t *testing.T) {
	// A minimal valid pipeline appended to team/workspace fragments, so each
	// case isolates the error it is about.
	const pipeline = `
[runners.claude]
command = "claude"

[queues.plan]
status = "Planning"
prompt = "Plan {{ticket}}."
runner = "claude"
on_success = "Done"
`
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
` + pipeline,
			wantErr: "unknown key(s): team_lead",
		},
		{
			name: "missing teams",
			toml: `
provision = "p"
dispose = "d"
` + pipeline,
			wantErr: "teams must list at least one Linear team key",
		},
		{
			name: "empty teams",
			toml: `
teams = []
provision = "p"
dispose = "d"
` + pipeline,
			wantErr: "teams must list at least one Linear team key",
		},
		{
			name: "empty team key",
			toml: `
teams = ["LERP", ""]
provision = "p"
dispose = "d"
` + pipeline,
			wantErr: "teams must not contain an empty team key",
		},
		{
			name: "duplicate team",
			toml: `
teams = ["LERP", "OPS", "LERP"]
provision = "p"
dispose = "d"
` + pipeline,
			wantErr: `team "LERP" is listed more than once`,
		},
		{
			name: "missing provision",
			toml: `
teams = ["LERP"]
dispose = "d"
` + pipeline,
			wantErr: "provision must not be empty",
		},
		{
			name: "missing dispose",
			toml: `
teams = ["LERP"]
provision = "p"
` + pipeline,
			wantErr: "dispose must not be empty",
		},
		{
			name: "no queues",
			toml: `
teams = ["LERP"]
provision = "p"
dispose = "d"
`,
			wantErr: "at least one queue is required",
		},
		{
			name: "unknown runner key",
			toml: `
teams = ["LERP"]
provision = "p"
dispose = "d"

[runners.claude]
command = "claude"
timeout = 30

[queues.plan]
status = "Planning"
prompt = "p"
runner = "claude"
on_success = "Done"
`,
			wantErr: "unknown key(s): runners.claude.timeout",
		},
		{
			name: "unknown queue key",
			toml: `
teams = ["LERP"]
provision = "p"
dispose = "d"

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
			name: "runner without command",
			toml: `
teams = ["LERP"]
provision = "p"
dispose = "d"

[runners.mine]
resume = "mine --resume {{session}}"
` + pipeline,
			wantErr: `runner "mine": command must not be empty`,
		},
		{
			name: "queue without status",
			toml: `
teams = ["LERP"]
provision = "p"
dispose = "d"

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
teams = ["LERP"]
provision = "p"
dispose = "d"

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
teams = ["LERP"]
provision = "p"
dispose = "d"

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
teams = ["LERP"]
provision = "p"
dispose = "d"

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
teams = ["LERP"]
provision = "p"
dispose = "d"

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
teams = ["LERP"]
provision = "p"
dispose = "d"

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

func TestLoadRepoConfigMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lerp.toml")
	_, err := LoadRepoConfig(path)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not carry the file path %q", err, path)
	}
}

func TestStockRepoConfig(t *testing.T) {
	c, err := ParseRepoConfig(StockRepoConfig([]string{"LERP", "OPS"}, true), "stock")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Teams, []string{"LERP", "OPS"}) {
		t.Errorf("Teams = %v", c.Teams)
	}
	if c.Runners["claude"].Command == "" || len(c.Queues) == 0 {
		t.Errorf("stock config = %+v, want Claude runner and queues", c)
	}
	if !strings.Contains(c.Runners["claude"].Command, "--permission-mode bypassPermissions") {
		t.Errorf("accepted grant missing from command %q", c.Runners["claude"].Command)
	}
}

// Declining the grant must scrub it from every command — the comments still
// name the flag, deliberately, so the operator knows how to widen later.
func TestStockRepoConfigWithoutBypass(t *testing.T) {
	c, err := ParseRepoConfig(StockRepoConfig([]string{"LERP"}, false), "stock")
	if err != nil {
		t.Fatal(err)
	}
	for name, r := range c.Runners {
		if strings.Contains(r.Command, "bypassPermissions") {
			t.Errorf("runner %q kept the declined grant: %q", name, r.Command)
		}
	}
	if got := c.Runners["claude"].Command; !strings.Contains(got, "claude -p") {
		t.Errorf("command = %q, want a claude invocation", got)
	}
}

func TestStockMatchesExample(t *testing.T) {
	stock, err := ParseRepoConfig(StockRepoConfig([]string{"LERP"}, true), "stock")
	if err != nil {
		t.Fatal(err)
	}
	example, err := LoadRepoConfig(filepath.Join("..", "..", "lerp.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stock, example) {
		t.Error("embedded stock config differs from lerp.example.toml")
	}
}

// The shipped example is what a new operator reads first, so it has to load
// and validate like any other config — and its topology has to actually
// connect.
func TestExampleConfigIsUsable(t *testing.T) {
	path := filepath.Join("..", "..", "lerp.example.toml")
	c, err := LoadRepoConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	byStatus := map[string]Queue{}
	for _, q := range c.Queues {
		byStatus[q.Status] = q
	}
	for name, q := range c.Queues {
		// A prompt that never names its ticket reaches every agent as the same
		// anonymous instruction, and lerp would still advance the ticket.
		if !strings.Contains(q.Prompt, "{{ticket}}") {
			t.Errorf("queue %q prompt does not name {{ticket}}", name)
		}
		if _, ok := c.Runners[q.Runner]; !ok {
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
