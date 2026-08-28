---
title: Telemetry
summary: One append-only JSONL line per finished run — where it lives, the schema, and querying with jq.
weight: 125
---

# Telemetry

At run exit, lerp appends one JSON line to a local telemetry file — a
structured record of the finished run's duration, token usage, cost, and
outcome.

## Design stance

Telemetry is history, not state ([SCOPE.md](SCOPE.md) invariant 1). Four
principles:

- **Written once at run exit by deterministic code.** The settling lane's
  goroutine totals token usage from the stream log, notes exit timing and
  exit code, and appends the line before freeing the lane.
- **Read by nothing in lerp.** The loop and the TUI do not query, parse,
  or depend on it. Losing the file costs a chart, never a ticket.
- **Never posted to Linear.** Linear receives stage-boundary decisions —
  plans, pull requests, verdicts. Process measurements belong to local
  history (SCOPE.md invariant 7), never on a ticket.
- **Never trusts agent prose.** Measurements come from config, the loop's
  own evidence records, and deterministic decoders of the stream logs —
  never from agents reporting their own spend.

## Where the file lives

```
$XDG_STATE_HOME/lerp/runs.jsonl
```

With `XDG_STATE_HOME` unset, `~/.local/state/lerp/runs.jsonl`, on both
macOS and Linux.

The file and its parent directories are created on the first finished
run. Writes are serialized across lanes with a process mutex and use
append mode (`O_APPEND`), so multiple `lerp` processes on different
repositories can append concurrently without tearing lines.

## The line format

The format is a stable interface: changes are additive only, and keys are
never renamed or repurposed.

Fields a runner or settlement path could not supply are omitted
(`omitempty`) rather than zero-faked: a command-template runner naming no
vendor omits `vendor`, and a killed run with no exit file omits
`exit_code` and `duration_ms`.

| Field | Type | Description |
| --- | --- | --- |
| `at` | string | ISO 8601 UTC timestamp when the run finished or was reaped (e.g. `"2026-08-27T10:04:11Z"`). Always present. |
| `repo` | string | Absolute path to the repository directory (e.g. `"/home/you/src/lerp"`). Always present. |
| `team` | string | Linear team key prefix from the ticket (e.g. `"LERP"`). Always present. |
| `ticket` | string | Linear ticket identifier (e.g. `"LERP-138"`). Always present. |
| `queue` | string | Name of the queue that ran the ticket (e.g. `"implement"`). Always present. |
| `runner` | string | Configured runner name from `lerp.toml` (e.g. `"claude"`). Always present. |
| `vendor` | string | Built-in vendor adapter name (`"claude"`, `"codex"`, `"antigravity"`), when configured with `vendor`. |
| `model` | string | Model identifier reported by the stream summary or configured on the runner (e.g. `"claude-opus-4-6"`). |
| `session` | string | Session ID, conversation ID, or thread ID for the run. |
| `duration_ms` | integer | Total run duration in milliseconds, read from the exit file timestamp minus started time. |
| `tokens` | integer | Total token usage summed across all tool calls in the run's stream log. |
| `cost_usd` | number | Total cost in USD reported by the runner's stream. |
| `exit_code` | integer | Process exit code (`0` for clean exit, non-zero for failure). Omitted if the run was killed before recording an exit file. |
| `status` | string | Linear status where the ticket came to rest at run exit (e.g. `"In Review"`, `"Needs Attention"`). |

### Example line

```json
{"at":"2026-08-27T10:04:11Z","repo":"/home/you/src/lerp","team":"LERP","ticket":"LERP-138","queue":"implement","runner":"claude","vendor":"claude","model":"claude-opus-4-6","session":"7420e6f8","duration_ms":742318,"tokens":1284310,"cost_usd":3.71,"exit_code":0,"status":"In Review"}
```

## Querying with `jq`

The file is standard JSON Lines, so `jq` is the dashboard.

### Total cost per ticket

```sh
jq -s 'group_by(.ticket)[] | {
  ticket: .[0].ticket,
  cost_usd: (map(.cost_usd // 0) | add | (.*100 | round)/100),
  runs: length
}' ~/.local/state/lerp/runs.jsonl
```

### Tokens per queue over the past 7 days

```sh
jq --arg since "$(date -u -v-7d +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d '7 days ago' +%Y-%m-%dT%H:%M:%SZ)" \
   -s '[.[] | select(.at >= $since)] | group_by(.queue)[] | {
  queue: .[0].queue,
  total_tokens: (map(.tokens // 0) | add),
  total_cost_usd: (map(.cost_usd // 0) | add | (.*100 | round)/100),
  runs: length
}' ~/.local/state/lerp/runs.jsonl
```

### Spend and runs by model

```sh
jq -s 'group_by(.model // "unspecified")[] | {
  model: .[0].model // "unspecified",
  total_cost_usd: (map(.cost_usd // 0) | add | (.*100 | round)/100),
  total_tokens: (map(.tokens // 0) | add),
  runs: length
}' ~/.local/state/lerp/runs.jsonl
```

### Clean exits by queue

```sh
jq -s 'group_by(.queue)[] | {
  queue: .[0].queue,
  clean_exits: (map(select(.exit_code == 0)) | length),
  total_runs: length
}' ~/.local/state/lerp/runs.jsonl
```
