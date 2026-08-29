---
title: How lerp works
summary: Tickets, queues, runners, lanes, the loop, and the claim that arbitrates between them.
weight: 50
---

# How lerp works

The board is your Linear team's statuses and the tickets sitting in
them. It is the only thing lerp reads and writes, and lerp's
screen is a view of it.

## Ticket

A Linear issue. Linear holds every durable fact, and lerp keeps no store
of its own. Local disk holds config, credentials, run evidence and
[telemetry](telemetry.md), never truth.

## Queue

A Linear status with instructions attached. A ticket sitting in `status`
runs through `runner` with `prompt`, then moves to `on_success`, or to
`on_failure` when the agent exits non-zero (or its stream reports failure or no output).
[Configuration](configuration.md) has the fields.

Those four fields are the whole workflow language. No conditionals, no
templates, no DAG. The topology is where each `on_success` points, and
branching is a person or an agent moving a ticket.

## Runner

An adapter to a coding-agent CLI, one of Claude Code, Codex, Antigravity,
or a [raw command](configuration.md#runners). It takes a prompt and a working
directory, runs to exit, and its exit code means done or failed (unless its
stream reports that the run produced nothing).

## Lane

The concurrency unit. At most N agents run at once, one per lane, and N
defaults to 10. A lane is a disposable workspace that `provision` builds
and `dispose` tears down.

## The loop

A reconciler. Each pass compares the board against the agents running on
this machine and closes the gap. A ticket sitting in a queue's status
with nothing running on it gets an agent, if a lane is free. A finished
run gets its ticket moved. An agent an earlier lerp started gets adopted.
So a crash is drift rather than an error, and [the next lerp picks up
what the last one left](troubleshooting.md#what-happens-on-crash-or-kill).

## The claim

Lerp claims a ticket by assigning it to your Linear user. A ticket is
eligible when it sits in a queue's status, has no assignee, and is not
blocked by an unfinished ticket. A claimed ticket is somebody else's
work, even when the somebody is you, which is how several developers
share a board with no server between them.

## Where a run comes to rest

A finished run releases the claim wherever the ticket rests. Rest in a
status some queue serves, and the next pass runs that stage. Rest in a
status no queue serves, and the pipeline is waiting on a human. That is a
gate. [Promote](promoting.md) it, or move it in Linear.

Two exceptions. A ticket moved out of the queue's status mid-run keeps
that move, and the skipped hop is reported. A ticket assigned to someone
else by the end of the run is theirs, hop and claim both.
