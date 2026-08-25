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

This creates the Linear team if it is missing and, on a first run,
holds a short conversation to fit the
[stock planning → approval → implementing pipeline](#stock-pipeline)
onto the board the team already has: it shows the team's existing
statuses, asks whether to include the optional planning stage and the
review pass, and offers to create the stock statuses the chosen
pipeline references — or, on customize, to map each one onto a status
you already have. Existing statuses are never modified, and init says
which statuses it creates and which it reuses before it acts. It then
writes `lerp.toml` at the repository root, uncommitted, for you to
review and check in.

The conversation's last question is whether the stock Claude runner
should include `--permission-mode bypassPermissions`. The default is no
— saying yes is a real grant (see [Stock pipeline](#stock-pipeline)),
and the diff you commit is where that decision gets reviewed. Declining
has a cost too: a headless run then fails at the first tool the agent
is not allowed to use, unless you curate an `--allowedTools` list —
also under [Stock pipeline](#stock-pipeline). Piped input or `--yes`
skips the conversation and takes the stock answers: the full pipeline,
stock status names, no bypass grant.

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

**3. Promote a ticket.** Routing is done by placing a ticket: in Linear,
move one into Planning (a big feature) or straight into Implementing (a
small fix). A ticket is eligible for pickup when it sits in a queue's
status, has no assignee, and is not blocked by an unfinished ticket
(Linear's `blockedBy`). Promotion is a human act; lerp never invents
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

Where a finished run leaves the ticket is the whole of the topology.
Coming to rest in a status some queue serves releases the claim — the
assignment is the claim, and a claimed ticket is someone else's work
as far as lerp is concerned, even when the someone is you — so the
next pass picks the ticket up for that stage on its own. Coming to
rest in a status no queue serves keeps it assigned to you on purpose:
that is the pipeline waiting on a human, and the stock config waits
twice, in "Plan Review" for you to read the plan and in "In Review"
for you to merge the PR. Promote it with `p` in the TUI, or move it in
Linear; either way the loop carries it on from there. Two things call
the whole rule off: a ticket an agent or a human moved out of the
queue's status during the run keeps that move — the hop it skipped is
reported rather than forced — and a ticket assigned to somebody else by
the time the run ends is theirs entirely, hop and claim both, since
taking over a run mid-flight is exactly the way to say so.

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
approval → implementing pipeline, with prompts you can read and argue
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
- **Name statuses by placeholder, not by name.** A prompt may also use
  `{{status}}`, `{{on_success}}`, and `{{on_failure}}`, expanded from
  its own queue's fields — no other queue's, and nothing more. Prose
  like "move {{ticket}} to {{on_failure}}" then follows a status rename
  or remap, where a literal name would silently point agents at a
  status that no longer exists. Referencing `{{on_failure}}` in a queue
  that does not set `on_failure` is a config error.

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
disposing the workspace, then settling the ticket exactly as the process
that started the run would have: a run that recorded its own exit status
takes its queue's hop, and one that never got that far has its claim
released instead, when the board still looks exactly as the dead run
left it — and starts eligible tickets as capacity frees. `-concurrency N` caps how many agents run at once
(default 5; each lane is a whole workspace, so lower it in a repo whose
`provision` command is heavy). The TUI needs a terminal: in a pipe or a
script, `lerp` prints usage and exits 2 rather than quietly starting to
claim tickets.

The Work view is one list of what the machine is doing with the
board, grouped by queue: every ticket sitting in each queue's status,
in the loop's own pickup order, with the ones running now at the top
of their own group. A running row carries its state — provisioning,
running, or adopted — and the run's elapsed time (an adopted run shows
its true age, not the moment it was adopted). Under it, once the run
has a log, a second line reads how that run is going: how long since
the log last said anything, and a sparkline of the agent's recent
activity, so a run that has fallen quiet reads as a flat line. Those
are numbers to read, not a timeout — lerp sets no threshold on them and
never acts on one; ejecting a run that has stopped making progress
stays the operator's call. A waiting row is shown faint with the reason
it waits, blocked or claimed. The panel title
and the status bar carry the capacity, `2/3 running`, which is what
says whether anything can start. Selecting a running ticket shows a
live tail of its log in the main pane, with scrollback that survives
the run's exit; selecting a waiting one shows where it sits in pickup
order and what gates it. The tail reads as agent activity rather than
as bytes: tool calls one line each, prose as prose, and thinking
collapsed to a single line with its token count. A runner whose output
lerp does not recognize is shown exactly as it was written, with no
configuration, and `r` toggles the pane back to the runner's raw
output — the log on disk is untouched either way. The list is
read-only; to change what runs next, move tickets in Linear. The
Inbox view lists what waits on a
human: unclaimed tickets, and the operator's own claimed tickets,
sitting in a status no queue serves. It is a table, one row per
ticket, carrying the identifier, the leverage, the title, the real
Linear status, the project and the priority — the vocabulary is
Linear's own, never a category invented by lerp. A status the
configured pipeline never names — neither a queue's status nor any
`on_success` or `on_failure` target — is marked, because that is the
fingerprint of a ticket that left the pipeline. Rows are grouped by
status by default, in an order derived from the pipeline itself —
where runs fail, then where they finish, then the statuses it never
names — so the run to retriage and the review to read are the top
rows rather than two of sixteen. Within a group, rows fall through to
leverage: how many other listed tickets promoting this one would
transitively unblock, then priority, then identifier — so the promote
worth making is the top row of its group. `s` cycles that to project,
leverage or priority; the two grouping modes draw a header per
boundary — none, when every row is in the same group — and the two flat
ones order the whole list. `P`
scopes the panel to one project and cycles back to all. Both are
session-only: no saved views, no filter syntax, and neither changes
which tickets are fetched. Selecting a row
reads the ticket itself into the main pane: its body — where the plan
lives — and the comments on it, the verdict a run left behind, so a
parked ticket can be decided from that one screen. That is a read
and stays one: nothing composes, replies, or navigates on to another
ticket, and `o` opens the ticket in Linear for everything else. Select
one and press `p` to promote it: pick a target from the configured
queue statuses or a pipeline exit, and lerp moves it there. That
MoveIssue is the only write any view makes; everything else about a
ticket still happens in Linear. Keys: `1`/`2` choose a panel and
`tab` cycles. `↑`/`↓` pick a row, `s` sorts
the Inbox and `P` scopes it to a project, `o` opens the selected ticket
in Linear, `pgup`/`pgdn` scroll the log or the ticket, `end` resumes
following, `r` shows the raw log, `q` quits (or backs out of the
promote picker).

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
| Backlog / Todo | you | promote a ticket into Planning, or into Implementing if it is small |
| Planning | agent | writes the plan into the ticket's description, under `## Plan` → Plan Review |
| Plan Review | you | read the plan — it is the top of the ticket — edit it where you disagree, then press `p` to promote: Implementing to build it, Planning to re-plan, Needs Attention to park it |
| Implementing | agent | reads its brief — unanswered PR review threads, else the newest comment asking for changes, else the ticket — commits, pushes, opens a draft PR with `gh` or adds to the ticket's existing one, then reviews and fixes its own work until a round is clean or three rounds are up; ends by leaving a verdict comment on the ticket and then marking the PR ready → In Review, or moves itself → Needs Attention |
| In Review | you | merge the PR; Linear's GitHub integration moves it to Done |
| Needs Attention | you | where failed runs and reviews that three rounds could not settle park; no queue watches it, so nothing retries it — promote back to Implementing to rework |

Which ticket enters where is the only routing decision, and it is made by
moving a ticket, not by configuration.

There is deliberately no queue for reviewing. A hop on the board is a
decision somebody makes, and iteration is not a decision: a review stage of
its own turns review-and-fix into a cycle nothing can bound, since counting
the rounds would mean state outside Linear or an `if` about your process. So
Implementing reviews and fixes its own work inside one run — findings go on
the pull request, as comments on the lines they concern, and the round count
is the agent's own context, which costs the board nothing. One short verdict
comment on the ticket says how it went.

That comment is also what makes a skipped review visible. The hop out of
Implementing keys on the run's exit code and the ticket's status, never on a
pull request — so an agent that implements, opens the draft and stops
still lands its ticket in In Review looking finished. The prompt answers that
by leading with the contract rather than trailing it: a run ends either with
a verdict on the ticket and the PR marked ready, or in Needs Attention saying
what stopped it and the PR never marked ready. Nothing enforces that
mechanically, but the verdict says how the review went — rounds run and what
the last one found — which is what keeps it from reading the same whether the
review happened or not, and the board reads a ticket's comments into the main
pane. A ticket resting in In Review with no verdict on it reads as unfinished
at a glance, without opening GitHub to find out. The comment goes on before
the PR is marked ready, since that flip is what frees a PR automation to move
the ticket.

What reaches Needs Attention is only what three rounds could not settle, and
that is a loop rather than the end of the line: say what you want on the pull
request, promote the ticket back to Implementing, and the next run reads your
unanswered threads as its brief, checks out the branch of the pull request
that already exists, and adds commits to it — no second PR. Lerp remembers
none of this. There is no "this is a re-run" flag anywhere, only a ticket in a
queue status, with its description, its comments and its pull request as the
state.

Two things the stock config assumes, both worth a deliberate look before you
run it:

- **The agent needs its own Linear access.** Lerp passes the ticket
  identifier and nothing else — never the ticket body — and every durable
  artifact — the plan in the ticket, the pull request, the verdict comment —
  is written by the agent, not by lerp. For Claude Code that means the Linear MCP server from step 2
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
running behavior while it is open (see [Running](#running)). Both
panels are built, and the Inbox's promote is the TUI's one write
action. `lerp once` is the single-shot alternative: one ticket through
its queue, no loop, no evidence store. Beyond those, `lerp version`
and `lerp init` complete the surface.

**Where does state live?** In Linear — that is the first sentence of
the model, and [SCOPE.md](SCOPE.md) invariant 1 holds it. Locally lerp
keeps exactly two things: `lerp.toml` (config, checked in) and an
evidence store, `.lerp/` at the repo root — one record per run under
`.lerp/runs` (pid, log file, ticket, workspace path, and the exit status
the run records for itself as it ends), workspaces under
`.lerp/workspaces`, an advisory lock at `.lerp/lock` that keeps it to
one loop per clone, and the loop's diagnostics in `.lerp/loop.log`.
Local state is evidence, never truth: losing all of it may cost
compute, never correctness. `lerp once` predates the store and keeps
its workspace and log under a temporary directory instead.

**What happens on crash or kill?** Every queue run is safe to kill and
restart from its beginning: progress is checkpointed only at queue
boundaries, as artifacts in Linear — the plan in the ticket, a PR link — so
the worst case is a re-run stage, never a lost ticket
([SCOPE.md](SCOPE.md) invariants 3 and 4 carry the full argument).
That includes killing lerp itself: agents outlive it, and the next
`lerp` adopts the live ones and reaps the dead. An adopted run that
reaches its own last line records its exit status beside its log, so
reaping it applies the queue's move rule — restarting lerp across a
run's finish costs nothing, rather than a whole re-run stage. A run
killed before it got that far records nothing, and reaping it releases
the claim so its ticket becomes eligible again; a failed run whose queue
has no `on_failure` route keeps its claim and waits on you, as it does
when lerp watched it fail. One caveat: a `lerp once` killed mid-run has
no evidence for a later loop to reap, so it leaves the ticket assigned —
unassign it in Linear yourself to make it eligible again.

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
human still acts on tickets there — the stock pipeline's "Plan Review",
where you approve a plan, and "In Review", where you merge the PR and
Linear's GitHub integration moves the ticket on — in-progress is
exactly right, and marking it completed would release dependent
tickets before the work had actually landed.

**How does multiplayer work?** It is inherited from Linear, not built:
each developer runs their own lerp against their own clone, and the
claim protocol of [SCOPE.md](SCOPE.md) invariant 4 — assign to self,
settle, read back — arbitrates across machines. The board reads like a
human team's board: "Sarah has LERP-42 in Implementing" is true
whether Sarah or Sarah's agent is doing the work. No server, no
scheduler, no work stealing.
