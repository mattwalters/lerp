package config

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// CovenantFile is the name of the per-repo covenant, found at the
// repo root.
const CovenantFile = "lerp.toml"

// defaultLanes is the lane count used when the global config does not
// set one.
const defaultLanes = 5

// Global is the operator-wide config: lanes, runners, and queues.
type Global struct {
	Lanes   int               `toml:"lanes"`
	Runners map[string]Runner `toml:"runners"`
	Queues  map[string]Queue  `toml:"queues"`
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
// point at a status with no queue (a human review column). Whether
// they exist on the actual Linear board is checked by the loop at
// startup, not here.
type Queue struct {
	Status    string `toml:"status"`
	Prompt    string `toml:"prompt"`
	Runner    string `toml:"runner"`
	OnSuccess string `toml:"on_success"`
	OnFailure string `toml:"on_failure"`
}

// Covenant is the per-repo config: the Linear teams this repo serves
// and the commands that provision and dispose lane workspaces
// (SCOPE invariants 2 and 9).
//
// A covenant names the teams one repo serves. The other half of SCOPE
// invariant 2 — that no two repos claim the same team — cannot be
// checked here: loading a single covenant sees a single repo. The
// loop verifies the full team → repo function at startup and refuses
// to run if it doesn't hold.
type Covenant struct {
	Teams     []string `toml:"teams"`
	Provision string   `toml:"provision"`
	Dispose   string   `toml:"dispose"`
}

// GlobalPath returns the default location of the global config file:
// $XDG_CONFIG_HOME/lerp/config.toml, or ~/.config/lerp/config.toml
// when XDG_CONFIG_HOME is unset.
func GlobalPath() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "lerp", "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating global config: %w", err)
	}
	return filepath.Join(home, ".config", "lerp", "config.toml"), nil
}

// LoadGlobal reads and validates the global config at path.
func LoadGlobal(path string) (*Global, error) {
	var g Global
	md, err := toml.DecodeFile(path, &g)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if keys := md.Undecoded(); len(keys) > 0 {
		return nil, fmt.Errorf("%s: unknown key(s): %s", path, joinKeys(keys))
	}
	if !md.IsDefined("lanes") {
		g.Lanes = defaultLanes
	}
	if err := g.validate(path); err != nil {
		return nil, err
	}
	return &g, nil
}

func (g *Global) validate(path string) error {
	if g.Lanes < 1 {
		return fmt.Errorf("%s: lanes must be at least 1, got %d", path, g.Lanes)
	}
	for _, name := range slices.Sorted(maps.Keys(g.Runners)) {
		if g.Runners[name].Command == "" {
			return fmt.Errorf("%s: runner %q: command must not be empty", path, name)
		}
	}
	queueByStatus := make(map[string]string)
	for _, name := range slices.Sorted(maps.Keys(g.Queues)) {
		q := g.Queues[name]
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
		if _, ok := g.Runners[q.Runner]; !ok {
			return fmt.Errorf("%s: queue %q: runner %q is not defined under [runners]", path, name, q.Runner)
		}
		if prev, dup := queueByStatus[q.Status]; dup {
			return fmt.Errorf("%s: queues %q and %q both watch status %q; a status may drive at most one queue", path, prev, name, q.Status)
		}
		queueByStatus[q.Status] = name
	}
	return nil
}

// LoadCovenant reads and validates the per-repo covenant at path
// (conventionally CovenantFile at the repo root).
func LoadCovenant(path string) (*Covenant, error) {
	var c Covenant
	md, err := toml.DecodeFile(path, &c)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if keys := md.Undecoded(); len(keys) > 0 {
		return nil, fmt.Errorf("%s: unknown key(s): %s", path, joinKeys(keys))
	}
	if err := c.validate(path); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Covenant) validate(path string) error {
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
