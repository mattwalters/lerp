package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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

// PromoteTargets is the promote picker's option list: every queue status
// once, then whichever on_success/on_failure targets are not already one —
// the pipeline's exits.
func TestPromoteTargets(t *testing.T) {
	path := writeFile(t, "lerp.toml", validRepo)
	c, err := LoadRepoConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Implementing", "Planning", "In Review", "Blocked"}
	if got := c.PromoteTargets(); !reflect.DeepEqual(got, want) {
		t.Errorf("PromoteTargets = %v, want %v", got, want)
	}
}

// WatchedStatuses is the set some queue picks up from, and nothing else: in
// validRepo the two queue statuses, never the on_success/on_failure targets
// no queue watches. Those are the pipeline's exits, and the claim-release
// rule turns on the difference.
func TestWatchedStatuses(t *testing.T) {
	path := writeFile(t, "lerp.toml", validRepo)
	c, err := LoadRepoConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"Planning": true, "Implementing": true}
	if got := c.WatchedStatuses(); !reflect.DeepEqual(got, want) {
		t.Errorf("WatchedStatuses = %v, want %v", got, want)
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
			name: "runner without vendor or command",
			toml: `
teams = ["LERP"]
provision = "p"
dispose = "d"

[runners.mine]
resume = "mine --resume {{session}}"
` + pipeline,
			wantErr: `runner "mine": set exactly one of vendor or command`,
		},
		{
			name: "runner with both vendor and command",
			toml: `
teams = ["LERP"]
provision = "p"
dispose = "d"

[runners.mine]
vendor = "claude"
command = "claude"
` + pipeline,
			wantErr: `runner "mine": set exactly one of vendor or command`,
		},
		{
			name: "runner with unknown vendor",
			toml: `
teams = ["LERP"]
provision = "p"
dispose = "d"

[runners.mine]
vendor = "chatgpt"
` + pipeline,
			wantErr: `runner "mine": unknown vendor "chatgpt" (known: antigravity, claude, codex)`,
		},
		{
			name: "model on a command runner",
			toml: `
teams = ["LERP"]
provision = "p"
dispose = "d"

[runners.mine]
command = "mine {{prompt}}"
model = "opus"
` + pipeline,
			wantErr: `runner "mine": model is set on a command runner; it belongs to a vendor`,
		},
		{
			name: "effort on a command runner",
			toml: `
teams = ["LERP"]
provision = "p"
dispose = "d"

[runners.mine]
command = "mine {{prompt}}"
effort = "high"
` + pipeline,
			wantErr: `runner "mine": effort is set on a command runner; it belongs to a vendor`,
		},
		{
			name: "args on a command runner",
			toml: `
teams = ["LERP"]
provision = "p"
dispose = "d"

[runners.mine]
command = "mine {{prompt}}"
args = "--verbose"
` + pipeline,
			wantErr: `runner "mine": args is set on a command runner; it belongs to a vendor`,
		},
		{
			name: "resume alongside a vendor",
			toml: `
teams = ["LERP"]
provision = "p"
dispose = "d"

[runners.mine]
vendor = "claude"
resume = "claude --resume {{session}}"
` + pipeline,
			wantErr: `runner "mine": resume is set by vendor "claude"; disagree with it by using a command runner instead`,
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
			name: "prompt references on_failure without one configured",
			toml: `
teams = ["LERP"]
provision = "p"
dispose = "d"

[runners.claude]
command = "claude"

[queues.plan]
status = "Planning"
prompt = "On trouble move {{ticket}} to {{on_failure}}."
runner = "claude"
on_success = "Done"
`,
			wantErr: `queue "plan": prompt references {{on_failure}} but on_failure is not set`,
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

// Prompts follow the configured statuses: a queue's own pointers expand into
// its prose, so renaming a status in config renames it in the instructions.
func TestQueueExpandPrompt(t *testing.T) {
	q := Queue{
		Status:    "Implementing",
		Prompt:    "Work {{ticket}} in {{status}}; done goes to {{on_success}}, trouble to {{on_failure}}. Close {{ticket}}.",
		OnSuccess: "In Review",
		OnFailure: "Needs Attention",
	}
	got := q.ExpandPrompt("LERP-42")
	want := "Work LERP-42 in Implementing; done goes to In Review, trouble to Needs Attention. Close LERP-42."
	if got != want {
		t.Errorf("ExpandPrompt = %q, want %q", got, want)
	}
}

// A three-line vendor block resolves to the adapter's default command and
// resume, with nothing downstream needing a case for vendors.
func TestVendorRunnerResolves(t *testing.T) {
	path := writeFile(t, "lerp.toml", `
teams = ["LERP"]
provision = "p"
dispose = "d"

[runners.claude]
vendor = "claude"

[queues.plan]
status = "Planning"
prompt = "p"
runner = "claude"
on_success = "Done"
`)
	c, err := LoadRepoConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	r := c.Runners["claude"]
	if want := "claude -p {{prompt}} --session-id {{session}} --output-format stream-json --verbose"; r.Command != want {
		t.Errorf("Command = %q, want %q", r.Command, want)
	}
	if want := "cd {{workdir}} && claude --resume {{session}}"; r.Resume != want {
		t.Errorf("Resume = %q, want %q", r.Resume, want)
	}
}

func TestAntigravityVendorRunnerResolves(t *testing.T) {
	path := writeFile(t, "lerp.toml", `
teams = ["LERP"]
provision = "p"
dispose = "d"

[runners.agy]
vendor = "antigravity"

[queues.plan]
status = "Planning"
prompt = "p"
runner = "agy"
on_success = "Done"
`)
	c, err := LoadRepoConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	r := c.Runners["agy"]
	if want := "agy -p {{prompt}} --output-format stream-json --add-dir {{workdir}} --print-timeout 24h"; r.Command != want {
		t.Errorf("Command = %q, want %q", r.Command, want)
	}
	if want := "cd {{workdir}} && agy --conversation {{session}}"; r.Resume != want {
		t.Errorf("Resume = %q, want %q", r.Resume, want)
	}
}

// Model, Effort and Args each reach the resolved command, singly and
// together — the override precedence a vendor block's whole point rests on.
func TestVendorRunnerOverridePrecedence(t *testing.T) {
	tests := []struct {
		name  string
		extra string
		want  string
	}{
		{
			name:  "model",
			extra: `model = "opus"`,
			want:  "claude -p {{prompt}} --session-id {{session}} --output-format stream-json --verbose --model 'opus'",
		},
		{
			name:  "effort",
			extra: `effort = "high"`,
			want:  "claude -p {{prompt}} --session-id {{session}} --output-format stream-json --verbose --effort 'high'",
		},
		{
			name:  "args",
			extra: `args = "--permission-mode bypassPermissions"`,
			want:  "claude -p {{prompt}} --session-id {{session}} --output-format stream-json --verbose --permission-mode bypassPermissions",
		},
		{
			name: "all three",
			extra: "model = \"opus\"\n" +
				"effort = \"high\"\n" +
				`args = "--permission-mode bypassPermissions"`,
			want: "claude -p {{prompt}} --session-id {{session}} --output-format stream-json --verbose" +
				" --model 'opus' --effort 'high' --permission-mode bypassPermissions",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeFile(t, "lerp.toml", `
teams = ["LERP"]
provision = "p"
dispose = "d"

[runners.claude]
vendor = "claude"
`+tt.extra+`

[queues.plan]
status = "Planning"
prompt = "p"
runner = "claude"
on_success = "Done"
`)
			c, err := LoadRepoConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := c.Runners["claude"].Command; got != tt.want {
				t.Errorf("Command = %q, want %q", got, tt.want)
			}
		})
	}
}

