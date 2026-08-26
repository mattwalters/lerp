package config

import (
	_ "embed"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// RepoConfigFile is the name of the per-repo config file, found at the
// repo root.
const RepoConfigFile = "lerp.toml"

// bypassFlag is the permission grant the stock Claude runner carries when the
// operator accepts it at init. It is stripped verbatim when they decline.
const bypassFlag = " --permission-mode bypassPermissions"

// stockRepo is the template for the first-run lerp.toml written by lerp
// init, rendered by Stock.Render. Keep it in the config package so the
// binary, rather than a source checkout, owns the template it installs.
//
//go:embed stock.toml
var stockRepo string

// Stock status names: what the pipeline maps onto when the operator does
// not choose otherwise.
const (
	StockPlanStatus       = "Planning"
	StockPlanReviewStatus = "Plan Review"
	StockImplementStatus  = "Implementing"
	StockExitStatus       = "In Review"
	StockAttentionStatus  = "Needs Attention"
)

// Stock describes one rendering of the stock lerp.toml: which optional
// parts it includes, the statuses the pipeline maps onto, and whether the
// stock runner keeps its bypassPermissions grant. The implement queue is
// always present. Empty status fields take the Stock* names; lerp init
// fills them from the operator's answers.
//
// Review is not a queue. Reviewing is iteration, and iteration is not a
// decision: a review stage of its own turns review-and-fix into a cycle on
// the board that nothing can bound, since counting the rounds would be state
// outside Linear or an `if` about the process. So the review pass lives
// inside the implement prompt, where the round count is the agent's own
// context — and declining it drops those paragraphs, nothing else.
type Stock struct {
	Teams  []string
	Bypass bool
	Plan   bool // include the plan queue
	Review bool // include the review pass in the implement prompt

	PlanStatus       string
	PlanReviewStatus string // where a finished plan waits for a human to approve it
	ImplementStatus  string
	ExitStatus       string // where finished work leaves the automated path
	AttentionStatus  string // where failures wait for a human
}

// RepoConfig is the whole configuration, one checked-in file per repo: the
// Linear teams this repo serves, the commands that provision and dispose lane
// workspaces (SCOPE invariants 2 and 9), and the pipeline — runners and
// queues (invariant 5). It lives in the repo so the pipeline and the
// permissions it grants are versioned and reviewed like code, and so every
// developer on a team runs the same pipeline against the same board.
//
// A repo config names the teams one repo serves. The other half of SCOPE
// invariant 2 — that no two repos claim the same team — cannot be
// checked here: loading a single repo config sees a single repo. No
// startup verification of the full team → repo function exists yet
// anywhere else either; for now the cross-repo half of the rule is the
// operator's to keep.
type RepoConfig struct {
	Teams     []string          `toml:"teams"`
	Provision string            `toml:"provision"`
	Dispose   string            `toml:"dispose"`
	Runners   map[string]Runner `toml:"runners"`
	Queues    map[string]Queue  `toml:"queues"`
}

// Runner is an adapter to a coding-agent CLI. Command is a template
// with placeholders for the prompt and working directory; Resume,
// if set, is a template with a placeholder for a session id, handed
// to the operator on eject.
type Runner struct {
	Command string `toml:"command"`
	Resume  string `toml:"resume"`
}

// Queue is a Linear status with instructions attached: exactly the
// four fields of SCOPE concept 2, plus the optional on_failure.
// OnSuccess and OnFailure name Linear statuses, not queues — they may
// point at a status with no queue (a human review column). That Status
// and both targets exist on each configured team's board is verified
// once at startup, before the first reconciler pass (loop.Verify), not
// here: loading config cannot see the board.
type Queue struct {
	Status    string `toml:"status"`
	Prompt    string `toml:"prompt"`
	Runner    string `toml:"runner"`
	OnSuccess string `toml:"on_success"`
	OnFailure string `toml:"on_failure"`
}

// ExpandPrompt returns the queue's prompt with its placeholders filled in:
// {{ticket}} from the given identifier, and {{status}}, {{on_success}}, and
// {{on_failure}} from the queue's own fields, so prompt prose follows the
// configured statuses instead of hardcoding names that a rename would orphan.
// A prompt sees its own queue's pointers and the ticket, nothing else — no
// other queue's fields, no conditionals, no templating language.
func (q Queue) ExpandPrompt(ticket string) string {
	return strings.NewReplacer(
		"{{ticket}}", ticket,
		"{{status}}", q.Status,
		"{{on_success}}", q.OnSuccess,
		"{{on_failure}}", q.OnFailure,
	).Replace(q.Prompt)
}

// StockRepoConfig renders the full stock pipeline under its stock status
// names. bypass keeps the stock runner's `--permission-mode
// bypassPermissions` grant; declining strips the flag, leaving a runner the
// operator must widen deliberately before unattended runs can do real work.
func StockRepoConfig(teams []string, bypass bool) string {
	return Stock{Teams: teams, Bypass: bypass, Plan: true, Review: true}.Render()
}

// ExampleRepoConfig renders the shipped lerp.example.toml: the stock
// pipeline for one team with the permission grant accepted. The example is a
// derived artifact, and this is what derives it — `internal/config/example`
// writes the file and TestStockMatchesExample pins the committed bytes to
// this output. The parameters live here, in one place, so the generator and
// the pin cannot disagree about what the example is supposed to be.
func ExampleRepoConfig() string {
	return StockRepoConfig([]string{"LERP"}, true)
}

// Render assembles the stock lerp.toml text for these choices. Assembly is
// textual, never parse-and-re-emit: the template's comments are part of the
// product, and a TOML round-trip would strip them. Prompt bodies are
// untouched — they reference {{status}}/{{on_success}}/{{on_failure}},
// expanded at run time, so the prose follows whatever mapping is chosen
// here.
func (s Stock) Render() string {
	quoted := make([]string, len(s.Teams))
	for i, team := range s.Teams {
		quoted[i] = fmt.Sprintf("%q", team)
	}
	rendered := renderSections(stockRepo, map[string]bool{"plan": s.Plan, "review": s.Review})
	rendered = strings.NewReplacer(
		"{{teams}}", strings.Join(quoted, ", "),
		"{{plan_status}}", orStock(s.PlanStatus, StockPlanStatus),
		"{{plan_review_status}}", orStock(s.PlanReviewStatus, StockPlanReviewStatus),
		"{{implement_status}}", orStock(s.ImplementStatus, StockImplementStatus),
		"{{exit_status}}", orStock(s.ExitStatus, StockExitStatus),
		"{{attention_status}}", orStock(s.AttentionStatus, StockAttentionStatus),
	).Replace(rendered)
	if !s.Bypass {
		rendered = strings.ReplaceAll(rendered, bypassFlag, "")
	}
	return rendered
}

// renderSections keeps or drops the template's optional sections. A line
// `#{{name}}` opens section name, `#{{/name}}` closes it; marker lines are
// always removed, and a section's lines survive only when include says so.
// Sections do not nest.
func renderSections(src string, include map[string]bool) string {
	kept := []string{}
	section := ""
	for _, line := range strings.Split(src, "\n") {
		if name, ok := strings.CutPrefix(line, "#{{"); ok && strings.HasSuffix(name, "}}") {
			section = strings.TrimSuffix(name, "}}")
			if strings.HasPrefix(section, "/") {
				section = ""
			}
			continue
		}
		if section == "" || include[section] {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// orStock returns name, or the stock default when the caller left it empty.
func orStock(name, stock string) string {
	if name == "" {
		return stock
	}
	return name
}

// WatchedStatuses is the set of statuses some configured queue picks up from
// — SCOPE concept 2's `status` field, collected. It is the single spelling of
// "a queue watches this", and three separate rules turn on it: whether a
// finished run releases its claim (the rule LERP-50 and LERP-59 each got
// wrong by writing it out a second time), which tickets the inbox calls
// waiting on a human, and which of the statuses a config names init reports
// as an exit rather than a stage. The loop's own prose calls these the
// *served* statuses; they are the same set.
//
// Every other status lerp.toml names — the on_success and on_failure targets
// no queue watches — is a pipeline exit by construction, so no method spells
// that out separately: it is PromoteTargets minus this.
func (c *RepoConfig) WatchedStatuses() map[string]bool {
	// One entry per queue exactly: validate rejects two queues sharing a
	// status.
	watched := make(map[string]bool, len(c.Queues))
	for _, q := range c.Queues {
		watched[q.Status] = true
	}
	return watched
}

// PromoteTargets lists every status the TUI's promote action may move a
// ticket into: each queue's own status, in queue-name order, followed by any
// on_success/on_failure target not already a queue's status — the
// pipeline's exits, statuses no queue watches (a review column, a "Needs
// Attention" parking spot). Each name appears once.
func (c *RepoConfig) PromoteTargets() []string {
	seen := make(map[string]bool)
	var targets []string
	add := func(status string) {
		if status == "" || seen[status] {
			return
		}
		seen[status] = true
		targets = append(targets, status)
	}
	names := slices.Sorted(maps.Keys(c.Queues))
	for _, name := range names {
		add(c.Queues[name].Status)
	}
	for _, name := range names {
		q := c.Queues[name]
		add(q.OnSuccess)
		add(q.OnFailure)
	}
	return targets
}

// LoadRepoConfig reads and validates the per-repo config file at path
// (conventionally RepoConfigFile at the repo root).
func LoadRepoConfig(path string) (*RepoConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return ParseRepoConfig(string(data), path)
}

// ParseRepoConfig decodes and validates repo config source; label names the
// origin (a file path) in errors.
func ParseRepoConfig(source, label string) (*RepoConfig, error) {
	var c RepoConfig
	md, err := toml.Decode(source, &c)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	if keys := md.Undecoded(); len(keys) > 0 {
		return nil, fmt.Errorf("%s: unknown key(s): %s", label, joinKeys(keys))
	}
	if err := c.validate(label); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *RepoConfig) validate(path string) error {
	if len(c.Teams) == 0 {
		return fmt.Errorf("%s: teams must list at least one Linear team key", path)
	}
	seen := make(map[string]bool)
	for _, team := range c.Teams {
		if team == "" {
			return fmt.Errorf("%s: teams must not contain an empty team key", path)
		}
		if seen[team] {
			return fmt.Errorf("%s: team %q is listed more than once", path, team)
		}
		seen[team] = true
	}
	if c.Provision == "" {
		return fmt.Errorf("%s: provision must not be empty", path)
	}
	if c.Dispose == "" {
		return fmt.Errorf("%s: dispose must not be empty", path)
	}
	for _, name := range slices.Sorted(maps.Keys(c.Runners)) {
		if c.Runners[name].Command == "" {
			return fmt.Errorf("%s: runner %q: command must not be empty", path, name)
		}
	}
	if len(c.Queues) == 0 {
		return fmt.Errorf("%s: at least one queue is required", path)
	}
	queueByStatus := make(map[string]string)
	for _, name := range slices.Sorted(maps.Keys(c.Queues)) {
		q := c.Queues[name]
		switch {
		case q.Status == "":
			return fmt.Errorf("%s: queue %q: status must not be empty", path, name)
		case q.Prompt == "":
			return fmt.Errorf("%s: queue %q: prompt must not be empty", path, name)
		case q.Runner == "":
			return fmt.Errorf("%s: queue %q: runner must not be empty", path, name)
		case q.OnSuccess == "":
			return fmt.Errorf("%s: queue %q: on_success must not be empty", path, name)
		}
		if _, ok := c.Runners[q.Runner]; !ok {
			return fmt.Errorf("%s: queue %q: runner %q is not defined under [runners]", path, name, q.Runner)
		}
		// Of the placeholders ExpandPrompt fills, on_failure is the only one
		// whose field may be absent — status and on_success are required
		// above, and the ticket is always supplied. A prompt that references
		// it anyway would expand to an empty string at run time, sending
		// agents to a nameless status; fail at load instead.
		if strings.Contains(q.Prompt, "{{on_failure}}") && q.OnFailure == "" {
			return fmt.Errorf("%s: queue %q: prompt references {{on_failure}} but on_failure is not set", path, name)
		}
		if prev, dup := queueByStatus[q.Status]; dup {
			return fmt.Errorf("%s: queues %q and %q both watch status %q; a status may drive at most one queue", path, prev, name, q.Status)
		}
		queueByStatus[q.Status] = name
	}
	return nil
}

func joinKeys(keys []toml.Key) string {
	names := make([]string, len(keys))
	for i, k := range keys {
		names[i] = k.String()
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}
