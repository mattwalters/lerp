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
loop — and [SCOPE.md](SCOPE.md) is the fence around them: nine
invariants and the litmus tests every change runs through. Read it
before proposing anything.

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
personal API key in Linear's settings and export it.

**1. Wire the repo to a team.** From anywhere inside your Git
repository:

```sh
LINEAR_API_KEY=... lerp init --team LERP --team-name "Lerp"
```

This creates the Linear team if it is missing, adds the statuses the
stock pipeline names, and writes `lerp.toml` — the
[stock planning → implementing → review pipeline](#stock-pipeline) —
at the repository root, uncommitted, for you to review and check in.
When it writes a new file, init asks one question: whether the stock
Claude runner should include `--permission-mode bypassPermissions`.
The default is no — saying yes is a real grant (see
[Stock pipeline](#stock-pipeline)), and the diff you commit is where
that decision gets reviewed.

`lerp init` is safe to repeat: it creates only missing Linear structure
and never replaces an existing `lerp.toml` — it verifies that the
existing config serves the requested team instead.

Statuses lerp creates are always in-progress (Linear's "started"
category), and statuses that already exist are left exactly as you have
them. That leaves one piece of setup only you can finish: where your
pipeline ends. Linear counts a ticket as blocking its dependents
(`blockedBy`) until its status carries a completed category — a fact
about your process that init cannot infer, so it reports instead of
guessing. For each `on_success` target no queue watches, init prints
whether Linear categorises it as completed. If such a status genuinely
means the work is done, set its category to Done in Linear; left
in-progress, finished tickets there would block their dependents
forever. If a human still acts on tickets there — the stock pipeline's
"In Review", where you merge the PR and Linear's GitHub integration
moves the ticket on — in-progress is exactly right, and marking it
completed would release dependent tickets before the work had actually
landed.

**2. Give the agent what it needs.** Lerp hands the runner a prompt and
the ticket identifier, nothing more. The stock prompts expect the agent
to read the ticket and write stage artifacts itself, so Claude Code
needs its Linear MCP server configured — and unattended runs need
permissions you grant deliberately. Both are spelled out under
[Stock pipeline](#stock-pipeline); read `lerp.toml` before you run it.

**3. Bless a ticket.** Routing is done by placing a ticket: in Linear,
move one into Planning (a big feature) or straight into Implementing (a
small fix). A ticket is eligible for pickup when it sits in a queue's
status, has no assignee, and is not blocked by an unfinished ticket.
Blessing is a human act; lerp never invents work items of its own.

**4. Run it.**

```sh
LINEAR_API_KEY=... lerp once
```

`lerp once` runs one eligible ticket through its queue and exits:
it claims the ticket by assigning it to your Linear user, provisions a
disposable workspace (the stock config uses a git worktree), runs the
queue's agent to exit, and applies the queue's move rule — `on_success`
on a clean exit, `on_failure` otherwise, and only if the agent didn't
move the ticket itself. The agent's full stream goes to a log file,
whose path is printed when the run finishes; its workspace and log live
under a temporary directory. Run `lerp once` again to carry the ticket
through its next stage.

## Configuration

Lerp reads one file: `lerp.toml` at the repo root, **checked in**. It
declares which Linear teams the repo serves, how to build a workspace,
and the pipeline itself — runners and queues, prompts included. The
pipeline is a team artifact: keeping it in the repo means the
permissions it grants are versioned and reviewed like code, and every
developer runs the same pipeline against the same board.

Durable state lives in Linear, never in this file or anywhere else
on disk. The file is strictly parsed: an unknown key is an error,
not a shrug.

[lerp.example.toml](lerp.example.toml) is the stock planning →
implementing → review pipeline, with prompts you can read and argue with.
`lerp init --team KEY` writes it into the repo for you and never
overwrites an existing file.

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
conditional, template, or DAG syntax: workflow topology exists only in
where tickets sit and where `on_success` points. Branching is a human
or an agent moving a ticket.

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

Every ticket must resolve to exactly one working directory: team →
repo is a function (SCOPE invariant 2). A single repo config can only
vouch for its own repo, so the cross-repo half of that rule — no two
repos claim the same team — is yours to keep when you copy a pipeline
between repos.

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
  not by lerp. For Claude Code that means configuring its Linear MCP server.
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

**Where does state live?** Every durable fact — what work exists, what
stage it is in, who claimed it, what was decided — lives in Linear.
Locally there are exactly two things: `lerp.toml` (config, checked in)
and `.lerp/` at the repo root, which holds evidence of running
processes — one record per run under `.lerp/runs` (pid, log file,
ticket, workspace path), workspaces under `.lerp/workspaces`, and the
clone lock at `.lerp/lock`. Local state is evidence, never truth:
losing all of it may cost compute, never correctness. `rm -rf
.lerp/runs` under live agents means orphaned processes and re-run
stages — never lost or corrupted tickets.

**What happens on crash or kill?** Every queue run is safe to kill and
restart from its beginning. Progress is checkpointed only at queue
boundaries, as artifacts in Linear — a plan comment, a PR link, a
review verdict — and never inside a run. A crash of an agent, of lerp,
or of the laptop is drift, and the loop repairs drift: each pass reads
the run evidence, adopts runs whose process is still alive, and reaps
dead ones — the workspace is disposed and the dead run's claim
released, but only when the board still looks exactly as the run left
it. If the ticket has moved or changed hands since, a human, an agent,
or an automation acted, and the loop leaves their work alone. The worst
case is a re-run stage: duplicated compute, never a lost ticket. One
caveat while the interface is young: `lerp once` keeps its workspace
and log in a temporary directory rather than the evidence store, so if
you kill it mid-run, unassign the ticket in Linear yourself to make it
eligible again.

**Why isn't my ticket being picked up?** A ticket is eligible when it
sits in a status some queue watches, has no assignee, and is not
blocked by an unfinished ticket (Linear's `blockedBy`). The assignment
is the claim: a ticket assigned to anyone — including you — is someone
else's work as far as the loop is concerned.

**How does multiplayer work?** It is inherited from Linear, not built.
Each developer runs their own lerp against their own clone; an advisory
lock on `.lerp/lock` keeps it to one lerp per clone, and the claim
protocol arbitrates across machines — assign to self, settle, read
back; if the assignee is you, you won, otherwise walk away. The race
window that remains resolves to duplicated compute, which safe-to-kill
runs already tolerate. The board reads like a human team's board:
"Sarah has LERP-42 in Implementing" is true whether Sarah or Sarah's
agent is doing the work. No server, no scheduler, no work stealing.
