// Package initcmd contains setup-time operations for lerp init.
// It deliberately has no dependency on the runtime loop.
package initcmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/linear"
)

// Board is the small setup-time Linear surface used by Init.
type Board interface {
	EnsureTeam(ctx context.Context, key, name string) error
	EnsureWorkflowStates(ctx context.Context, teamKey string, states []linear.StateSpec) error
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
func Init(ctx context.Context, board Board, repoRoot, teamKey, teamName string, confirmBypass func() bool) (created bool, err error) {
	if teamKey == "" {
		return false, fmt.Errorf("team key must not be empty")
	}
	if teamName == "" {
		teamName = teamKey
	}
	path := filepath.Join(repoRoot, config.RepoConfigFile)
	cfg, stock, err := planRepoConfig(path, teamKey, confirmBypass)
	if err != nil {
		return false, err
	}
	if err := board.EnsureTeam(ctx, teamKey, teamName); err != nil {
		return false, fmt.Errorf("ensure team %q: %w", teamKey, err)
	}
	if err := board.EnsureWorkflowStates(ctx, teamKey, stateSpecs(cfg)); err != nil {
		return false, fmt.Errorf("ensure workflow states for %q: %w", teamKey, err)
	}
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

// stateSpecs names every status the queues reference, with the category to
// create it in when Linear does not have it yet.
//
// The rule is the smallest one that reads the topology correctly: a status some
// queue watches, or that failures are routed to, still holds live work, so it
// is "started". A status that is only ever an on_success target is where work
// leaves the automated path, so it is "completed" — created as "started",
// Linear would keep reporting finished tickets as blockers and every ticket
// waiting on one would be ineligible forever (see linear.StateSpec).
func stateSpecs(cfg *config.RepoConfig) []linear.StateSpec {
	const (
		live = "started"
		done = "completed"
	)
	category := map[string]string{}
	// on_success targets first, so a status that is also a queue status or a
	// failure route overwrites the terminal guess below.
	for _, q := range cfg.Queues {
		category[q.OnSuccess] = done
	}
	for _, q := range cfg.Queues {
		if q.OnFailure != "" {
			category[q.OnFailure] = live
		}
	}
	for _, q := range cfg.Queues {
		category[q.Status] = live
	}

	names := make([]string, 0, len(category))
	for name := range category {
		names = append(names, name)
	}
	slices.Sort(names)
	specs := make([]linear.StateSpec, 0, len(names))
	for _, name := range names {
		specs = append(specs, linear.StateSpec{Name: name, Type: category[name]})
	}
	return specs
}
