---
title: Configuration
aliases:
  - /docs/lerp-toml
  - /docs/lerp-toml/
summary: The one file lerp reads, teams, workspace commands, runners and queues, and the stock pipeline it ships with.
weight: 120
---

# Configuration

Lerp reads one config file at the root of your project, **checked in**.
The root is the directory holding that file, found by walking up from
wherever lerp was run. It declares which Linear teams the repo serves,
how to build a workspace, and the pipeline itself, runners and queues
with their prompts. It holds no durable state, because [Linear is the
database](how-lerp-works.md#ticket). Parsing is strict, and an unknown
key is an error.

TOML is the default format and what `lerp init` writes (`lerp.toml`).
YAML (`lerp.yaml` or `lerp.yml`) and JSON (`lerp.json`) are accepted as
well. Exactly one config file may exist at the repo root; having more
than one is refused at startup.

`lerp init --team KEY` writes it (see [Quickstart](quickstart.md)).
[lerp.example.toml](lerp.example.toml) is the stock pipeline in full,
with prompts you can read and argue with. Read it before you run it.

## The shape of it

{{< config-snippet "shape" >}}

Those queue fields are the complete set, `status`, `prompt`, `runner`,
`on_success`, and optionally `on_failure`. There is no conditional,
template or DAG syntax, because [topology lives on the
board](how-lerp-works.md#queue).

## Runners

A runner defines how lerp invokes a coding-agent CLI for a queue. There
are two kinds.

**Vendor adapters** (`vendor = "claude"`, `"codex"` or `"antigravity"`)
package what a shell template cannot express. They supply the flags that
stream events live (`--output-format stream-json --verbose` for Claude
Code and Antigravity, `--json` for Codex), which is what the log pane
decodes into activity lines, token counts and sparklines. They also track
the session [ejecting](ejecting.md) resumes. Claude Code takes a UUID
lerp mints up front, while Codex and Antigravity assign their own IDs,
which lerp reads back from the run's stream log.

**Command templates** (`command = "..."`) run the exact shell command you
write, via `sh -c`, keeping the contract at the lowest common
denominator, prompt and working directory in, exit code out. Reach for
one for an unsupported CLI, an agent inside a container or VM (`docker
exec ...`), or a custom wrapper.

{{< config-snippet "runner-command" >}}

### Runner configuration keys

| Key | Type | Kind | Description |
| --- | --- | --- | --- |
| `vendor` | string | vendor only | Names a built-in vendor adapter: `"claude"`, `"codex"`, or `"antigravity"`. Exactly one of `vendor` or `command` must be set. |
| `model` | string | vendor only | Model override passed to the CLI (`--model <model>` on claude/codex/antigravity). Shell-quoted. |
| `effort` | string | vendor only | Reasoning effort override (`--effort <effort>` on claude/antigravity, `-c model_reasoning_effort=<effort>` on codex). Shell-quoted. |
| `args` | string | vendor only | Extra CLI flags appended verbatim and last to the command template. Unquoted shell text, and it overrides earlier flags on last-wins CLIs. |
| `context` | integer | vendor or command | The model's context window in tokens (e.g. `200000`). Display denominator for the token percentage on a running row, faint until 80% and then `⚠`. `0` (default) means unset, and no percentage is shown. |
| `command` | string | command only | Raw shell command template executed via `sh -c`. Supports `{{prompt}}`, `{{ticket}}`, `{{workdir}}`, and `{{session}}`. Shell-quoted placeholders. |
| `resume` | string | command only | Resume command template handed to operator on eject. Supports `{{session}}`, `{{ticket}}`, `{{workdir}}`. |

Validation rules:
- Exactly one of `vendor` or `command` must be set per runner.
- Setting `model`, `effort`, or `args` on a command runner is refused at startup.
- Setting `resume` on a vendor runner is refused at startup (the adapter supplies its own resume template).
- Setting a negative `context` is refused at startup.

### Permission grants in checked-in config

No adapter lerp ships adds permission-skipping flags on its own. An
unattended agent that cannot edit files or run `git`, `gh`, and tests
fails at the first restricted tool call. To permit unattended execution,
write the grant explicitly into your runner's `args` (or `command`):

- **Claude Code:** `args = "--permission-mode bypassPermissions"`
- **Codex:** `args = "--dangerously-bypass-approvals-and-sandbox"`
- **Antigravity:** `args = "--dangerously-skip-permissions"`

Saying yes means the agent runs with your full user account. The worktree
that `provision` builds is a tidiness boundary, not a security sandbox,
and nothing stops an agent from reading `~/.ssh`. Because the grant lives
in a checked-in file, adding it, reviewing it, or narrowing it with
`--allowedTools` is an ordinary code change. [SECURITY.md](SECURITY.md)
covers the rest, including who can trigger a run.

## Workspace commands

Lerp runs `provision` before starting a runner and `dispose` when reaping
its lane. Each runs from the repo root, writes stdout and stderr to the
lane log, and receives:

- `LERP_LANE`, the lane number
- `LERP_TICKET_ID`, the Linear issue ID
- `LERP_WORKSPACE`, the workspace path

They inherit the rest of lerp's environment minus one variable.
`LINEAR_API_KEY` is lerp's own credential and never reaches a provision,
dispose or runner command, and stored OAuth tokens are never passed
either. A command that needs Linear carries its own credential.

If provisioning fails, lerp leaves the ticket untouched and does not
start the runner. A disposal failure is recorded in the lane log but
never keeps a lane occupied.

## Queues

A queue connects a Linear status to a runner and a prompt.

| Field | Type | Description |
| --- | --- | --- |
| `status` | string | The Linear status this queue watches. Must match a status on the team's board. Each status may drive at most one queue. |
| `prompt` | string | Instructions passed to the runner. Plain text. |
| `runner` | string | Name of a runner defined under `[runners]`. |
| `on_success` | string | Linear status to move the ticket to on a clean exit (exit code 0), unless the agent already moved the ticket itself or the run's stream reports that it produced nothing. |
| `on_failure` | string | Optional. Linear status to move the ticket to when the agent exits non-zero or its stream reports failure or no output. |

### Prompt placeholders

Prompts support four runtime placeholders:

- `{{ticket}}`, the Linear ticket identifier (e.g. `LERP-42`). Also exported to runners as `LERP_TICKET`.
- `{{status}}`, this queue's own `status` field.
- `{{on_success}}`, this queue's own `on_success` status.
- `{{on_failure}}`, this queue's own `on_failure` status. Referencing it when unset is a config error.

Name the ticket in your prompt. A prompt is shared across every ticket in
its queue, so one that never names `{{ticket}}` leaves the agent no way
to know which ticket it was started for. Name target statuses by
placeholder too, so "Move {{ticket}} to {{on_failure}}" follows a status
rename on its own.

## The stock pipeline

[lerp.example.toml](lerp.example.toml) ships one opinion. Lerp holds
none. The order below exists only in that config's `on_success`
pointers, and rewriting them into a different shape needs no code change.

| Status | Who acts | Then |
| --- | --- | --- |
| Backlog / Todo | you | promote a ticket into Planning, or into Implementing if it is small |
| Planning | agent | writes the plan into the ticket's description, under `## Plan` → Plan Review |
| Plan Review | you | read the plan, edit it where you disagree, then press `p` to promote: Implementing to build it, Planning to re-plan, Needs Attention to park it |
| Implementing | agent | reads its brief, commits, pushes, opens a draft PR with `gh` or adds to the ticket's existing one, then reviews and fixes its own work until a round is clean or three rounds are up, then ends by leaving a verdict comment and marking the PR ready → In Review, or moves itself → Needs Attention |
| In Review | you | merge the PR, and Linear's GitHub integration moves it to Done |
| Needs Attention | you | where failed runs and unsettled reviews park, with no queue watching it, so nothing retries it |

Which ticket enters where is the only routing decision, and it is made by
[moving a ticket](promoting.md), not by configuration.

### Review happens inside Implementing

There is no review queue. A stage that hands work back draws a cycle, and
bounding a cycle needs a round count, which is state outside Linear
([SCOPE.md](SCOPE.md) carries the argument). So Implementing reviews and
fixes its own work inside one run, leaving each round's findings as a
review on the pull request and one verdict comment on the ticket.

The verdict is also the check on the review. The hop out of Implementing
keys on the run's exit code and the ticket's status, never on a pull
request, so the prompt requires a run to end either with a verdict and
the PR marked ready, or in Needs Attention saying what stopped it. The
comment goes on before the PR flips to ready, which keeps the run safe on
a team that still has [a mid-stage PR
automation](troubleshooting.md#why-did-my-ticket-skip-a-stage) on.

Needs Attention holds what three rounds could not settle, and it is a
loop rather than the end of the line. Say what you want on the pull
request, promote the ticket back to Implementing, and the next run reads
your unanswered threads as its brief, checks out the same branch, and
adds commits. There is no re-run flag anywhere, only a ticket in a queue
status with its description, comments and pull request as the state.

### Two things it assumes

- **Every agent CLI needs its own Linear access.** Lerp passes the ticket
  identifier and nothing else, so the plan, the pull request and the
  verdict are all written by the agent. [Install](install.md#give-the-agents-linear-access)
  sets that up. Without it, a run silently fails to read its ticket.
- **The stock runner wants a permission grant.** `lerp init` asks before
  including it and defaults to leaving it out. See [permission
  grants](#permission-grants-in-checked-in-config) above.
