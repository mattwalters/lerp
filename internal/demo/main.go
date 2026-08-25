// Command demo renders lerp's README cast. It is the real TUI over the real
// reconciler: only the two things that would cost money or vary are faked —
// Linear (linear.Fake, already a non-test Client) and the coding agent (a
// shell stub replaying a checked-in log). So the recording is of lerp, not of
// a mock of lerp, and a change to the loop or the panes that breaks the tape
// breaks it loudly.
//
// It lives under internal/ rather than cmd/ deliberately: `go build ./...`
// and `go vet ./...` compile it, so `make check` already catches TUI and loop
// API drift, while `go install ./cmd/lerp` never ships it. Nothing in lerp's
// own binary may import it.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/evidence"
	"github.com/mattwalters/lerp/internal/linear"
	"github.com/mattwalters/lerp/internal/loop"
	"github.com/mattwalters/lerp/internal/tui"
)

// The demo pipeline and its stub agent ship as files, not as literals: the
// harness writes them into a throwaway root and reads the config back through
// config.LoadRepoConfig, exactly as lerp does for a real repo.
var (
	//go:embed board.toml
	boardTOML string
	//go:embed agent.sh
	agentScript string
	//go:embed agent.log
	agentFixture string
)

const (
	// demoTeam is the one Linear team on the fake board; board.toml serves it.
	demoTeam = "DEMO"
	// dirEnv carries the throwaway root to the stub agent. board.toml and
	// agent.sh both reach their fixture through it, which is what lets the
	// config file be checked in byte-for-byte as the file that is loaded.
	dirEnv = "LERP_DEMO_DIR"
	// lanes is small enough that a reader can follow one of them across a
	// 1000-pixel-wide GIF, and large enough that tickets visibly queue behind
	// the running ones.
	lanes = 3
	// interval is far below loop.DefaultInterval's 12s so that several passes
	// land inside the cast's runtime: the board updating is one of the beats.
	interval = 2 * time.Second
)

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "lerp demo:", err)
		os.Exit(1)
	}
}

// run wires the same reconciler and TUI that cmd/lerp's openTUI wires, over a
// throwaway root that is both the repo dir and the evidence dir. Nothing here
// reads LINEAR_API_KEY, constructs linear.New, or shells out to git.
func run(ctx context.Context) error {
	root, err := os.MkdirTemp("", "lerp-demo-")
	if err != nil {
		return fmt.Errorf("create demo root: %w", err)
	}
	// The root holds the config, the stub agent, and every lane's run
	// evidence and workspace, so removing it is the whole of the cleanup.
	defer os.RemoveAll(root)

	if err := os.Setenv(dirEnv, root); err != nil {
		return fmt.Errorf("export %s: %w", dirEnv, err)
	}
	files := []struct {
		name string
		body string
		mode os.FileMode
	}{
		{config.RepoConfigFile, boardTOML, 0o600},
		{"agent.sh", agentScript, 0o700},
		{"agent.log", agentFixture, 0o600},
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(root, f.name), []byte(f.body), f.mode); err != nil {
			return fmt.Errorf("write %s: %w", f.name, err)
		}
	}

	repo, err := config.LoadRepoConfig(filepath.Join(root, config.RepoConfigFile))
	if err != nil {
		return err
	}
	client := seedBoard()
	// The same refusal openTUI makes, and here it doubles as a rot guard: a
	// status renamed in board.toml but not on the seeded board fails the
	// render instead of recording an empty queue.
	if err := loop.VerifyStatuses(ctx, client, repo); err != nil {
		return err
	}

	ev := evidence.New(root)
	lock, err := ev.AcquireLock()
	if err != nil {
		return err
	}
	defer lock.Close()

	events := make(chan loop.Event, 64)
	rec, err := loop.NewReconciler(loop.ReconcilerOptions{
		Client:   client,
		Repo:     repo,
		RepoDir:  root,
		Evidence: ev,
		Lanes:    lanes,
		Interval: interval,
		Events:   func(e loop.Event) { events <- e },
		// No Log: provision, dispose and runner diagnostics are process
		// detail, and the cast records a screen, not a file.
	})
	if err != nil {
		return err
	}
	return tui.Run(ctx, tui.Options{
		Ticker:   rec,
		Promoter: rec,
		Reader:   rec,
		Statuses: repo.PromoteTargets(),
		Interval: interval,
		Lanes:    lanes,
		Events:   events,
	})
}

// boardStates are the DEMO team's workflow states in board order. The
// pipeline in board.toml maps onto a subset of them; the rest exist so the
// inbox has statuses the pipeline never names to mark as such.
var boardStates = []string{
	"Triage", "Backlog", "Planning", "Plan Review",
	"Implementing", "In Review", "Needs Attention", "Done", "Canceled",
}