// A codex vendor block resolves the same way a claude one does, through the
// same code path — the point of the adapter mechanism.
func TestCodexVendorRunnerResolves(t *testing.T) {
	path := writeFile(t, "lerp.toml", `
teams = ["LERP"]
provision = "p"
dispose = "d"

[runners.codex]
vendor = "codex"

[queues.plan]
status = "Planning"
prompt = "p"
runner = "codex"
on_success = "Done"
`)
	c, err := LoadRepoConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	r := c.Runners["codex"]
	if want := "codex exec --json -- {{prompt}}"; r.Command != want {
		t.Errorf("Command = %q, want %q", r.Command, want)
	}
	if want := "cd {{workdir}} && codex resume {{session}}"; r.Resume != want {
		t.Errorf("Resume = %q, want %q", r.Resume, want)
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

// Every shape the init conversation can choose must render to a config the
// loader accepts, with no template plumbing left in the text.
func TestStockVariants(t *testing.T) {
	for name, tc := range map[string]struct {
		stock            Stock
		queues           []string
		implementSuccess string
		reviewPass       bool // the implement prompt reviews its own work
	}{
		"full": {
			stock:            Stock{Teams: []string{"LERP"}, Plan: true, Review: true},
			queues:           []string{"implement", "plan"},
			implementSuccess: StockExitStatus,
			reviewPass:       true,
		},
		"no review": {
			stock:            Stock{Teams: []string{"LERP"}, Plan: true},
			queues:           []string{"implement", "plan"},
			implementSuccess: StockExitStatus,
		},
		"no planning": {
			stock:            Stock{Teams: []string{"LERP"}, Review: true},
			queues:           []string{"implement"},
			implementSuccess: StockExitStatus,
			reviewPass:       true,
		},
		"implement only": {
			stock:            Stock{Teams: []string{"LERP"}},
			queues:           []string{"implement"},
			implementSuccess: StockExitStatus,
		},
		"mapped onto an existing board": {
			stock: Stock{
				Teams: []string{"LERP"}, Plan: true, Review: true,
				PlanStatus: "Spec", PlanReviewStatus: "Spec Approval",
				ImplementStatus: "Todo",
				ExitStatus:      "Ready to Merge", AttentionStatus: "Stuck",
			},
			queues:           []string{"implement", "plan"},
			implementSuccess: "Ready to Merge",
			reviewPass:       true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			rendered := tc.stock.Render()
			for _, leftover := range []string{"#{{", "_status}}", "{{teams}}"} {
				if strings.Contains(rendered, leftover) {
					t.Errorf("rendered config leaks template plumbing %q", leftover)
				}
			}
			if !strings.Contains(rendered, "# Check this file in") {
				t.Error("rendered config lost its explanatory comments")
			}
			c, err := ParseRepoConfig(rendered, "stock")
			if err != nil {
				t.Fatal(err)
			}
			var queues []string
			for q := range c.Queues {
				queues = append(queues, q)
			}
			sort.Strings(queues)
			if !reflect.DeepEqual(queues, tc.queues) {
				t.Errorf("queues = %v, want %v", queues, tc.queues)
			}
			if got := c.Queues["implement"].OnSuccess; got != tc.implementSuccess {
				t.Errorf("implement.on_success = %q, want %q", got, tc.implementSuccess)
			}
			if tc.stock.ImplementStatus != "" {
				if got := c.Queues["implement"].Status; got != tc.stock.ImplementStatus {
					t.Errorf("implement.status = %q, want %q", got, tc.stock.ImplementStatus)
				}
			}
			// Declining the review pass is the only thing that distinguishes
			// two otherwise identical renderings, and it shows up as prose
			// inside one prompt rather than as a queue of its own.
			// "three rounds" is the cap that paragraph puts on the fix loop;
			// "how the review went" is what the verdict comment owes the board
			// once a review has happened, and a pipeline without the pass must
			// not ask an agent to report rounds it never ran.
			for _, prose := range []string{"three rounds", "how the review went"} {
				if got := strings.Contains(c.Queues["implement"].Prompt, prose); got != tc.reviewPass {
					t.Errorf("implement prompt contains %q = %v, want %v", prose, got, tc.reviewPass)
				}
			}
			// A finished plan lands on the approval gate, never straight in
			// implement: the planning stage is worth running only if a human
			// reads the plan, and an unserved status is what makes it wait.
			if plan, ok := c.Queues["plan"]; ok {
				gate := orStock(tc.stock.PlanReviewStatus, StockPlanReviewStatus)
				if plan.OnSuccess != gate {
					t.Errorf("plan.on_success = %q, want the approval gate %q", plan.OnSuccess, gate)
				}
				for name, q := range c.Queues {
					if q.Status == gate {
						t.Errorf("queue %q watches the approval gate %q: the plan never waits for a human", name, gate)
					}
				}
			}
		})
	}
}

// The shipped example is exactly what ExampleRepoConfig renders, byte for
// byte, and this is what says so. Comparing the parsed structs would pin
// only the decoded fields and leave the ~90 lines of operator-facing
// commentary — the PERMISSIONS warning, the plan-gate explanation, the "no
// review queue" rationale — free to drift: revise a paragraph in stock.toml,
// tests stay green, and the file a new operator reads first carries stale
// guidance. Bytes cover both, since equal text parses equal.
//
// The repo-root lerp.toml is deliberately not pinned: it edits the stock
// pipeline for this repo, and pinning it would be pinning a fork.
func TestStockMatchesExample(t *testing.T) {
	example, err := os.ReadFile(filepath.Join("..", "..", "lerp.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if diff := firstDiff(ExampleRepoConfig(), string(example)); diff != "" {
		// Which side is wrong depends on which one someone just edited, so
		// name both directions: stock.toml is the source, but the drift this
		// test exists to catch has arrived as an edit to the example too.
		t.Errorf("lerp.example.toml is not what ExampleRepoConfig renders:\n%s\n\n"+
			"internal/config/stock.toml is the source. Make the change there —\n"+
			"including any prose meant only for the example — then regenerate:\n"+
			"    make example", diff)
	}
}

// firstDiff describes where two texts first differ, as the line number and
// the two lines, or "" when they are identical. A 200-line file needs a
// failure that names the line, not one that reports two byte counts.
func firstDiff(got, want string) string {
	if got == want {
		return ""
	}
	// A file's final newline makes Split yield a trailing "", so line counts
	// here are counted, not len()'d — an off-by-one in a failure message
	// sends a reader to the wrong line.
	gotLines, wantLines := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(gotLines) && i < len(wantLines); i++ {
		if gotLines[i] != wantLines[i] {
			return fmt.Sprintf("first difference at line %d:\n  rendered: %q\n  example:  %q", i+1, gotLines[i], wantLines[i])
		}
	}
	// One is a prefix of the other. The likeliest way that happens is a lost
	// trailing newline, which prints as an empty extra line and reads as
	// nothing at all unless it is named.
	long, short, which := gotLines, wantLines, "rendered"
	if len(wantLines) > len(gotLines) {
		long, short, which = wantLines, gotLines, "example"
	}
	// ...but only when the shorter text does not already end in one, or an
	// added blank line at EOF would be reported as a newline that is there.
	shortText := want
	if which == "example" {
		shortText = got
	}
	if len(long) == len(short)+1 && long[len(short)] == "" && !strings.HasSuffix(shortText, "\n") {
		return fmt.Sprintf("identical except the trailing newline: %s has one, the other does not", which)
	}
	// A newline-terminated text's last element is the phantom after it, not a
	// line, so it is not counted — the number has to be one a reader can go to.
	lines := len(short)
	if strings.HasSuffix(shortText, "\n") {
		lines--
	}
	return fmt.Sprintf("identical for %d lines, then %s continues: %q",
		lines, which, long[len(short)])
}

// Declining the permission grant drops the whole #{{bypass}} section instead
// of string-replacing the flag out of the rendered document — so the only
// place the grant can appear as real config is inside that section, and
// nowhere else can declining reach in and edit text nobody meant it to
// touch. Comment lines (prose, and the commented-out command = example) are
// exempt: the PERMISSIONS paragraph is deliberately the one place the flag
// survives declining, so an operator knows how to widen later.
func TestBypassFlagAppearsOnlyInRunnerCommands(t *testing.T) {
	const flag = "--permission-mode bypassPermissions"
	inBypass, found := false, false
	for i, line := range strings.Split(stockRepo, "\n") {
		switch strings.TrimSpace(line) {
		case "#{{bypass}}":
			inBypass = true
			continue
		case "#{{/bypass}}":
			inBypass = false
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue // prose and the commented-out escape-hatch example
		}
		if !strings.Contains(line, flag) {
			continue
		}
		found = true
		if !inBypass || !strings.HasPrefix(trimmed, "args = ") {
			t.Errorf("stock.toml line %d has %q outside the #{{bypass}} section's args line, so declining the grant would not remove it:\n  %s",
				i+1, flag, line)
		}
	}
	if !found {
		t.Error("stock.toml never sets the bypass grant")
	}

	declined := Stock{Teams: []string{"LERP"}}.Render()
	c, err := ParseRepoConfig(declined, "declined")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(c.Runners["claude"].Command, flag) {
		t.Errorf("declining the grant left %q in the resolved command %q", flag, c.Runners["claude"].Command)
	}
	if !strings.Contains(declined, "PERMISSIONS:") {
		t.Error("declining the grant lost the PERMISSIONS paragraph")
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

	// Prompts reference statuses through their queue's placeholders, never by
	// name: a literal status in the prose would survive a rename and send
	// agents to a status that no longer exists.
	names := map[string]bool{}
	for _, q := range c.Queues {
		for _, s := range []string{q.Status, q.OnSuccess, q.OnFailure} {
			if s != "" {
				names[s] = true
			}
		}
	}
	for name, q := range c.Queues {
		for status := range names {
			if strings.Contains(q.Prompt, status) {
				t.Errorf("queue %q prompt hardcodes status %q", name, status)
			}
		}
	}

	// The chain: planning hands off to a status no queue watches — the
	// approval gate, where a human reads the plan and promotes it — and
	// implementing hands off to a status no queue watches again, where a
	// human merges. Two queues, two gates, and no cycle: every hop on this
	// board is a decision somebody makes.
	plan, ok := byStatus["Planning"]
	if !ok {
		t.Fatal("no queue watches Planning")
	}
	if _, watched := byStatus[plan.OnSuccess]; watched {
		t.Errorf("Planning hands off to %q, which a queue picks up: the plan is implemented before anyone reads it", plan.OnSuccess)
	}
	implement, ok := byStatus["Implementing"]
	if !ok {
		t.Fatal("no queue watches Implementing")
	}
	if _, watched := byStatus[implement.OnSuccess]; watched {
		t.Errorf("Implementing hands off to %q, which a queue picks up: work never reaches a human", implement.OnSuccess)
	}
}
