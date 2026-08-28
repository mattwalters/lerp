---
title: Why lerp
description: What lerp is, what it deliberately is not, and how that differs from the other ways to put agents on a backlog.
splash: true
---

# Why lerp

Lerp is an operator's TUI over a Linear board. You open it; it fills a few
lanes with coding agents and moves tickets across the board as they finish;
you close it and everything stops. No daemon, no server, no webhook, no
account with anybody but Linear.

That is a smaller machine than the things it is compared to. The smallness
is the pitch.

## What it is

**Your board, run as the workflow.** A queue is a Linear status with four
fields of config: the status, a prompt, a runner, and the status to move to
on success. Topology lives in where tickets sit and where those pointers
go, so the process is legible on the board itself — not in a DAG file.

**One loop, and crashes are ordinary.** Desired state is the board; actual
state is the agent processes on this machine. Lerp starts, adopts, or reaps
until they match. Killing lerp, or the laptop dying mid-run, is drift
rather than an error: progress is checkpointed at queue boundaries as
artifacts in Linear, so the worst outcome is a stage that runs again.

**It is you on the board, not a bot.** Lerp authenticates as the operator,
and claiming a ticket means assigning it to your Linear user. "Sarah has
LERP-42 in Implementing" reads the same whether Sarah or Sarah's agent is
typing, and colleagues see claims and stage artifacts exactly as they see
each other's human work.

**Agents stay agents.** A runner is a command template around a
coding-agent CLI — Claude Code, Codex, whatever exits non-zero when it
fails. Lerp hands it a prompt and a working directory and reads the exit
code. When you want the wheel, `eject` stops the agent, frees the lane, and
hands you the vendor's own resume command with the session's context
intact.

## What it deliberately is not

- **Not a service.** Nothing listens on a port while lerp works, except the
  few seconds `lerp login` holds a loopback socket open for an OAuth
  redirect at setup time.
- **Not a database.** All durable state is Linear's. Local disk holds
  config, credentials, and evidence of running processes. Deleting it may
  cost compute; it may never cost correctness.
- **Not a workflow engine.** No conditionals, no DAG language, no plugin
  hooks, no retry policies. If a stage needs to branch, an agent or a human
  moves the ticket and the loop respects it.
- **Not a code-host client.** Lerp speaks exactly one external API: Linear.
  Git, GitHub and pull requests belong to the agents' prompts and to you.
  The engine has never heard of a pull request.
- **Not a team scheduler.** No work stealing, no global queue, no fairness
  guarantees. Each developer runs their own lerp against their own clone,
  and claims arbitrate.
- **Not an agent framework, a CI system, a process supervisor, or a
  deployment tool.**

## How that differs from the alternatives

**A hosted agent platform.** You get a queue, a dashboard, somebody
else's compute — and a second system of record, seats to buy, and your
repository in their cloud. Lerp adds no system of record: the board you
already run stand-ups from is the one the agents work from, and nothing
leaves the machine but what you would have typed into Linear anyway.

**Agents wired into CI.** Good for one job on one trigger, poor for a
pipeline: the state lives in a code host, a stage that needs a human
decision has nowhere to wait, and watching a run means reading logs in a
browser tab. Lerp's stages end at a status, human gates are statuses no
queue serves, and the run's stream is a live tail beside the board.

**A general workflow engine** — Temporal, Airflow, an n8n-shaped thing.
The right tools for durable, distributed, long-running processes, with
their own store, topology language, and operational surface. Lerp's bet is
that a backlog of coding work needs none of it: Linear is already the
durable store and the board is already the topology.

**Running the agent yourself, a terminal at a time.** The real incumbent,
and genuinely good at one ticket. It stops scaling at about two: nothing
says which work is claimed, nothing survives closing the terminal, and the
record of what was decided lives in scrollback. Lerp is that same workflow
with lanes, a claim protocol, and the decisions written back to the ticket —
plus `eject` for the ticket that turns out to want your hands after all.

## When lerp is the wrong tool

- **You do not use Linear.** The tracker being the database is the design,
  not an adapter.
- **You want work to happen while nobody is watching.** The TUI is the
  engine. Work happens while lerp is open, on the operator's machine.
- **You have fifty developers and want one scheduler over them.** Different
  product. Lerp's multiplayer story is Linear's: claims, and nothing else.
- **You are on Windows.** macOS and Linux only — the loop uses process
  groups and an advisory `flock` that Windows does not have. WSL2 counts as
  Linux.

Still here? [The manual](docs/) is install to a first promoted ticket and a
page per motion after that, and [SCOPE.md](SCOPE.md) is the fence every
claim above is enforced by.
