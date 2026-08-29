---
title: Troubleshooting
summary: The questions a board raises, a ticket that will not start, a stage that got skipped, a run that outlived its lerp.
weight: 140
---

# Troubleshooting

## Why isn't my ticket being picked up?

Check the three [eligibility](how-lerp-works.md#the-claim) conditions, a
queue's status, no assignee, not blocked.

The assignee is the one that surprises people. Assignment is the claim,
so a ticket assigned to anyone, including you, is someone else's work as
far as lerp is concerned. A finished run releases its claim, so a claim
still standing means a human put it there, or a run died with nothing
left to reap it. Either way, select the row in the work panel and press
`S`, which [takes back your own
claim](starting-past-the-limit.md) and runs the stage.

## Why did my ticket skip a stage?

A run whose ticket left its queue status before it finished keeps that
move, and the status bar says which hop it skipped. An agent escalating
moves a ticket to a status your pipeline names. An automation usually
moves it to one it does not, and lerp names those automations at startup.

The usual culprit is Linear's GitHub integration, whose automations sit
per team in workflow settings as pull request triggers. On the teams lerp
serves, set the four open-PR triggers (draft opened, opened, review
activity, ready for merge) to No action, since a run that opens a pull
request trips them mid-stage. Leave On PR merge on, because it fires
after the stock pipeline is done with the ticket. The startup check
cannot see that one, so the settings screen is worth a look.

If your pipeline has a stage that runs after the merge, the merge trigger
is mid-stage for you too. Either set it to No action, or point the
previous stage's `on_success` at the status the automation moves to,
which makes the automation the next stage's trigger. That setup kills the
queue's `on_failure` route, since the ticket has already moved by the
time a failing run ends.

## What happens on crash or kill?

Every queue run is safe to kill and restart from its beginning. Progress
is checkpointed only at queue boundaries, as artifacts in Linear, so the
worst case is a re-run stage and never a lost ticket.

That includes killing lerp itself. Agents outlive it, and the next `lerp`
adopts the live ones and reaps the dead. An adopted run that reached its
own last line recorded its exit status beside its log, so reaping applies
the queue's move rule, and restarting lerp across a run's finish costs
nothing.

A run killed before it got that far records nothing, and reaping it
releases the claim so the ticket becomes eligible again. A failed run
whose queue has no `on_failure` route keeps its claim and waits on you.
To run it again, select it in the work panel and press `S`.

## Why does init tell me to set a status's category myself?

Statuses lerp creates are always in-progress (Linear's "started"
category), and existing statuses are left exactly as you have them. That
leaves one piece of setup only you can finish, where your pipeline ends.

Linear counts a ticket as blocking its dependents (`blockedBy`) until its
status carries a completed category, and init cannot infer which of your
statuses mean the work is done. So for each `on_success` target no queue
watches, it prints whether Linear categorises that status as completed.

If the status means the work is finished, set its category to Done in
Linear. Left in-progress, finished tickets there block their dependents
forever. If a human still acts on tickets there, as with the stock Plan
Review and In Review, in-progress is exactly right, and marking it
completed would release dependents before the work had landed.

## Where are the logs?

- `.lerp/runs/` holds one record and one log per run, the agent's own
  output, which is what [the main pane
  tails](watching-a-run.md#the-log).
- `.lerp/loop.log` holds the loop's diagnostics, provision, dispose,
  adopt, reap, the eject commands lerp handed back, and the full text of
  any status-bar line a narrow terminal truncated. It is appended to
  across sessions, with a marker line at each start.
- `$XDG_STATE_HOME/lerp/runs.jsonl` (or `~/.local/state/lerp/runs.jsonl`)
  holds the append-only [telemetry](telemetry.md) records for finished
  runs.

## How do I clean up or uninstall?

[Cleanup and uninstall](cli.md#cleanup-and-uninstall) has the three
steps. Delete `.lerp/` only once all agents have stopped, since run
evidence is how the next `lerp` adopts or reaps them. Losing local state
costs compute or charts, never correctness.
