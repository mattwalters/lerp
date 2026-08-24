// Package initcmd contains setup-time operations for lerp init.
// It deliberately has no dependency on the runtime loop.
package initcmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/linear"
)

// Board is the small setup-time Linear surface used by Init.
// EnsureWorkflowStates reports the category of every state the team has
// after the call, keyed by state name.
type Board interface {
	EnsureTeam(ctx context.Context, key, name string) error
	EnsureWorkflowStates(ctx context.Context, teamKey string, states []linear.StateSpec) (map[string]string, error)
}

// Init creates the board structure this repo's config requires, writing the
// stock config first when the repo has none. Repeating it verifies the
// existing config rather than replacing a user's choices.
//
// When lerp.toml is absent, confirmBypass is consulted exactly once: it
// decides whether the stock runner keeps its bypassPermissions grant (nil
// declines). The file is written only after the board calls succeed, so a
// failed init leaves nothing behind; created reports whether this invocation
// wrote it. The file lands uncommitted in the working tree, where the grant
// is reviewed and checked in like any other code.
//
// out receives the pipeline-exit report (see reportExits); nil discards it.
func Init(ctx context.Context, board Board, out io.Writer, repoRoot, teamKey, teamName string, confirmBypass func() bool) (created bool, err error) {
	if teamKey == "" {
		return false, fmt.Errorf("team key must not be empty")
	}
	if teamName == "" {
		teamName = teamKey
	}
	if out == nil {
		out = io.Discard
	}
	path := filepath.Join(repoRoot, config.RepoConfigFile)
	cfg, stock, err := planRepoConfig(path, teamKey, confirmBypass)
	if err != nil {
		return false, err
	}
	if err := board.EnsureTeam(ctx, teamKey, teamName); err != nil {
		return false, fmt.Errorf("ensure team %q: %w", teamKey, err)
	}
	categories, err := board.EnsureWorkflowStates(ctx, teamKey, stateSpecs(cfg))
	if err != nil {
		return false, fmt.Errorf("ensure workflow states for %q: %w", teamKey, err)
	}
	reportExits(out, cfg, categories)
	if stock == "" {
		return false, nil
	}
	return writeRepoConfig(path, teamKey, stock)
}

// planRepoConfig loads an existing lerp.toml and verifies it serves teamKey,
// or renders the stock config to be written later. stock is empty when the
// file already exists.
func planRepoConfig(path, teamKey string, confirmBypass func() bool) (cfg *config.RepoConfig, stock string, err error) {
	if _, err := os.Stat(path); err == nil {
		cfg, err := loadFor(path, teamKey)
		return cfg, "", err
	} else if !os.IsNotExist(err) {
		return nil, "", fmt.Errorf("check repo config: %w", err)
	}
	bypass := confirmBypass != nil && confirmBypass()
	stock = config.StockRepoConfig([]string{teamKey}, bypass)
	// Parsing what we are about to install catches a broken stock template
	// here, not on the operator's first run.
	cfg, err = config.ParseRepoConfig(stock, path)
	if err != nil {
		return nil, "", fmt.Errorf("stock repo config: %w", err)
	}
	return cfg, stock, nil
}

func loadFor(path, teamKey string) (*config.RepoConfig, error) {
	c, err := config.LoadRepoConfig(path)
	if err != nil {
		return nil, fmt.Errorf("existing repo config: %w", err)
	}
	if !slices.Contains(c.Teams, teamKey) {
		return nil, fmt.Errorf("existing repo config %s does not serve team %q", path, teamKey)
	}
	return c, nil
}

func writeRepoConfig(path, teamKey, stock string) (created bool, err error) {
	// O_EXCL makes creation race-safe and, importantly, never overwrites a
	// configuration that appeared since planRepoConfig looked.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			_, err := loadFor(path, teamKey)
			return false, err
		}
		return false, fmt.Errorf("create repo config: %w", err)
	}
	_, writeErr := f.WriteString(stock)
	closeErr := f.Close()
	if writeErr != nil {
		return false, fmt.Errorf("write repo config: %w", writeErr)
	}
	if closeErr != nil {
		return false, fmt.Errorf("close repo config: %w", closeErr)
	}
	return true, nil
}

// stateSpecs names every status the queues reference, all in Linear's
// "started" category.
//
// "started" is deliberate, not a default: whether a status ends work is a
// fact about the operator's process that queue topology cannot reveal. An
// on_success target no queue watches is just as often a human column
// ("Ready to Merge") as a terminal one, and creating a human column as
// completed silently stops its tickets from blocking their dependents
// (see linear.StateSpec) — work becomes eligible before it is done.
// Created as "started", the failure mode is at least loud: a finished
// blocker that still blocks is something a human notices. reportExits
// turns that residual risk into an explicit instruction.
func stateSpecs(cfg *config.RepoConfig) []linear.StateSpec {
	names := map[string]bool{}
	for _, q := range cfg.Queues {
		names[q.Status] = true
		names[q.OnSuccess] = true
		if q.OnFailure != "" {
			names[q.OnFailure] = true
		}
	}
	sorted := make([]string, 0, len(names))
	for name := range names {
		sorted = append(sorted, name)
	}
	slices.Sort(sorted)
	specs := make([]linear.StateSpec, 0, len(sorted))
	for _, name := range sorted {
		specs = append(specs, linear.StateSpec{Name: name, Type: "started"})
	}
	return specs
}

// pipelineExits are the on_success targets no queue watches: the statuses
// where work leaves the automated path.
func pipelineExits(cfg *config.RepoConfig) []string {
	watched := map[string]bool{}
	for _, q := range cfg.Queues {
		watched[q.Status] = true
	}
	seen := map[string]bool{}
	exits := []string{}
	for _, q := range cfg.Queues {
		if watched[q.OnSuccess] || seen[q.OnSuccess] {
			continue
		}
		seen[q.OnSuccess] = true
		exits = append(exits, q.OnSuccess)
	}
	slices.Sort(exits)
	return exits
}

// reportExits tells the operator, for each pipeline exit, whether Linear's
// category for that status ends work. Lerp never sets a completed category
// itself — that guess is wrong for a human column and its cost is silent —
// so a genuinely terminal exit is the one piece of board setup only the
// operator can finish, and this report is where init says so.
func reportExits(out io.Writer, cfg *config.RepoConfig, categories map[string]string) {
	for _, name := range pipelineExits(cfg) {
		category, ok := categories[name]
		if category == "completed" || category == "canceled" {
			fmt.Fprintf(out, "pipeline exit %q: Linear categorises it as %s; tickets that land there stop blocking their dependents.\n", name, category)
			continue
		}
		if !ok {
			category = "unknown"
		}
		fmt.Fprintf(out, "pipeline exit %q: Linear categorises it as %s, not completed.\n", name, category)
		fmt.Fprintf(out, "  Tickets that land there keep blocking their dependents — right if a human\n")
		fmt.Fprintf(out, "  still acts on them there, wrong if %q means the work is done. Lerp\n", name)
		fmt.Fprintf(out, "  will not guess: if that status truly ends work, set its category to Done\n")
		fmt.Fprintf(out, "  in Linear yourself.\n")
	}
}
