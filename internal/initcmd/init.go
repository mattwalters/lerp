// Package initcmd contains setup-time operations for lerp init.
// It deliberately has no dependency on the runtime loop.
package initcmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/mattwalters/lerp/internal/config"
)

// Board is the small setup-time Linear surface used by Init.
type Board interface {
	EnsureTeam(ctx context.Context, key, name string) error
	EnsureWorkflowStates(ctx context.Context, teamKey string, names []string) error
}

// Init creates the missing board statuses required by global, then creates a
// repo config at repoRoot. Repeating it verifies the existing config rather
// than replacing a user's choices.
func Init(ctx context.Context, board Board, global *config.Global, repoRoot, teamKey, teamName string) error {
	if teamKey == "" {
		return fmt.Errorf("team key must not be empty")
	}
	if teamName == "" {
		teamName = teamKey
	}
	if err := board.EnsureTeam(ctx, teamKey, teamName); err != nil {
		return fmt.Errorf("ensure team %q: %w", teamKey, err)
	}
	if err := board.EnsureWorkflowStates(ctx, teamKey, stateNames(global)); err != nil {
		return fmt.Errorf("ensure workflow states for %q: %w", teamKey, err)
	}
	return ensureRepoConfig(filepath.Join(repoRoot, config.RepoConfigFile), teamKey)
}

func stateNames(global *config.Global) []string {
	set := map[string]bool{}
	for _, q := range global.Queues {
		set[q.Status] = true
		set[q.OnSuccess] = true
		if q.OnFailure != "" {
			set[q.OnFailure] = true
		}
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func ensureRepoConfig(path, teamKey string) error {
	if _, err := os.Stat(path); err == nil {
		c, err := config.LoadRepoConfig(path)
		if err != nil {
			return fmt.Errorf("existing repo config: %w", err)
		}
		if !slices.Contains(c.Teams, teamKey) {
			return fmt.Errorf("existing repo config %s does not serve team %q", path, teamKey)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check repo config: %w", err)
	}

	c := config.RepoConfig{
		Teams:     []string{teamKey},
		Provision: "git worktree add --detach \"$LERP_WORKSPACE\" HEAD",
		Dispose:   "git worktree remove --force \"$LERP_WORKSPACE\"",
	}
	var b strings.Builder
	if err := toml.NewEncoder(&b).Encode(c); err != nil {
		return fmt.Errorf("encode repo config: %w", err)
	}
	// O_EXCL makes creation race-safe and, importantly, never overwrites an
	// existing checked-in configuration.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return ensureRepoConfig(path, teamKey)
		}
		return fmt.Errorf("create repo config: %w", err)
	}
	_, writeErr := f.WriteString(b.String())
	closeErr := f.Close()
	if writeErr != nil {
		return fmt.Errorf("write repo config: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close repo config: %w", closeErr)
	}
	return nil
}
