---
title: The board
summary: Tickets, queues, runners, lanes, the loop — and the claim that arbitrates between them.
weight: 50
---

# The board

Lerp has five concepts and no sixth: a **ticket**, a **queue**, a
**runner**, a **lane**, and **the loop**. Everything the board does is those
five arranged by where tickets sit. This page is them as an adopter meets
them; [SCOPE.md](SCOPE.md) is the same five as a fence, with the nine
invariants and the litmus tests every change to lerp runs through, and it is
the canonical text where the two ever seem to disagree.

## Tickets, and where state lives

A ticket is a Linear issue, and Linear is the database. All durable state —
what work exists, what stage it is in, who has claimed it, what was decided —
lives there, and lerp keeps no store of its own. [SCOPE.md](SCOPE.md)
invariant 1 is what holds that.

Locally, disk holds four things: `lerp.toml` (config, checked in), an
evidence store at `.lerp/` at the repo root (gitignored by init; run records,
logs, workspaces, and lock), the operator's credentials, and run
[telemetry](telemetry.md) at `$XDG_STATE_HOME/lerp/runs.jsonl` (or
`~/.local/state/lerp/runs.jsonl`). Local state is evidence and history, never
truth: losing all of it may cost compute or a chart, never correctness.

## Queues, and why there is no workflow syntax

A queue is a Linear status with instructions attached: tickets sitting in
`status` are picked up, run through `runner` with `prompt`, and moved to
`on_success` on a clean exit — or to `on_failure`, if the queue names one,
when the agent exits non-zero.

That is the whole of the workflow language. There is deliberately no
conditional, template, or DAG syntax, because the topology is not in the
config at all: it is in where tickets sit and where each `on_success`
points. A stage whose `on_success` names a status some queue watches chains
into that stage; one that names a status no queue watches is a gate, where
the pipeline waits on a human. Branching is a person or an agent moving a
ticket. Rewriting the pipeline into a different shape needs no code change
and no new syntax — see [`lerp.toml`](lerp-toml.md) for the fields and the
stock arrangement of them.

## Runners

A runner is an adapter to a coding-agent CLI (or a raw command template). The
contract is the lowest common denominator: it takes a prompt and a working
directory, runs to exit, and its exit code means done or failed.

Lerp ships built-in vendor adapters for Claude Code (`claude`), Codex
(`codex`), and Antigravity (`antigravity`), which package flag spellings,
streaming log decoders for the live UI, and session bookkeeping for eject. A
raw command runner is available for custom CLI invocations and wrappers. See
[`lerp.toml`](lerp-toml.md#runners) for every runner configuration key.

Lerp does not parse an agent's output to decide anything — it reads it only to
draw it on the screen and record telemetry at exit. What the agent writes into
Linear, it writes itself, with its own credentials.

## Lanes

A lane is the concurrency unit: lerp runs at most N agents at once, one per
lane. Each lane is a whole disposable workspace, built by the `provision`
command before a run starts and torn down by `dispose` when the lane is
reaped — the stock config uses a git worktree, and environment isolation
(ports, databases, containers) is the repo's own problem, solved in those
two commands. The default N is 10 and `-concurrency` changes it; a repo with
a heavy provision command wants it lower.

## The loop

Lerp is a reconciler. Desired state is the board; actual state is the agent
processes running on this machine; and each pass starts, adopts, or reaps
agents until the two match. A crash is not an error case — it is drift, and
the loop repairs drift.

That is why an agent is not lerp's child in any sense that matters. Each one
is its own process group with its run evidence on disk, so it outlives the
lerp that started it, and the pass that finds it belongs to whichever lerp is
open — [what a crash or a kill actually
costs](troubleshooting.md#what-happens-on-crash-or-kill) is a question that
page answers.

## The claim

Assignment is the claim: lerp claims a ticket by assigning it to your Linear
user, and a claimed ticket is somebody else's work as far as lerp is
concerned, even when the somebody is you. So a ticket is eligible for pickup
when three things are true at once — it sits in a queue's status, it has no
assignee, and it is not blocked by an unfinished ticket (Linear's
`blockedBy`).

The claim is also what makes multiplayer work without a server: each
developer runs their own lerp against their own clone, and the claim
arbitrates between them, with no lerp server and no coordination service
anywhere. The protocol that does it, and what it is allowed to cost when two
lerps collide, is [SCOPE.md](SCOPE.md) invariant 4 — read it there rather
than here, because it is the kind of rule that is worth having in one place.

What falls out for an adopter is that colleagues see claims and stage
artifacts, the same visibility they have into each other's human work.

## Where a run comes to rest

Where a finished run leaves the ticket is the whole of the topology. A
finished run releases the claim wherever it comes to rest. Coming to rest in
a status some queue serves means the next pass picks the ticket up for that
stage on its own. Coming to rest in a status no queue serves is the pipeline
waiting on a human — the status is the gate, so nothing needs to hold the
ticket there. [Promote](promoting.md) it, or move it in Linear; either way
the loop carries it on from there.

Two things call the whole rule off. A ticket an agent or a human moved out of
the queue's status during the run keeps that move — the hop it skipped is
reported rather than forced. And a ticket assigned to somebody else by the
time the run ends is theirs entirely, hop and claim both, since taking over a
run mid-flight is exactly the way to say so.
