// Command lerp orchestrates software work through Linear: tickets go
// on a board, lerp runs coding agents to move them across it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/credentials"
	"github.com/mattwalters/lerp/internal/evidence"
	"github.com/mattwalters/lerp/internal/initcmd"
	"github.com/mattwalters/lerp/internal/linear"
	"github.com/mattwalters/lerp/internal/loop"
	"github.com/mattwalters/lerp/internal/theme"
	"github.com/mattwalters/lerp/internal/tui"
	"github.com/mattwalters/lerp/internal/update"
	"github.com/mattwalters/lerp/internal/version"
)

const usage = `usage:
  lerp [-concurrency N]         open the TUI; the loop runs while it is open
  lerp version, --version       print the version
  lerp login                    sign in to Linear (loopback OAuth); no flags
  lerp logout                   sign out of Linear and revoke the token; no flags
  lerp init [--team KEY] [--yes]  map lerp's queues onto the team's board and write this repo's lerp.toml
`

// defaultLanes is how many agents run at once unless -concurrency says so.
// SCOPE keeps N small and leaves the number to this default; each lane is a
// whole workspace, so a repo with a heavy provision command may want
// -concurrency lower.
const defaultLanes = 10

func main() {
	args := normalizeArgs(os.Args[1:])
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
			var notSetUp notSetUpError
			if errors.As(err, &notSetUp) {
				greeting(os.Stderr, notSetUp.dir)
				os.Exit(1)
			}
			fatal(fmt.Errorf("lerp: %w", err))
		}
		return
	}
	switch args[0] {
	case "version":
		printVersion(os.Stdout)
	case "login":
		// No flags, so an unrecognised one — --port, --help, a typo — must
		// not fall through silently into opening a browser and binding a
		// port for two minutes.
		if len(args) > 1 {
			fmt.Fprintf(os.Stderr, "lerp login: takes no arguments\n\n%s", usage)
			os.Exit(2)
		}
		if err := credentials.Login(context.Background(), os.Stdout); err != nil {
			fatal(fmt.Errorf("lerp login: %w", err))
		}
	case "logout":
		if len(args) > 1 {
			fmt.Fprintf(os.Stderr, "lerp logout: takes no arguments\n\n%s", usage)
			os.Exit(2)
		}
		if err := credentials.Logout(context.Background(), os.Stdout); err != nil {
			fatal(fmt.Errorf("lerp logout: %w", err))
		}
	case "init":
		initCommand(args[1:])
	default:
		// Falling through to the version here would make a typo look like a
		// success: `lerp int --team LERP` would print a version and exit 0.
		fmt.Fprintf(os.Stderr, "lerp: unknown command %q\n\n%s", args[0], usage)
		os.Exit(2)
	}
}

func printVersion(w io.Writer) {
	fmt.Fprintf(w, "lerp %s\n", version.Version)
	if latest, err := update.CachedLatest(version.Version); err == nil && latest != "" {
		fmt.Fprintf(w, "latest %s — brew upgrade lerp\n", latest)
	}
}

// normalizeArgs rewrites the flag-shaped --version, the spelling scripts and
// --help habits reach for, into the version subcommand. Left alone, it falls
// into the bare-TUI flag set below and dies as an unknown flag instead of
// printing anything.
func normalizeArgs(args []string) []string {
	if len(args) > 0 && args[0] == "--version" {
		args[0] = "version"
	}
	return args
}