// ticket is one seeded issue. It is the whole database: the fake board holds
// nothing lerp does not read.
type ticket struct {
	id        string // human identifier; the fake needs no separate UUID
	title     string
	status    string
	project   string
	priority  int    // Linear's own scale: 1 urgent … 4 low, 0 none
	blockedBy string // identifier of the ticket holding this one up
	body      string // what the inbox pane renders in the main pane
}

// board is what a stranger meets in the first frame: two queues with more
// work in them than lanes to run it, and an inbox of tickets resting in
// statuses no queue serves. Identifiers are spread out rather than
// consecutive, because a real board's are.
var board = []ticket{
	// The implement queue. Six eligible tickets for three lanes, so the cast
	// shows lanes turning over, plus one blocked ticket that never runs.
	{id: "DEMO-31", title: "Cache the parsed repo config", status: "Implementing", project: "v0.4", priority: 3},
	{id: "DEMO-30", title: "Retry a status read once before failing the pass", status: "Implementing", project: "v0.4", priority: 2},
	{id: "DEMO-29", title: "Name the lane in every loop log line", status: "Implementing", project: "v0.4", priority: 3},
	{id: "DEMO-27", title: "Trim trailing whitespace from prompt templates", status: "Implementing", priority: 4},
	{id: "DEMO-26", title: "Reap a run whose workspace is already gone", status: "Implementing", project: "v0.4", priority: 2},
	{id: "DEMO-24", title: "Widen the queue table on narrow terminals", status: "Implementing", project: "Board polish", priority: 3},
	{id: "DEMO-22", title: "Publish the v0.4 milestone", status: "Implementing", project: "v0.4", priority: 3, blockedBy: "DEMO-34"},

	// The plan queue, picked up once the implement queue drains.
	{id: "DEMO-33", title: "Multi-repo: one lerp, several clones", status: "Planning", project: "v0.5", priority: 2},
	{id: "DEMO-32", title: "A Codex runner adapter", status: "Planning", project: "v0.5", priority: 3},

	// The inbox: statuses no queue serves, which is what puts them in front
	// of a human.
	{id: "DEMO-34", title: "Log tail drops its first line after adoption", status: "Needs Attention",
		project: "v0.4", priority: 1,
		body: "The tail skips to the next newline when it attaches mid-line, and\non adoption it attaches at offset zero — where there is no partial\nline to skip.\n\n## Plan\n\nSkip only when the attach offset is past the start of the file."},
	{id: "DEMO-28", title: "Release notes for v0.3", status: "In Review", project: "v0.4", priority: 3,
		blockedBy: "DEMO-34",
		body:      "Draft the notes from the merged pull requests since v0.2.\n\nBlocked until the tail bug is settled — it changes what the log\npane section has to say."},
	{id: "DEMO-25", title: "Document the runner contract", status: "Plan Review", project: "Docs", priority: 2,
		body: "## Plan\n\nOne page under `docs/`: what a runner is handed (a prompt, a working\ndirectory), what lerp reads back (an exit code), and the three\nplaceholders a command template may use."},
	{id: "DEMO-23", title: "Windows support for provision and dispose", status: "Backlog", project: "v0.5", priority: 4,
		body: "Both commands go through `sh -c`. Deciding what the Windows story is\ncomes before writing any of it."},
	{id: "DEMO-21", title: "Sanitize Linear titles before rendering", status: "In Review", project: "Board polish", priority: 2,
		body: "A ticket title carrying an escape sequence must not reach the\nterminal as one."},
	{id: "DEMO-18", title: "Decide how eject hands over a session", status: "Triage", priority: 0,
		body: "Nobody has triaged this yet — the pipeline never names \"Triage\", so\nthe board says so on the row."},
}

// seedBoard builds the fake Linear the cast runs against.
func seedBoard() *linear.Fake {
	fake := linear.NewFake()
	fake.SetTeamStates(demoTeam, boardStates...)
	for _, t := range board {
		fake.AddIssue(demoTeam, linear.Issue{
			ID:         t.id,
			Identifier: t.id,
			Title:      t.title,
			Status:     t.status,
			Project:    t.project,
			Priority:   t.priority,
			URL:        "https://linear.app/demo/issue/" + t.id,
		})
		if t.body != "" {
			fake.SetDescription(t.id, t.body)
		}
	}
	// Declared after every issue exists: blocking is a relation between two
	// of them, and the fake computes Blocked from the blocker's own status.
	for _, t := range board {
		if t.blockedBy != "" {
			fake.Block(t.id, t.blockedBy)
		}
	}
	return fake
}
