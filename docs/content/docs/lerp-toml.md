---
title: lerp.toml
summary: The one file lerp reads — teams, workspace commands, runners, queues — and the stock pipeline it ships with.
weight: 120
---

# `lerp.toml`

Lerp reads one file: `lerp.toml` at the repo root, **checked in**. It
declares which Linear teams the repo serves, how to build a workspace,
and the pipeline itself — runners and queues, prompts included. Checked
in, the permissions it grants are versioned and reviewed like code, and
every developer runs the same pipeline against the same board.

No durable state lives in this file or anywhere else on disk — [Linear is
the database](the-board.md#tickets-and-where-state-lives). The file is
strictly parsed: an unknown key is an error, not a shrug.

[lerp.example.toml](lerp.example.toml) is the stock pipeline in full, with
prompts you can read and argue with. `lerp init --team KEY` writes it into
the repo (see [Quickstart](quickstart.md)). Read it before you run it: it
may grant the agent broad permissions and it assumes your runner can reach
Linear, both covered [below](#the-stock-pipeline).

## The shape of it

{{< shot src="lerp-toml-shape.svg" alt="A minimal lerp.toml, every field annotated: a teams list; provision and dispose commands building a git worktree per lane; a claude runner with commented-out model, effort and context overrides and an explicit permission grant in args; and two queues — plan, watching Planning and moving to Implementing, and implement, watching Implementing and moving to In Review, with Needs Attention as its on_failure." >}}

The queue fields above are the complete set — `status`, `prompt`,
`runner`, `on_success`, and optionally `on_failure`. There is deliberately
no conditional, template, or DAG syntax:
[topology lives on the board](the-board.md#queues-and-why-there-is-no-workflow-syntax),
and branching is a human or an agent moving a ticket.

## Runners

A runner defines how lerp invokes a coding-agent CLI for a queue. There
are two kinds:

**Vendor adapters** (`vendor = "claude"` / `"codex"` / `"antigravity"`)
are built-in adapters for supported CLIs. They package what a shell
template cannot express cleanly:

- **Streaming log decoders.** Each adapter supplies the flags that stream
  events live (`--output-format stream-json --verbose` for Claude Code and
  Antigravity, `--json` for Codex), and lerp's log pane decodes the stream
  into activity lines, token usage, and sparklines.
- **Session tracking for eject.** Claude Code accepts a caller-chosen UUID
  (`--session-id`), which lerp mints up front; Codex and Antigravity
  assign their own thread or conversation IDs, which lerp extracts from
  the run's stream log. Either way [ejecting](ejecting.md) hands back the
  correct `resume` command with the session's context intact.

Reach for a vendor adapter for any supported CLI: live log formatting,
accurate token and cost metrics, and eject support with no shell
boilerplate.

**Command templates** (`command = "..."`) run the exact shell command you
write, via `sh -c`, keeping the contract at the lowest common denominator:
prompt and working directory in, exit code out. Reach for one for an
unsupported CLI, agents inside a container or VM (`docker exec ...`), or a
custom wrapper:

{{< shot src="runner-command.svg" alt="A command-template runner: command invokes my-agent with prompt, workdir and session placeholders; resume changes into the workdir and resumes the session, the command eject hands back." >}}

### Runner configuration keys

| Key | Type | Kind | Description |
| --- | --- | --- | --- |
| `vendor` | string | vendor only | Names a built-in vendor adapter: `"claude"`, `"codex"`, or `"antigravity"`. Exactly one of `vendor` or `command` must be set. |
| `model` | string | vendor only | Model override passed to the CLI (`--model <model>` on claude/codex/antigravity). Shell-quoted. |
| `effort` | string | vendor only | Reasoning effort override (`--effort <effort>` on claude/antigravity, `-c model_reasoning_effort=<effort>` on codex). Shell-quoted. |
| `args` | string | vendor only | Extra CLI flags appended verbatim and last to the command template. Unquoted shell text; overrides earlier flags on last-wins CLIs. |
| `context` | integer | vendor or command | The model's context window in tokens (e.g. `200000`). Display denominator for token percentage on the board; `0` (default) means unset (no percentage shown). |
| `command` | string | command only | Raw shell command template executed via `sh -c`. Supports `{{prompt}}`, `{{ticket}}`, `{{workdir}}`, and `{{session}}`. Shell-quoted placeholders. |
| `resume` | string | command only | Resume command template handed to operator on eject. Supports `{{session}}`, `{{ticket}}`, `{{workdir}}`. |

Validation rules:
- Exactly one of `vendor` or `command` must be set per runner.
- Setting `model`, `effort`, or `args` on a command runner is refused at startup.
- Setting `resume` on a vendor runner is refused at startup (the adapter supplies its own resume template).
- Setting a negative `context` is refused at startup.

### Permission grants in checked-in config

**Permissions are always stated in checked-in config, never defaulted.**

No adapter lerp ships adds permission-skipping flags on its own. An
unattended agent that cannot edit files or run `git`, `gh`, and tests
fails at the first restricted tool call. To permit unattended execution,
write the grant explicitly into your runner's `args` (or `command`):

- **Claude Code:** `args = "--permission-mode bypassPermissions"`
- **Antigravity:** `args = "--dangerously-skip-permissions"`

The worktree that `provision` builds is a tidiness boundary, not a
security sandbox: nothing stops the agent from reading `~/.ssh` or writing
outside its workspace. Stating grants in checked-in `lerp.toml` keeps them
versioned and reviewed like code (see [SECURITY.md](SECURITY.md)).

## Workspace commands

Lerp runs `provision` before starting a runner and `dispose` when reaping
its lane. Each command runs from the repo root, writes stdout and stderr
to the lane log, and receives:

- `LERP_LANE` — the lane number
- `LERP_TICKET_ID` — the Linear issue ID
- `LERP_WORKSPACE` — the workspace path

They inherit the rest of lerp's environment minus one variable:
`LINEAR_API_KEY` is lerp's own credential and does not go down to a
provision, dispose or runner command (stored OAuth tokens are never passed
either). A command that needs Linear carries its own credential. See
[SECURITY.md](SECURITY.md) for what that does and does not buy.

If provisioning fails, lerp leaves the ticket untouched and does not start
the runner. A disposal failure is recorded in the lane log but never keeps
a lane occupied.

## Queues

A queue connects a Linear status to a runner and a prompt.

| Field | Type | Description |
| --- | --- | --- |
| `status` | string | The Linear status this queue watches. Must match a status on the team's board. Each status may drive at most one queue. |
| `prompt` | string | Instructions passed to the runner. Plain text. |
| `runner` | string | Name of a runner defined under `[runners]`. |
| `on_success` | string | Linear status to move the ticket to on a clean exit (exit code 0), unless the agent already moved the ticket itself. |
| `on_failure` | string | Optional. Linear status to move the ticket to when the agent exits non-zero. |

### Prompt placeholders

Prompts support four runtime placeholders:

- `{{ticket}}` — the Linear ticket identifier (e.g. `LERP-42`). Also exported to runners as `LERP_TICKET`.
- `{{status}}` — this queue's own `status` field.
- `{{on_success}}` — this queue's own `on_success` status.
- `{{on_failure}}` — this queue's own `on_failure` status (referencing `{{on_failure}}` when unset is a config error).

**Name the ticket in your prompt.** A prompt is shared across all tickets
in its queue; one that never names `{{ticket}}` leaves the agent no way to
know which ticket it was started for.

**Name target statuses by placeholder.** "Move {{ticket}} to
{{on_failure}}" follows status renames and remaps in config on its own.

## The stock pipeline

[lerp.example.toml](lerp.example.toml) ships one opinion. Lerp holds
none: the order below exists only in the config's `on_success` pointers,
and rewriting them into a different shape needs no code change.

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
decision: a review stage of its own turns review-and-fix into a cycle
nothing can bound, since counting rounds would mean state outside Linear.
So Implementing reviews and fixes its own work inside one run — each round
leaves a review comment on the pull request, findings on the lines they
concern (Low included), the round count in the agent's own context — and
one short verdict comment on the ticket says how it went.

The verdict is also what makes a skipped review visible. The hop out of
Implementing keys on the run's exit code and the ticket's status, never
on a pull request — so an agent that implements, opens the draft and
stops would land its ticket in In Review looking finished. The prompt
leads with the contract: a run ends either with a verdict on the ticket
and the PR marked ready, or in Needs Attention saying what stopped it,
the PR never marked ready. Nothing enforces that mechanically, but the
verdict states rounds run, what the last one found, and how the review
was done — so it cannot read the same whether the review happened or
not — and [the board reads a ticket's
comments](reading-the-board.md#the-main-pane) into the main pane. The
comment goes on before the PR flips to ready, which is what keeps the run
safe on a team that has not yet done what
[Lerp needs the status field](install.md#lerp-needs-the-status-field)
asks — there, the flip is exactly what frees an automation to move the
ticket.

What reaches Needs Attention is only what three rounds could not settle,
and it is a loop rather than the end of the line: say what you want on the
pull request, promote the ticket back to Implementing, and the next run
reads your unanswered threads as its brief, checks out the PR's existing
branch, and adds commits — no second PR. Lerp remembers none of this:
there is no "re-run" flag anywhere, only a ticket in a queue status with
its description, comments and pull request as the state.

### Two things it assumes

Both worth a deliberate look before you run it:

- **The agent needs its own Linear access.** Lerp passes the ticket
  identifier and nothing else — never the ticket body — and every durable
  artifact (the plan, the pull request, the verdict comment) is written by
  the agent, not by lerp. Every CLI a runner names needs its own Linear
  access configured in that CLI — typically Linear's MCP server, as in
  step 2 of the [quickstart](quickstart.md). Lerp scrubs `LINEAR_API_KEY`
  from child environments, so an exported key reaching agents through a
  shell profile is the environment leak the config comments warn about,
  not a supported access path. Without MCP configured in its CLI, a run
  silently fails to read its ticket or leave a verdict.
- **The agent runs with permissions — if you say so.** An unattended agent
  that cannot run `git`, `gh`, or your tests just fails, so the stock
  runner wants `--permission-mode bypassPermissions`; `lerp init` asks
  before including it and defaults to leaving it out. Understand what yes
  means: the agent runs with your full user account, and the provisioned
  worktree is a tidiness boundary, not a security one. Because the grant
  lives in a checked-in `lerp.toml`, adding, reviewing, or narrowing it
  (for example with `--allowedTools`) is an ordinary code change.

Both of those, and the half that is not about this config at all — who can
trigger a run, and what a ticket's text is to the agent that reads it —
are set out in [SECURITY.md](SECURITY.md), which is also where to report a
vulnerability.