// openTUI runs the reconciler under the Bubble Tea shell. The TUI is the
// engine: it drives the loop's ticks and subscribes to its events; nothing
// runs when it is closed, and no daemon exists (SCOPE, "The interface").
func openTUI(ctx context.Context, lanes int) error {
	// First, before anything costs anything: looking for a config is a stat
	// walk up the tree and costs less than resolving credentials, which may
	// renew a token over the network.
	repoDir, err := anchorDir()
	if err != nil {
		return err
	}
	repo, err := loadRepo(repoDir)
	if err != nil {
		return err
	}
	// Before a board check and a lock: an operator with no credential
	// should not pay for those before being told so. Resolved once here and
	// asked per request from then on — an API key answers with itself, a
	// stored token renews itself underneath.
	auth, err := credentials.Resolve(nil)
	if err != nil {
		return err
	}
	// Up here with the refusal above, for the same reason: the TUI applies
	// this itself, but only once everything below has run, and an operator
	// who misspelled it should not pay for a board check and a lock first.
	if err := theme.UseBackground(); err != nil {
		return err
	}

	// Refuse to run before the first reconciler pass unless every configured
	// status exists on its team (SCOPE invariant 2's refuse-at-startup
	// spirit): a misspelled queue status would poll as a permanently empty
	// queue, not an error, and a missing on_success target would fail only
	// after an agent's whole run. What the same check merely warns about — a
	// team git automation that would move a ticket mid-stage — is shown here
	// and the run starts anyway.
	client := linear.New(auth, nil)
	warnings, err := loop.Verify(ctx, client, repo, repoDir)
	if err != nil {
		return err
	}
	boardOrder, err := boardStatuses(ctx, client, repo.Teams)
	if err != nil {
		return err
	}
	ev := evidence.New(repoDir)
	lock, err := ev.AcquireLock()
	if err != nil {
		return err
	}
	defer lock.Close()

	// After the lock, not before: a second lerp on this clone should fail on
	// the lock rather than make the operator read and acknowledge a warning
	// about a run it is never going to start.
	announce(os.Stderr, os.Stdin, warnings, isTerminal(os.Stderr))

	// The loop's diagnostic stream — provision, dispose, and runner output —
	// is ephemeral process detail: a local file, discarded without ceremony
	// (SCOPE invariant 7). Agent output itself goes to each run's own log,
	// which the board tails. Append rather than truncate: a session that dies
	// at launch must not have already destroyed the previous session's crash
	// diagnostics. The marker line keeps sessions distinguishable.
	loopLog, err := os.OpenFile(ev.LoopLogPath(),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open loop log: %w", err)
	}
	defer loopLog.Close()
	fmt.Fprintf(loopLog, "=== lerp session started %s ===\n", time.Now().Format(time.RFC3339))

	// Buffered so the loop rarely waits on a render; the model always keeps a
	// receive pending, so the channel drains for as long as the TUI is open —
	// and tui.Run keeps draining it while it waits out a pass on the way out,
	// so a full buffer can never wedge the pass that is filling it.
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
		Engine:         rec,
		Statuses:       boardOrder,
		PromoteTargets: repo.PromoteTargets(),
		Windows:        repo.ContextWindows(),
		Runners:        repo.QueueRunners(),
		Interval:       loop.DefaultInterval,
		Lanes:          lanes,
		Events:         events,
		CheckUpdate: func(ctx context.Context) update.Notice {
			notice, err := update.Check(ctx, nil, time.Now(), version.Version)
			if err != nil {
				fmt.Fprintf(loopLog, "update check: %v\n", err)
			}
			return notice
		},
	})
}

// boardStatuses reads workflow states in Linear board order for each
// configured team, deduplicating names across teams while preserving order.
func boardStatuses(ctx context.Context, client linear.Client, teams []string) ([]string, error) {
	var order []string
	seen := make(map[string]bool)
	for _, team := range teams {
		states, err := client.TeamStates(ctx, team)
		if err != nil {
			return nil, fmt.Errorf("read workflow states for team %s: %w", team, err)
		}
		for _, s := range states {
			if !seen[s] {
				seen[s] = true
				order = append(order, s)
			}
		}
	}
	return order, nil
}

func initCommand(args []string) {
	fs := flag.NewFlagSet("lerp init", flag.ExitOnError)
	team := fs.String("team", "", "Linear team key to set up")
	name := fs.String("team-name", "", "name to use if the Linear team must be created")
	yes := fs.Bool("yes", false, "take the stock answer to every question")
	fs.Parse(args)
	*team = strings.ToUpper(strings.TrimSpace(*team))
	interactive := !*yes && isTerminal(os.Stdin) && isTerminal(os.Stdout)
	if *team == "" && !interactive {
		fmt.Fprintln(os.Stderr, "lerp init: --team is required")
		os.Exit(2)
	}
	auth, err := resolveWithLogin(context.Background(), os.Stdout, os.Stdin, interactive, func() (func(context.Context) (string, error), error) {
		return credentials.Resolve(nil)
	}, credentials.Login)
	if err != nil {
		fatal(fmt.Errorf("lerp init: %w", err))
	}
	repoRoot, err := initAnchorDir()
	if err != nil {
		fatal(fmt.Errorf("lerp init: %w", err))
	}
	// The init conversation needs a terminal on both ends; a piped init takes
	// the stock answers, exactly as --yes does.
	var answers io.Reader
	if interactive {
		answers = os.Stdin
	}
	created, err := initcmd.Init(context.Background(), linear.New(auth, nil), os.Stdout, answers, filepath.Clean(repoRoot), *team, *name)
	if err != nil {
		if errors.Is(err, initcmd.ErrCanceled) {
			fmt.Println("nothing was written")
			return
		}
		fatal(fmt.Errorf("lerp init: %w", err))
	}
	if created {
		fmt.Printf("wrote %s with Lerp's stock pipeline — review it and check it in\n", config.RepoConfigFile)
	}
}

// resolveWithLogin resolves Linear credentials, offering to run the OAuth
// login flow inline when running interactively and no credentials are found
// or the session has expired.
func resolveWithLogin(
	ctx context.Context,
	out io.Writer,
	in io.Reader,
	interactive bool,
	resolve func() (func(context.Context) (string, error), error),
	login func(context.Context, io.Writer) error,
) (func(context.Context) (string, error), error) {
	auth, err := resolve()
	var origErr error
	if err != nil {
		if err == credentials.ErrNoCredentials || errors.Is(err, credentials.ErrLoginRequired) {
			origErr = err
		} else {
			return nil, err
		}
	} else {
		_, probeErr := auth(ctx)
		if probeErr != nil && errors.Is(probeErr, credentials.ErrLoginRequired) {
			origErr = probeErr
		} else {
			return auth, nil
		}
	}

	if !interactive {
		return nil, origErr
	}

	if errors.Is(origErr, credentials.ErrLoginRequired) {
		fmt.Fprintln(out, "Linear session expired.")
	} else {
		fmt.Fprintln(out, "No Linear credentials found.")
	}
	fmt.Fprint(out, "Sign in to Linear now? [Y/n] ")

	ans, readErr := readByteAnswer(in)
	if readErr != nil || (ans != "" && ans != "y" && ans != "yes") {
		return nil, origErr
	}

	if err := login(ctx, out); err != nil {
		return nil, err
	}
	return resolve()
}

