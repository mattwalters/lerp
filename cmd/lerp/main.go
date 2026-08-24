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

const usage = `usage:
  lerp                  print the version
  lerp version          print the version
  lerp init --team KEY  create missing Linear structure and this repo's lerp.toml
  lerp once             run one eligible ticket through its queue
`

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Printf("lerp %s\n", version.Version)
		return
	}
	switch args[0] {
	case "version":
		fmt.Printf("lerp %s\n", version.Version)
	case "init":
		initCommand(args[1:])
	case "once":
		if len(args) != 1 {
			fmt.Fprint(os.Stderr, "lerp once takes no arguments\n\n"+usage)
			os.Exit(2)
		}
		if err := once(context.Background()); err != nil {
			fatal(fmt.Errorf("lerp once: %w", err))
		}
	default:
		// Falling through to the version here would make a typo look like a
		// success: `lerp int --team LERP` would print a version and exit 0.
		fmt.Fprintf(os.Stderr, "lerp: unknown command %q\n\n%s", args[0], usage)
		os.Exit(2)
	}
}

// once is a temporary single-lane command for exercising the first vertical
// slice. Its workspace and log live under the system temporary directory;
// durable run evidence will replace this path policy with the reconciler.
func once(ctx context.Context) error {
	apiKey := os.Getenv("LINEAR_API_KEY")
	if apiKey == "" {
		return errors.New("LINEAR_API_KEY is required")
	}
	// The repo root, not the working directory: lerp init writes lerp.toml at
	// the root, so resolving it from the cwd would fail everywhere but there.
	repoDir, err := gitRoot()
	if err != nil {
		return err
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

	// A fresh private directory per invocation (MkdirTemp creates it 0700).
	// A fixed world-readable path under /tmp would let any other user on the
	// host pre-create or symlink it and read the workspace and the agent log.
	root, err := os.MkdirTemp("", "lerp-once-")
	if err != nil {
		return fmt.Errorf("create temporary run directory: %w", err)
	}
	var logPath string
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
			logPath = filepath.Join(root, "run-lane-1-"+issue.ID+".log")
			return logPath
		},
		Log: os.Stderr,
	})
	if err != nil {
		return err
	}
	if !ran {
		fmt.Println("lerp once: no eligible ticket")
		return nil
	}
	fmt.Println("lerp once: completed one ticket")
	if logPath != "" {
		fmt.Printf("lerp once: agent log at %s\n", logPath)
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
	global, created, err := config.LoadOrCreateGlobal(globalPath)
	if err != nil {
		fatal(fmt.Errorf("load global config: %w", err))
	}
	if created {
		fmt.Printf("created global config at %s from Lerp's stock pipeline\n", globalPath)
		fmt.Fprintln(os.Stderr, "review it before running agents: the stock Claude runner grants broad workspace permissions")
	}
	repoRoot, err := gitRoot()
	if err != nil {
		fatal(err)
	}
	if err := initcmd.Init(context.Background(), linear.New(os.Getenv("LINEAR_API_KEY"), nil), global, filepath.Clean(repoRoot), *team, *name); err != nil {
		fatal(fmt.Errorf("lerp init: %w", err))
	}
	fmt.Printf("initialized %s for Linear team %s\n", repoRoot, *team)
}

func gitRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("find repository root (lerp runs inside a Git repository): %w", err)
	}
	return filepath.Clean(strings.TrimSpace(string(out))), nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
