// Command lerp orchestrates software work through Linear: tickets go
// on a board, lerp runs coding agents to move them across it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/evidence"
	"github.com/mattwalters/lerp/internal/initcmd"
	"github.com/mattwalters/lerp/internal/linear"
	"github.com/mattwalters/lerp/internal/loop"
	"github.com/mattwalters/lerp/internal/tui"
	"github.com/mattwalters/lerp/internal/version"
)

const usage = `usage:
  lerp [-concurrency N]         open the TUI; the loop runs while it is open
  lerp version                  print the version
  lerp init --team KEY [--yes]  map lerp's queues onto the team's board and write this repo's lerp.toml
`

// defaultLanes is how many agents run at once unless -concurrency says so.
// SCOPE keeps N small and leaves the number to this default; each lane is a
// whole workspace, so a repo with a heavy provision command may want
// -concurrency lower.
const defaultLanes = 10

func main() {
	args := os.Args[1:]
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fs := flag.NewFlagSet("lerp", flag.ExitOnError)
		lanes := fs.Int("concurrency", defaultLanes, "how many agents may run at once")
		// -h shows the whole surface, not just the flags: the subcommands are
		// undiscoverable otherwise.
		fs.Usage = func() {
			fmt.Fprint(os.Stderr, usage+"\nflags:\n")
			fs.PrintDefaults()
		}
		fs.Parse(args)
		if fs.NArg() != 0 {
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}
		if *lanes < 1 {
			fmt.Fprintln(os.Stderr, "lerp: -concurrency must be at least 1")
			os.Exit(2)
		}
		// A bare `lerp` in a pipe or a command substitution must not quietly
		// grab /dev/tty (Bubble Tea's fallback for a non-terminal stdin) and
		// start claiming tickets: an engine run is an operator's decision,
		// made at a terminal.
		if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
			fmt.Fprint(os.Stderr, "lerp: the TUI needs a terminal\n\n"+usage)
			os.Exit(2)
		}
		if err := openTUI(context.Background(), *lanes); err != nil {
			fatal(fmt.Errorf("lerp: %w", err))
		}
		return
	}
	switch args[0] {
	case "version":
		fmt.Printf("lerp %s\n", version.Version)
	case "init":
		initCommand(args[1:])
	default:
		// Falling through to the version here would make a typo look like a
		// success: `lerp int --team LERP` would print a version and exit 0.
		fmt.Fprintf(os.Stderr, "lerp: unknown command %q\n\n%s", args[0], usage)
		os.Exit(2)
	}
}

// openTUI runs the reconciler under the Bubble Tea shell. The TUI is the
// engine: it drives the loop's ticks and subscribes to its events; nothing
// runs when it is closed, and no daemon exists (SCOPE, "The interface").
func openTUI(ctx context.Context, lanes int) error {
	apiKey := os.Getenv("LINEAR_API_KEY")
	if apiKey == "" {
		return errors.New("LINEAR_API_KEY is required")
	}
	repoDir, err := gitRoot()
	if err != nil {
		return err
	}
	repo, err := config.LoadRepoConfig(filepath.Join(repoDir, config.RepoConfigFile))
	if err != nil {
		return err
	}

	// Refuse to run before the first reconciler pass unless every configured
	// status exists on its team (SCOPE invariant 2's refuse-at-startup
	// spirit): a misspelled queue status would poll as a permanently empty
	// queue, not an error, and a missing on_success target would fail only
	// after an agent's whole run.
	client := linear.New(apiKey, nil)
	if err := loop.VerifyStatuses(ctx, client, repo); err != nil {
		return err
	}

	ev := evidence.New(repoDir)
	lock, err := ev.AcquireLock()
	if err != nil {
		return err
	}
	defer lock.Close()

	// The loop's diagnostic stream — provision, dispose, and runner output —
	// is ephemeral process detail: a local file, discarded without ceremony
	// (SCOPE invariant 7). Agent output itself goes to each run's own log,
	// which the board tails. Append rather than truncate: a session that dies
	// at launch must not have already destroyed the previous session's crash
	// diagnostics. The marker line keeps sessions distinguishable.
	loopLog, err := os.OpenFile(filepath.Join(repoDir, ".lerp", "loop.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open loop log: %w", err)
	}
	defer loopLog.Close()
	fmt.Fprintf(loopLog, "=== lerp session started %s ===\n", time.Now().Format(time.RFC3339))

	// Buffered so the loop rarely waits on a render; the model always keeps a
	// receive pending, so the channel drains for as long as the TUI is open.
	events := make(chan loop.Event, 64)
	rec, err := loop.NewReconciler(loop.ReconcilerOptions{
		Client:   client,
		Repo:     repo,
		RepoDir:  repoDir,
		Evidence: ev,
		Lanes:    lanes,
		Events:   func(ev loop.Event) { events <- ev },
		Log:      loopLog,
	})
	if err != nil {
		return err
	}

	// ctx is never cancelled on quit: quitting closes the screen, stops the
	// ticking, and waits (bounded — see tui.Run) for a pass already in flight
	// to settle before the lock and log above are released, while the agents —
	// their own process groups, with run evidence on disk — keep working. The
	// next lerp adopts them.
	return tui.Run(ctx, tui.Options{
		Ticker:   rec,
		Promoter: rec,
		Ejector:  rec,
		Starter:  rec,
		Reader:   rec,
		Statuses: repo.PromoteTargets(),
		Interval: loop.DefaultInterval,
		Lanes:    lanes,
		Events:   events,
	})
}

func initCommand(args []string) {
	fs := flag.NewFlagSet("lerp init", flag.ExitOnError)
	team := fs.String("team", "", "Linear team key to set up")
	name := fs.String("team-name", "", "name to use if the Linear team must be created")
	yes := fs.Bool("yes", false, "take the stock answer to every question")
	fs.Parse(args)
	if *team == "" {
		fmt.Fprintln(os.Stderr, "lerp init: --team is required")
		os.Exit(2)
	}
	if os.Getenv("LINEAR_API_KEY") == "" {
		fatal(fmt.Errorf("lerp init: LINEAR_API_KEY is required"))
	}
	repoRoot, err := gitRoot()
	if err != nil {
		fatal(err)
	}
	// The init conversation needs a terminal on both ends; a piped init takes
	// the stock answers, exactly as --yes does.
	var answers io.Reader
	if !*yes && isTerminal(os.Stdin) && isTerminal(os.Stdout) {
		answers = os.Stdin
	}
	created, err := initcmd.Init(context.Background(), linear.New(os.Getenv("LINEAR_API_KEY"), nil), os.Stdout, answers, filepath.Clean(repoRoot), *team, *name)
	if err != nil {
		fatal(fmt.Errorf("lerp init: %w", err))
	}
	if created {
		fmt.Printf("wrote %s with Lerp's stock pipeline — review it and check it in\n", config.RepoConfigFile)
	}
	fmt.Printf("initialized %s for Linear team %s\n", repoRoot, *team)
}

// isTerminal reports whether f is a character device — a terminal, as far as
// Stat can tell. A file that cannot be stat'd is given the benefit of the
// doubt; the guard exists to stop scripts, not to fight exotic ttys.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return true
	}
	return info.Mode()&os.ModeCharDevice != 0
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
