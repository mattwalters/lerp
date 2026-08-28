---
title: The board
summary: Tickets, queues, runners, lanes, the loop — and the claim that arbitrates between them.
weight: 50
---

# The board

Lerp has five concepts and no sixth: a **ticket**, a **queue**, a
**runner**, a **lane**, and **the loop**. Everything the board does is
those five arranged by where tickets sit. [SCOPE.md](SCOPE.md) is the same
five as a fence — nine invariants and the litmus tests every change runs
through — and it is canonical where the two ever seem to disagree.

## Tickets, and where state lives

A ticket is a Linear issue, and Linear is the database. All durable state —
what work exists, what stage it is in, who has claimed it, what was
decided — lives there, and lerp keeps no store of its own
([SCOPE.md](SCOPE.md) invariant 1).

Locally, disk holds four things: `lerp.toml` (config, checked in), an
evidence store at `.lerp/` at the repo root (gitignored by init; run
records, logs, workspaces, lock), the operator's credentials, and run
[telemetry](telemetry.md) at `$XDG_STATE_HOME/lerp/runs.jsonl` (or
`~/.local/state/lerp/runs.jsonl`). Local state is evidence and history,
never truth: losing all of it may cost compute or a chart, never
correctness.

## Queues, and why there is no workflow syntax

A queue is a Linear status with instructions attached: tickets sitting in
`status` are picked up, run through `runner` with `prompt`, and moved to
`on_success` on a clean exit — or to `on_failure`, if the queue names one,
when the agent exits non-zero.

That is the whole workflow language. There is deliberately no conditional,
template, or DAG syntax: the topology is in where tickets sit and where
each `on_success` points. An `on_success` naming a status some queue
watches chains into that stage; one naming a status no queue watches is a
gate, where the pipeline waits on a human. Branching is a person or an
agent moving a ticket. See [`lerp.toml`](lerp-toml.md) for the fields and
the stock arrangement.

## Runners

A runner is an adapter to a coding-agent CLI (or a raw command template).
The contract is the lowest common denominator: it takes a prompt and a
working directory, runs to exit, and its exit code means done or failed.

Lerp ships built-in vendor adapters for Claude Code (`claude`), Codex
(`codex`), and Antigravity (`antigravity`), which package flag spellings,
streaming log decoders for the live UI, and session bookkeeping for eject.
A raw command runner covers custom invocations and wrappers. See
[`lerp.toml`](lerp-toml.md#runners) for the configuration keys.

Lerp does not parse an agent's output to decide anything — it reads it
only to draw the screen and record telemetry at exit. What the agent
writes into Linear, it writes itself, with its own credentials.

## Lanes

A lane is the concurrency unit: lerp runs at most N agents at once, one
per lane. Each lane is a disposable workspace, built by the `provision`
command before a run starts and torn down by `dispose` when the lane is
reaped — the stock config uses a git worktree, and environment isolation
(ports, databases, containers) is the repo's own problem, solved in those
two commands. The default N is 10; `-concurrency` changes it, and a repo
with a heavy provision command wants it lower.

## The loop

Lerp is a reconciler. Desired state is the board; actual state is the
agent processes on this machine; each pass starts, adopts, or reaps agents
until the two match. A crash is not an error case — it is drift, and the
loop repairs drift.

An agent is therefore not lerp's child in any sense that matters: each is
its own process group with run evidence on disk, so it outlives the lerp
that started it, and whichever lerp is open next adopts it.
[What a crash or a kill costs](troubleshooting.md#what-happens-on-crash-or-kill)
has its own page.

## The claim

Assignment is the claim: lerp claims a ticket by assigning it to your
Linear user, and a claimed ticket is somebody else's work as far as lerp
is concerned, even when the somebody is you. A ticket is eligible for
pickup when three things are true at once: it sits in a queue's status, it
has no assignee, and it is not blocked by an unfinished ticket (Linear's
`blockedBy`).

The claim is also what makes multiplayer work without a server: each
developer runs their own lerp against their own clone, and the claim
arbitrates between them. The protocol, and what it is allowed to cost when
two lerps collide, is [SCOPE.md](SCOPE.md) invariant 4.

## Where a run comes to rest

Where a finished run leaves the ticket is the whole of the topology. A
finished run releases the claim wherever it comes to rest. Rest in a
status some queue serves, and the next pass picks the ticket up for that
stage. Rest in a status no queue serves, and the pipeline is waiting on a
human — the status is the gate, so nothing needs to hold the ticket there.
[Promote](promoting.md) it, or move it in Linear; either way the loop
carries it on.

Two things call the rule off. A ticket an agent or a human moved out of
the queue's status during the run keeps that move — the skipped hop is
reported, not forced. And a ticket assigned to somebody else by the time
the run ends is theirs entirely, hop and claim both: taking over a run
mid-flight is exactly the way to say so.
