// Command lerp orchestrates software work through Linear: tickets go
// on a board, lerp runs coding agents to move them across it.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/linear"
	"github.com/mattwalters/lerp/internal/loop"
	"github.com/mattwalters/lerp/internal/version"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "once" {
		if err := once(context.Background()); err != nil {
			fmt.Fprintln(os.Stderr, "lerp once:", err)
			os.Exit(1)
		}
		return
	}
	fmt.Printf("lerp %s\n", version.Version)
}

// once is a temporary single-lane command for exercising the first vertical
// slice. Its workspace and log live under the system temporary directory;
// durable run evidence will replace this path policy with the reconciler.
func once(ctx context.Context) error {
	if len(os.Args) != 2 {
		return errors.New("usage: lerp once")
	}
	apiKey := os.Getenv("LINEAR_API_KEY")
	if apiKey == "" {
		return errors.New("LINEAR_API_KEY is required")
	}
	repoDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get current directory: %w", err)
	}
	globalPath, err := config.GlobalPath()
	if err != nil {
		return err
	}
	global, err := config.LoadGlobal(globalPath)
	if err != nil {
		return err
	}
	repo, err := config.LoadRepoConfig(filepath.Join(repoDir, config.RepoConfigFile))
	if err != nil {
		return err
	}

	root := filepath.Join(os.TempDir(), "lerp-once")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create temporary run directory: %w", err)
	}
	ran, err := loop.Once(ctx, loop.OnceOptions{
		Client:  linear.New(apiKey, nil),
		Global:  global,
		Repo:    repo,
		RepoDir: repoDir,
		Lane:    1,
		WorkspaceFor: func(issue linear.Issue) string {
			return filepath.Join(root, "workspace-lane-1-"+issue.ID)
		},
		LogPathFor: func(issue linear.Issue) string {
			return filepath.Join(root, "run-lane-1-"+issue.ID+".log")
		},
		Log: os.Stderr,
	})
	if err != nil {
		return err
	}
	if ran {
		fmt.Println("lerp once: completed one ticket")
	} else {
		fmt.Println("lerp once: no eligible ticket")
	}
	return nil
}
