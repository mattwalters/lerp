---
title: Telemetry
summary: One append-only JSONL line per finished run, where it lives, the schema, and querying with jq.
weight: 125
---

# Telemetry

At run exit, lerp appends one JSON line to a local file, a record of the
finished run's duration, token usage, cost and outcome.

Telemetry is history, not state. The settling lane writes it once, from
the run's stream log and the loop's own evidence, never from an agent's
account of its own spend. Nothing in lerp reads it back, and none of it
reaches Linear, which gets decisions rather than measurements. Losing the
file costs a chart, never a ticket.

## Where the file lives

```
$XDG_STATE_HOME/lerp/runs.jsonl
```

With `XDG_STATE_HOME` unset, `~/.local/state/lerp/runs.jsonl`, on both
macOS and Linux.

The file and its parent directories are created on the first finished
run. Writes are serialized across lanes and use append mode, so several
`lerp` processes can append at once without tearing lines.

## The line format

The format is a stable interface. Changes are additive only, and keys are
never renamed or repurposed. A field the run could not supply is omitted
rather than zero-faked, so a command-template runner omits `vendor`, and
a run killed before it recorded an exit file omits `exit_code` and
`duration_ms`.

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

### Clean exits by queue

```sh
jq -s 'group_by(.queue)[] | {
  queue: .[0].queue,
  clean_exits: (map(select(.exit_code == 0)) | length),
  total_runs: length
}' ~/.local/state/lerp/runs.jsonl
```