// readByteAnswer reads a line from r byte-at-a-time, returning the trimmed
// lowercase answer. It avoids buffering ahead so subsequent readers on r
// (such as init's own conversation) do not lose unread input.
func readByteAnswer(r io.Reader) (string, error) {
	var buf []byte
	var b [1]byte
	for {
		n, err := r.Read(b[:])
		if err != nil {
			if len(buf) > 0 && errors.Is(err, io.EOF) {
				return strings.ToLower(strings.TrimSpace(string(buf))), nil
			}
			return "", err
		}
		if n == 0 {
			continue
		}
		if b[0] == '\n' || b[0] == '\r' {
			break
		}
		buf = append(buf, b[0])
	}
	return strings.ToLower(strings.TrimSpace(string(buf))), nil
}

// announce shows the startup warnings and waits for the operator to
// acknowledge them. The refusal returns an error and never opens the screen,
// so the operator reads it; a warning printed on the way up would not be read
// at all — the TUI takes the alternate screen buffer a moment later and the
// text is gone until they quit. So the run still starts, as the warning is
// not a refusal, but it starts when the operator has seen the warning.
//
// Every launch, for as long as the board and the config disagree. There is
// nowhere to remember that an operator has already decided to live with a
// warning: SCOPE invariant 1 keeps durable state in Linear, and lerp's own
// board is not the place to record an opinion about the board. A keystroke
// per launch is the price of a warning that is read, and the warning ends the
// moment either side of the disagreement is fixed.
//
// It waits only when visible says the warnings went somewhere the operator is
// looking — a terminal. Redirected away with `lerp 2>/dev/null`, the prompt
// lands nowhere, and waiting for an answer to a question nobody was asked
// would hang the launch behind a blank screen.
//
// The acknowledgement is read a byte at a time rather than through a buffered
// reader: whatever they type after the newline belongs to the TUI, and a
// buffered read would swallow it.
func announce(w io.Writer, r io.Reader, warnings []string, visible bool) {
	if len(warnings) == 0 {
		return
	}
	for _, line := range warnings {
		// Write errors are dropped on purpose. A warning is not a refusal,
		// and `lerp 2>&-` must not be the difference between a run and no
		// run — the same reasoning as the unreadable stdin below.
		fmt.Fprintln(w, line)
	}
	if !visible {
		return
	}
	fmt.Fprint(w, "\npress enter to start anyway ")
	var b [1]byte
	for {
		n, err := r.Read(b[:])
		if err != nil {
			return
		}
		// Both line endings: a terminal that is not translating carriage
		// returns delivers enter as \r, and a gate that only knows \n would
		// swallow every keystroke and never open.
		if n == 1 && (b[0] == '\n' || b[0] == '\r') {
			return
		}
	}
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

const initHint = `run "lerp init"`

type notSetUpError struct {
	dir string
}

func (e notSetUpError) Error() string {
	return fmt.Sprintf("no repo config in %s or any parent directory: %s", e.dir, initHint)
}

func greeting(w io.Writer, dir string) {
	fmt.Fprintf(w, "lerp %s\n", version.Version)
	fmt.Fprintln(w, "Put tickets on a Linear board; lerp runs coding agents to move them across it.")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "No %s in %s or any parent directory.\n", config.RepoConfigFile, dir)
	fmt.Fprintln(w, `Run "lerp init" to pick a team and set this repo up.`)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Docs: https://lerp.sh/latest/docs/quickstart/")
}

// anchorFrom walks up from start looking for a repo config file, returning
// the first directory containing one. A match must be a regular file, so a
// directory named lerp.toml (or another config name) is ignored.
func anchorFrom(start string) (string, error) {
	dir := start
	for {
		_, err := config.FindRepoConfig(dir)
		if err == nil {
			if resolved, err := filepath.EvalSymlinks(dir); err == nil {
				return resolved, nil
			}
			return dir, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", notSetUpError{dir: start}
		}
		dir = parent
	}
}

func anchorDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return anchorFrom(cwd)
}

func initAnchorFrom(start string) string {
	if anchor, err := anchorFrom(start); err == nil {
		return anchor
	}
	if resolved, err := filepath.EvalSymlinks(start); err == nil {
		return resolved
	}
	return start
}

func initAnchorDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return initAnchorFrom(cwd), nil
}

// loadRepo reads and validates the repository configuration. When no repo
// config is found, it points at init rather than surfacing the raw fs error.
func loadRepo(repoDir string) (*config.RepoConfig, error) {
	path, err := config.FindRepoConfig(repoDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("no repo config: %s", initHint)
		}
		return nil, err
	}
	return config.LoadRepoConfig(path)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
