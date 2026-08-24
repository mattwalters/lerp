// Command lerp orchestrates software work through Linear: tickets go
// on a board, lerp runs coding agents to move them across it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/initcmd"
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
	if len(os.Args) > 1 && os.Args[1] == "init" {
		initCommand(os.Args[2:])
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

func initCommand(args []string) {
	fs := flag.NewFlagSet("lerp init", flag.ExitOnError)
	team := fs.String("team", "", "Linear team key to set up")
	name := fs.String("team-name", "", "name to use if the Linear team must be created")
	fs.Parse(args)
	if *team == "" {
		fmt.Fprintln(os.Stderr, "lerp init: --team is required")
		os.Exit(2)
	}
	if os.Getenv("LINEAR_API_KEY") == "" {
		fatal(fmt.Errorf("lerp init: LINEAR_API_KEY is required"))
	}
	globalPath, err := config.GlobalPath()
	if err != nil {
		fatal(err)
	}
	global, err := config.LoadGlobal(globalPath)
	if err != nil {
		fatal(fmt.Errorf("load global config: %w", err))
	}
	repoRoot, err := gitRoot()
	if err != nil {
		fatal(fmt.Errorf("get working directory: %w", err))
	}
	if err := initcmd.Init(context.Background(), linear.New(os.Getenv("LINEAR_API_KEY"), nil), global, filepath.Clean(repoRoot), *team, *name); err != nil {
		fatal(fmt.Errorf("lerp init: %w", err))
	}
	fmt.Printf("initialized %s for Linear team %s\n", repoRoot, *team)
}

func gitRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("find repository root (run lerp init from a Git repository): %w", err)
	}
	return filepath.Clean(strings.TrimSpace(string(out))), nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
