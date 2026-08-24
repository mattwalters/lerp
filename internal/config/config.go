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

// stockRepo is the first-run lerp.toml written by lerp init, rendered by
// StockRepoConfig. Keep it in the config package so the binary, rather than a
// source checkout, owns the template it installs.
//
//go:embed stock.toml
var stockRepo string

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
// once at startup, before the first reconciler pass
// (loop.VerifyStatuses), not here: loading config cannot see the board.
type Queue struct {
	Status    string `toml:"status"`
	Prompt    string `toml:"prompt"`
	Runner    string `toml:"runner"`
	OnSuccess string `toml:"on_success"`
	OnFailure string `toml:"on_failure"`
}

// StockRepoConfig renders the stock lerp.toml for the given teams. bypass
// keeps the stock runner's `--permission-mode bypassPermissions` grant;
// declining strips the flag, leaving a runner the operator must widen
// deliberately before unattended runs can do real work.
func StockRepoConfig(teams []string, bypass bool) string {
	quoted := make([]string, len(teams))
	for i, team := range teams {
		quoted[i] = fmt.Sprintf("%q", team)
	}
	rendered := strings.ReplaceAll(stockRepo, "{{teams}}", strings.Join(quoted, ", "))
	if !bypass {
		rendered = strings.ReplaceAll(rendered, bypassFlag, "")
	}
	return rendered
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
