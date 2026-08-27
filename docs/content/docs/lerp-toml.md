---
title: lerp.toml
summary: The one file lerp reads — teams, workspace commands, runners, queues — and the stock pipeline it ships with.
weight: 120
---

# `lerp.toml`

Lerp reads one file: `lerp.toml` at the repo root, **checked in**. It
declares which Linear teams the repo serves, how to build a workspace, and
the pipeline itself — runners and queues, prompts included. The pipeline is a
team artifact: keeping it in the repo means the permissions it grants are
versioned and reviewed like code, and every developer runs the same pipeline
against the same board.

No durable state lives in this file or anywhere else on disk — [Linear is the
database](the-board.md#tickets-and-where-state-lives). The file is strictly
parsed: an unknown key is an error, not a shrug.

[lerp.example.toml](lerp.example.toml) is the stock pipeline in full, with
prompts you can read and argue with. `lerp init --team KEY` writes it into
the repo for you (see [Quickstart](quickstart.md)). Read it before you run
it: it may grant the agent broad permissions and it assumes your runner can
reach Linear, both of which are [below](#the-stock-pipeline).

## The shape of it

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
command = "claude -p {{prompt}} --session-id {{session}}"
# Optional. Handed to you on eject so a headless run becomes your
# interactive session. {{session}} is the id lerp generated for the
# run — so a command without {{session}} cannot be ejected either,
# however this line reads — and {{workdir}} is the workspace lerp
# leaves standing, which Claude Code needs to be in to find the
# session.
resume = "cd {{workdir}} && claude --resume {{session}}"

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
conditional, template, or DAG syntax: [topology lives on the
board](the-board.md#queues-and-why-there-is-no-workflow-syntax), and
branching is a human or an agent moving a ticket.

## Notes

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
  `--session-id`), include `{{session}}` in its command. Lerp generates
  that ID before the run starts and records it with the run, which is
  what makes the run [ejectable](ejecting.md) later — including by a
  `lerp` that did not start it. `resume` may use `{{session}}`,
  `{{ticket}}` and `{{workdir}}`, quoted the same way.
- **Name the ticket in your prompt.** `{{ticket}}` is expanded inside
  the prompt as well as the command, and the identifier reaches the
  runner as `LERP_TICKET`. A prompt is shared by every ticket in its
  queue, so one that never names the ticket leaves the agent no way to
  know which ticket it was started for — while lerp will still advance
  that ticket on a clean exit. Write `prompt = "Implement {{ticket}}
  ..."`, not `prompt = "Implement the ticket ..."`.
- **`context` is a display denominator, never a capability.** Set it on a
  runner to the model's context window in tokens (`context = 200000`) and
  the board turns a run's context reading into a percentage; leave it unset
  and the row shows tokens with no percentage. Lerp carries no
  model→window table — windows vary by model and by flags — so an unset
  `context` is the honest state, not a gap to fill in.
- **Name statuses by placeholder, not by name.** A prompt may also use
  `{{status}}`, `{{on_success}}`, and `{{on_failure}}`, expanded from
  its own queue's fields — no other queue's, and nothing more. Prose
  like "move {{ticket}} to {{on_failure}}" then follows a status rename
  or remap, where a literal name would silently point agents at a
  status that no longer exists. Referencing `{{on_failure}}` in a queue
  that does not set `on_failure` is a config error.

Every ticket must resolve to exactly one working directory: one repo may
serve several teams, but two repos may never claim the same team. A single
repo config can only vouch for its own repo, and today lerp does not verify
the cross-repo half of that rule — keeping it is your job when you copy a
pipeline between repos.

## Workspace commands

Lerp runs `provision` before starting a runner and `dispose` when reaping its
lane. Each command runs from the repo root, writes both standard output and
standard error to the lane log, and receives these environment variables:

- `LERP_LANE` — the lane number
- `LERP_TICKET_ID` — the Linear issue ID
- `LERP_WORKSPACE` — the workspace path

They inherit the rest of lerp's environment, with one variable removed:
`LINEAR_API_KEY` is lerp's own credential and does not go down to a
provision, dispose or runner command. A command that needs Linear must carry
its own credential. See [SECURITY.md](SECURITY.md) for what that does and
does not buy you.

If provisioning fails, lerp leaves the ticket untouched and does not start
the runner. A disposal failure is recorded in the lane log but never keeps a
lane occupied.

## The stock pipeline

[lerp.example.toml](lerp.example.toml) ships one opinion. Lerp holds none:
the order below exists only in the config's `on_success` pointers, and
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
[moving a ticket](promoting.md), not by configuration.

### Why there is no review queue

A hop on the board is a decision somebody makes, and iteration is not a
decision: a review stage of its own turns review-and-fix into a cycle nothing
can bound, since counting the rounds would mean state outside Linear or an
`if` about your process. So Implementing reviews and fixes its own work
inside one run — each round leaves a review comment on the pull request,
findings go on the lines they concern (Low included), and the round count is
the agent's own context, which costs the board nothing. One short verdict
comment on the ticket says how it went.

That comment is also what makes a skipped review visible. The hop out of
Implementing keys on the run's exit code and the ticket's status, never on a
pull request — so an agent that implements, opens the draft and stops still
lands its ticket in In Review looking finished. The prompt answers that by
leading with the contract rather than trailing it: a run ends either with a
verdict on the ticket and the PR marked ready, or in Needs Attention saying
what stopped it and the PR never marked ready. Nothing enforces that
mechanically, but the verdict says how the review went — rounds run, what
the last one found, and how the review was done — which is what keeps it from
reading the same whether the review happened or not, and [the board reads a
ticket's comments](reading-the-board.md#the-main-pane) into the main pane. The
comment goes on before the PR is marked ready: on a team configured as [Lerp
needs the status field](install.md#lerp-needs-the-status-field) asks, nothing is
listening for that flip and lerp's own `on_success` makes the hop — but the
ordering is what makes the run safe on a team that has not done it yet, where
the flip is exactly what frees an automation to move the ticket.

What reaches Needs Attention is only what three rounds could not settle, and
that is a loop rather than the end of the line: say what you want on the pull
request, promote the ticket back to Implementing, and the next run reads your
unanswered threads as its brief, checks out the branch of the pull request
that already exists, and adds commits to it — no second PR. Lerp remembers
none of this. There is no "this is a re-run" flag anywhere, only a ticket in
a queue status, with its description, its comments and its pull request as
the state.

### Two things it assumes

Both worth a deliberate look before you run it:

- **The agent needs its own Linear access.** Lerp passes the ticket
  identifier and nothing else — never the ticket body — and every durable
  artifact — the plan in the ticket, the pull request, the verdict comment —
  is written by the agent, not by lerp. Every CLI a runner names needs its own
  Linear access configured in that CLI — typically Linear's MCP server, as in
  step 2 of the [quickstart](quickstart.md). Lerp scrubs `LINEAR_API_KEY` from
  child environments so agents rely on their own credentials; an exported key
  reaching agents through login shells or shell profiles is the environment leak
  the config comments already warn about, not a supported access path. On a
  machine without that leaked key, any run without MCP configured in its CLI
  silently fails to read its ticket or leave a verdict.
- **The agent runs with permissions — if you say so.** An unattended agent
  that cannot run `git`, `gh`, or your tests just fails, so the stock runner
  wants `--permission-mode bypassPermissions`; `lerp init` asks before
  including it, and defaults to leaving it out. Understand what a yes means:
  the agent runs with your full user account. The worktree that `provision`
  builds is a tidiness boundary, not a security one — nothing stops the agent
  from reading `~/.ssh` or writing outside its workspace. Because the grant
  lives in a checked-in `lerp.toml`, adding, reviewing, or narrowing it (for
  example with `--allowedTools`) is an ordinary code change.

Both of those, and the half that is not about this config at all — who can
trigger a run, and what a ticket's text is to the agent that reads it — are
set out in [SECURITY.md](SECURITY.md), which is also where to report a
vulnerability.
