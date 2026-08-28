---
title: Troubleshooting
summary: The questions a board raises — a ticket that will not start, a stage that got skipped, a run that outlived its lerp.
weight: 140
---

# Troubleshooting

## Why isn't my ticket being picked up?

Check the three [eligibility](the-board.md#the-claim) conditions: a
queue's status, no assignee, not blocked.

The assignee is the one that surprises people — the assignment is the
claim, so a ticket assigned to anyone, including you, is someone else's
work as far as lerp is concerned. A finished run releases its claim, so a
claim still standing usually means a human put it there, or a run died
where nothing was left to reap it. Either way the fix is the same: select
the row in the work panel and press `S`, which
[takes back your own claim](starting-past-the-limit.md) and runs the
stage.

## Why did my ticket skip a stage?

A run whose ticket left its queue status before it finished keeps that
move, and says on the status bar which hop it therefore skipped.

Something moved the ticket mid-stage: an agent escalating (a status your
pipeline names), or an automation (usually one it does not) — see
[Lerp needs the status field](install.md#lerp-needs-the-status-field),
which is also what lerp warns about at startup.

## What happens on crash or kill?

Every queue run is safe to kill and restart from its beginning: progress
is checkpointed only at queue boundaries, as artifacts in Linear — the
plan in the ticket, a PR link — so the worst case is a re-run stage, never
a lost ticket. [SCOPE.md](SCOPE.md) invariants 3 and 4 carry the full
argument.

That includes killing lerp itself: agents outlive it, and the next `lerp`
adopts the live ones and reaps the dead. An adopted run that reached its
own last line recorded its exit status beside its log, so reaping applies
the queue's move rule — restarting lerp across a run's finish costs
nothing.

A run killed before it got that far records nothing, and reaping it
releases the claim so the ticket becomes eligible again. A failed run
whose queue has no `on_failure` route keeps its claim and waits on you;
to run it again, select it in the work panel and press `S`.

## Why does init tell me to set a status's category myself?

Statuses lerp creates are always in-progress (Linear's "started"
category), and existing statuses are left exactly as you have them. That
leaves one piece of setup only you can finish: where your pipeline ends.

Linear counts a ticket as blocking its dependents (`blockedBy`) until its
status carries a completed category — a fact about your process init
cannot infer, so it reports instead of guessing. For each `on_success`
target no queue watches, init prints whether Linear categorises it as
completed.

If such a status genuinely means the work is done, set its category to
Done in Linear; left in-progress, finished tickets there block their
dependents forever. If a human still acts on tickets there — the stock
"Plan Review" and "In Review" — in-progress is exactly right, and marking
it completed would release dependents before the work had landed.

## Where are the logs?

Three places:

- `.lerp/runs/` holds one record and one log per run — the agent's own
  output, what [the main pane tails](watching-a-run.md#the-log).
- `.lerp/loop.log` holds the loop's diagnostics: provision, dispose,
  adopt, reap, the eject commands lerp handed back, and the full text of
  any status-bar line a narrow terminal truncated. Appended to across
  sessions, with a marker line at each start.
- `$XDG_STATE_HOME/lerp/runs.jsonl` (or
  `~/.local/state/lerp/runs.jsonl`) holds the append-only
  [telemetry](telemetry.md) records for finished runs.

## How do I clean up or uninstall?

`brew uninstall lerp` removes a brew install; `make uninstall` from a
clone removes what `make install` put in your `GOBIN`; an install.sh
install is removed by deleting `lerp` from `$HOME/.local/bin` (or the
`--bin-dir` you chose).

`lerp logout` (or deleting the stored token file in your user config
directory) revokes and removes Linear credentials.

To clean up local state, delete `.lerp/` once all agents have stopped:

```sh
rm -rf .lerp/
git worktree prune
```

Run evidence in `.lerp/runs/` is how the next `lerp` adopts or reaps live
agents, and workspaces under `.lerp/workspaces/` are git worktrees whose
registrations `dispose` normally unwinds (`git worktree prune`
unregisters strays). To clear run history too, remove
`$XDG_STATE_HOME/lerp/runs.jsonl` (or `~/.local/state/lerp/runs.jsonl`).
Losing local state costs compute or charts, never correctness.
