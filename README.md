# lerp

[![ci](https://github.com/mattwalters/lerp/actions/workflows/ci.yml/badge.svg)](https://github.com/mattwalters/lerp/actions/workflows/ci.yml)

Lerp is a small, reliable CLI, written in Go, that orchestrates
software work through Linear. You put tickets on a board; lerp runs
coding agents to move them across it.

The mental model is three sentences. **Linear is the database**: all
durable state — what work exists, what stage it is in, who has claimed
it, what was decided — lives in Linear, and lerp keeps no store of its
own. **The board is the workflow**: a queue is a Linear status with a
prompt, a runner, and a status to move to on success; workflow topology
exists only in where tickets sit and where `on_success` points, never
in config syntax. **Lerp is a reconciler**: desired state is the board,
actual state is the agent processes running on this machine, and the
loop starts, adopts, or reaps agents until the two match — a crash is
not an error case, it is drift, and the loop repairs drift.

The whole ontology is five concepts — ticket, queue, runner, lane, the
loop — where a lane is the concurrency unit: lerp runs at most N
agents at once, one per lane. [SCOPE.md](SCOPE.md) is the fence around
all five: nine invariants and the litmus tests every change runs
through. Read it before proposing anything.

## Install

```sh
go install github.com/mattwalters/lerp/cmd/lerp@latest
```

Or from a clone:

```sh
git clone https://github.com/mattwalters/lerp
cd lerp
make install
```

`make install` builds with the version stamped from `git describe` and
installs into Go's bin dir — `GOBIN` when set, else `GOPATH/bin`;
`lerp version` prints what you got. `make check` runs exactly what CI
runs: gofmt, vet, build, test.

## Getting started

Lerp speaks exactly one external API: Linear. Every command reads the
API key from the `LINEAR_API_KEY` environment variable — create a
personal API key in Linear's settings and export it. Lerp itself needs
nothing else beyond Git, but the stock pipeline shells out to `claude`
([Claude Code](https://docs.claude.com/en/docs/claude-code)) as its
runner and its implementing prompt opens PRs with `gh` — install both
before step 4.

**1. Wire the repo to a team.** From anywhere inside your Git
repository:

```sh
LINEAR_API_KEY=... lerp init --team LERP --team-name "Lerp"
```

`--team` is the Linear team key — the ticket prefix, the LERP in
LERP-42 — and `--team-name` is the display name used only if the team
must be created. Check the key against Linear before you run: a key
that matches no existing team exactly is not an error, it quietly
creates a new team under that key.

This creates the Linear team if it is missing, adds the statuses your
config's queues name — the stock pipeline's, on a first run — and
writes `lerp.toml` — the
[stock planning → implementing → review pipeline](#stock-pipeline) —
at the repository root, uncommitted, for you to review and check in.
When it writes a new file, init asks one question: whether the stock
Claude runner should include `--permission-mode bypassPermissions`.
The default is no — saying yes is a real grant (see
[Stock pipeline](#stock-pipeline)), and the diff you commit is where
that decision gets reviewed. Declining has a cost too: a headless run
then fails at the first tool the agent is not allowed to use, unless
you curate an `--allowedTools` list — also under
[Stock pipeline](#stock-pipeline).

`lerp init` is safe to repeat: it creates only missing Linear
structure and never replaces an existing `lerp.toml` — it verifies
that the existing config serves the requested team, and ensures the
statuses that config's queues name, instead.

Init may also print a report about where your pipeline ends — statuses
Linear does not yet count as completing work. Act on what it prints;
the reasoning is under
[How it behaves](#how-it-behaves).

**2. Give the agent what it needs.** Lerp hands the runner a prompt and
the ticket identifier, nothing more. The stock prompts expect the agent
to read the ticket and write stage artifacts itself, so Claude Code
needs Linear's MCP server. See
[Linear's MCP documentation](https://linear.app/docs/mcp) for the
authoritative endpoint; typically:

```sh
claude mcp add --transport http linear https://mcp.linear.app/mcp
```

Then start `claude` and run `/mcp` to complete the interactive OAuth
flow — that is a one-time step only you can do, and it must happen
before any unattended run can reach Linear. What the agent is
permitted to do beyond reading tickets is the other half of setup,
spelled out under [Stock pipeline](#stock-pipeline); read `lerp.toml`
before you run it.

**3. Bless a ticket.** Routing is done by placing a ticket: in Linear,
move one into Planning (a big feature) or straight into Implementing (a
small fix). A ticket is eligible for pickup when it sits in a queue's
status, has no assignee, and is not blocked by an unfinished ticket
(Linear's `blockedBy`). Blessing is a human act; lerp never invents
work items of its own.

**4. Run it.**

```sh
LINEAR_API_KEY=... lerp
```

Bare `lerp` opens the TUI, and the loop runs while it is open: each
pass claims eligible tickets — assigning one to your Linear user is
the claim — provisions a disposable workspace per lane (the stock
config uses a git worktree), runs the queue's agent to exit, and
applies the queue's move rule: `on_success` on a clean exit,
`on_failure` otherwise, and only if the agent didn't move the ticket
itself. [Running](#running) describes the interface.

For scripts, or to watch a single run at ground level, `lerp once`
runs one eligible ticket through the same claim → provision → run →
move sequence and exits. It predates the loop's evidence store: its
workspace and agent log live under a temporary directory instead, and
the log's path is printed when the run finishes.

A finished run leaves the ticket assigned to you. The assignment is
the claim, and a claimed ticket is someone else's work as far as lerp
is concerned — even when the someone is you. So to carry the ticket
through its next stage: unassign it in Linear. The loop picks it up
again on its next pass; with `lerp once`, run the command again.

## Configuration

Lerp reads one file: `lerp.toml` at the repo root, **checked in**. It
declares which Linear teams the repo serves, how to build a workspace,
and the pipeline itself — runners and queues, prompts included. The
pipeline is a team artifact: keeping it in the repo means the
permissions it grants are versioned and reviewed like code, and every
developer runs the same pipeline against the same board.

No durable state lives in this file or anywhere else on disk — Linear
is the database (see the model above). The file is strictly parsed: an
unknown key is an error, not a shrug.

[lerp.example.toml](lerp.example.toml) is the stock planning →
implementing → review pipeline, with prompts you can read and argue
with. `lerp init --team KEY` writes it into the repo for you (see
[Getting started](#getting-started)).

Read it before you run it. It may grant the agent broad permissions and it
assumes your runner can reach Linear; both are explained in the file and
under [Stock pipeline](#stock-pipeline) below.

```toml
# Linear team keys this repo serves. One repo may serve several teams
# (the monorepo case); two repos may never claim the same team.
teams = ["LERP"]

# Commands that create and destroy a lane's workspace. Lerp invokes
# them with a unique lane/run identity and otherwise knows nothing
# about what they do. Environment isolation — ports, databases,
# containers — is this repo's problem, solved here.
provision = "scripts/lerp-provision"
dispose = "scripts/lerp-dispose"

# A runner is an adapter to a coding-agent CLI. The contract is the
# lowest common denominator: takes a prompt and a working directory,
# runs to exit, exit code means done or failed.
[runners.claude]
command = "claude -p"
# Optional. Handed to you on eject so a headless run becomes your
# interactive session.
resume = "claude --resume"

# A queue is a Linear status with instructions attached. Tickets
# sitting in `status` are picked up, run through `runner` with
# `prompt`, and moved to `on_success` on a clean exit — unless the
# agent already moved the ticket itself, which lerp respects.
[queues.plan]
status = "Planning"
prompt = "Read {{ticket}} and post a plan as a comment."
runner = "claude"
on_success = "Implementing"

[queues.implement]
status = "Implementing"
prompt = "Implement {{ticket}} per its plan comment. Open a PR."
runner = "claude"
on_success = "Reviewing"
# Optional. Where the ticket goes when the agent exits non-zero.
on_failure = "Human Review"
```

The queue fields above are the complete set — status, prompt, runner,
`on_success`, and optionally `on_failure`. There is deliberately no
conditional, template, or DAG syntax: topology lives on the board (see
the model above), and branching is a human or an agent moving a ticket.

Notes:

- `on_success` / `on_failure` name Linear statuses, not queues. They
  may point at a status no queue watches (a human review column) —
  that is how work exits the automated path.
- A status may drive at most one queue; two queues sharing a `status`
  is a config error.
- Every queue's `runner` must be defined under `[runners]`.
- `command` is run by `sh -c`. Use `{{prompt}}`, `{{ticket}}` and
  `{{workdir}}` to insert the queue prompt, the ticket identifier and
  the workspace directory; lerp shell-quotes every value, so nothing in
  a ticket can alter the command you configured. If the runner accepts
  a caller-chosen session ID (for example, Claude Code's
  `--session-id`), include `{{session}}` in its command. Lerp records
  that generated ID with the run for a later eject/resume action.
- **Name the ticket in your prompt.** `{{ticket}}` is expanded inside
  the prompt as well as the command, and the identifier reaches the
  runner as `LERP_TICKET`. A prompt is shared by every ticket in its
  queue, so one that never names the ticket leaves the agent no way to
  know which ticket it was started for — while lerp will still advance
  that ticket on a clean exit. Write `prompt = "Implement {{ticket}}
  ..."`, not `prompt = "Implement the ticket ..."`.

Every ticket must resolve to exactly one working directory: one repo
may serve several teams, but two repos may never claim the same team.
A single repo config can only vouch for its own repo, and today lerp
does not verify the cross-repo half of that rule — keeping it is your
job when you copy a pipeline between repos.

### Workspace commands

Lerp runs `provision` before starting a runner and `dispose` when reaping
its lane. Each command runs from the repo root, writes both standard output
and standard error to the lane log, and receives these environment variables:

- `LERP_LANE` — the lane number
- `LERP_TICKET_ID` — the Linear issue ID
- `LERP_WORKSPACE` — the workspace path

If provisioning fails, lerp leaves the ticket untouched and does not start
the runner. A disposal failure is recorded in the lane log but never keeps a
lane occupied.

## Running

```sh
LINEAR_API_KEY=... lerp
```

`lerp` opens the TUI, and the TUI is the engine: the loop runs while it
is open, and there is no daemon. Each pass reads the run evidence on
disk, adopts live agents a previous lerp left behind, reaps dead ones —
disposing the workspace and releasing the claim, when the board still
looks exactly as the dead run left it — and starts eligible tickets
into free lanes. `-lanes N` caps how many agents run at once (default
3). The TUI needs a terminal: in a pipe or a script, `lerp` prints
usage and exits 2 rather than quietly starting to claim tickets.

The Board view shows one row per lane — ticket, queue, and runner
state: provisioning, running, or adopted, each with the run's elapsed
time (an adopted run shows its true age, not the moment it was
adopted) — and a live tail of the selected lane's log, with
scrollback that survives the run's exit. The Queue view shows what
runs next: each configured queue with every ticket sitting in its
status, in the loop's own pickup order, refreshed on every pass —
eligible tickets run as lanes free up, and blocked or claimed ones are
shown faint with the reason. It is read-only; to change what runs
next, move tickets in Linear. Keys: `1`/`2`/`3` choose a view and
`tab` cycles — the Attention view is an empty shell until its own
ticket (LERP-13) lands. `↑`/`↓` pick a lane, `pgup`/`pgdn` scroll the
log, `end` resumes following, `q` quits.

Quitting (`q` or `ctrl+c`) closes the screen, stops future passes, and
waits briefly for a pass already in flight to settle. The agents are
never touched: they are their own processes, with run evidence on
disk, and the next `lerp` adopts them. An advisory lock keeps it to
one loop per clone, and the loop's own diagnostics — provision,
dispose, adopt, reap — append to `.lerp/loop.log`.

## Stock pipeline

[lerp.example.toml](lerp.example.toml) ships one opinion. Lerp holds
none: the order below exists only in the config's `on_success` pointers, and
rewriting them into a different shape needs no code change.

| Status | Who acts | Then |
| --- | --- | --- |
| Backlog / Todo | you | bless a ticket into Planning, or into Implementing if it is small |
| Planning | agent | posts a plan comment → Implementing |
| Implementing | agent | commits, pushes, opens a PR with `gh` → Agent Review |
| Agent Review | agent | posts a review verdict → In Review, or back to Implementing |
| In Review | you | merge the PR; Linear's GitHub integration moves it to Done |
| Needs Attention | you | where a failed run parks; no queue watches it, so nothing retries it |

Which ticket enters where is the only routing decision, and it is made by
moving a ticket, not by configuration.

Two things the stock config assumes, both worth a deliberate look before you
run it:

- **The agent needs its own Linear access.** Lerp passes the ticket
  identifier and nothing else — never the ticket body — and every durable
  artifact (the plan comment, the review verdict) is written by the agent,
  not by lerp. For Claude Code that means the Linear MCP server from step 2
  of [Getting started](#getting-started).
- **The agent runs with permissions — if you say so.** An unattended agent
  that cannot run `git`, `gh`, or your tests just fails, so the stock runner
  wants `--permission-mode bypassPermissions`; `lerp init` asks before
  including it, and defaults to leaving it out. Understand what a yes means:
  the agent runs with your full user account. The worktree that `provision`
  builds is a tidiness boundary, not a security one — nothing stops the
  agent from reading `~/.ssh` or writing outside its workspace. Because the
  grant lives in a checked-in `lerp.toml`, adding, reviewing, or narrowing
  it (for example with `--allowedTools`) is an ordinary code change.

## How it behaves

**What ships today?** The TUI is the way to run the loop: bare `lerp`
opens it, and the reconciling loop of the mental model — N lanes,
adopting live runs, reaping dead ones, repairing drift — is real,
running behavior while it is open (see [Running](#running)). Of the
TUI's three views only the Board is built so far. `lerp once` is the
single-shot alternative: one ticket through its queue, no loop, no
evidence store. Beyond those, `lerp version` and `lerp init` complete
the surface.

**Where does state live?** In Linear — that is the first sentence of
the model, and [SCOPE.md](SCOPE.md) invariant 1 holds it. Locally lerp
keeps exactly two things: `lerp.toml` (config, checked in) and an
evidence store, `.lerp/` at the repo root — one record per run under
`.lerp/runs` (pid, log file, ticket, workspace path), workspaces under
`.lerp/workspaces`, an advisory lock at `.lerp/lock` that keeps it to
one loop per clone, and the loop's diagnostics in `.lerp/loop.log`.
Local state is evidence, never truth: losing all of it may cost
compute, never correctness. `lerp once` predates the store and keeps
its workspace and log under a temporary directory instead.

**What happens on crash or kill?** Every queue run is safe to kill and
restart from its beginning: progress is checkpointed only at queue
boundaries, as artifacts in Linear — a plan comment, a PR link — so
the worst case is a re-run stage, never a lost ticket
([SCOPE.md](SCOPE.md) invariants 3 and 4 carry the full argument).
That includes killing lerp itself: agents outlive it, and the next
`lerp` adopts the live ones and reaps the dead — releasing a dead
run's claim so its ticket becomes eligible again. One caveat: a `lerp
once` killed mid-run has no evidence for a later loop to reap, so it
leaves the ticket assigned — unassign it in Linear yourself to make it
eligible again.

**Why isn't my ticket being picked up?** Check the three eligibility
conditions from step 3 of [Getting started](#getting-started): a
queue's status, no assignee, not blocked. The one that surprises
people is the assignee — the assignment is the claim, so a ticket
assigned to anyone, including you, is someone else's work as far as
lerp is concerned. A finished run leaves the ticket assigned to you on
purpose (see step 4).

**Why does init tell me to set a status's category myself?** Statuses
lerp creates are always in-progress (Linear's "started" category), and
statuses that already exist are left exactly as you have them. That
leaves one piece of setup only you can finish: where your pipeline
ends. Linear counts a ticket as blocking its dependents (`blockedBy`)
until its status carries a completed category — a fact about your
process that init cannot infer, so it reports instead of guessing. For
each `on_success` target no queue watches, init prints whether Linear
categorises it as completed. If such a status genuinely means the work
is done, set its category to Done in Linear; left in-progress,
finished tickets there would block their dependents forever. If a
human still acts on tickets there — the stock pipeline's "In Review",
where you merge the PR and Linear's GitHub integration moves the
ticket on — in-progress is exactly right, and marking it completed
would release dependent tickets before the work had actually landed.

**How does multiplayer work?** It is inherited from Linear, not built:
each developer runs their own lerp against their own clone, and the
claim protocol of [SCOPE.md](SCOPE.md) invariant 4 — assign to self,
settle, read back — arbitrates across machines. The board reads like a
human team's board: "Sarah has LERP-42 in Implementing" is true
whether Sarah or Sarah's agent is doing the work. No server, no
scheduler, no work stealing.
