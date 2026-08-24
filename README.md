# lerp

Lerp is a small, reliable CLI, written in Go, that orchestrates
software work through Linear. You put tickets on a board; lerp runs
coding agents to move them across it.

Linear is the database, the board is the workflow, and lerp is a
reconciler: it compares the board against the agent processes running
on this machine and starts, adopts, or reaps agents until they match.
[SCOPE.md](SCOPE.md) is the fence around the project — read it before
proposing changes.

## Configuration

Lerp reads two files. The **global config** is yours as an operator —
it defines how agents run everywhere. The **repo config** (`lerp.toml`)
belongs to each repo — it declares which Linear teams the repo serves
and how to build a workspace.

Durable state lives in Linear, never in these files or anywhere else
on disk. Both files are strictly parsed: an unknown key is an error,
not a shrug.

### Global config

Location: `$XDG_CONFIG_HOME/lerp/config.toml`, falling back to
`~/.config/lerp/config.toml`.

```toml
# Max concurrent agents across all lanes. Optional; defaults to 5.
lanes = 5

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
prompt = "Read the ticket and post a plan as a comment."
runner = "claude"
on_success = "Implementing"

[queues.implement]
status = "Implementing"
prompt = "Implement the ticket per the plan comment. Open a PR."
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
- `command` is run by `sh -c`. Use `{{prompt}}` and `{{workdir}}` to
  insert the queue prompt and workspace directory; lerp shell-quotes
  both values. If the runner accepts a caller-chosen session ID (for
  example, Claude Code's `--session-id`), include `{{session}}` in its
  command. Lerp records that generated ID with the run for a later
  eject/resume action.

### Repo config

Location: `lerp.toml` at the repo root, checked in.

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
```

Every ticket must resolve to exactly one working directory: team →
repo is a function. A single repo config can only vouch for its own repo,
so the cross-repo half of that rule ("no two repos claim the same
team") is verified by the loop at startup, which refuses to run if it
doesn't hold.

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
