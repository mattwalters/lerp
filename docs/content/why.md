---
title: Why lerp
splash: true
---

# Why lerp

Lerp is an operator's TUI over a Linear board. You open it; it fills a few
lanes with coding agents and moves tickets across the board as they finish;
you close it and everything stops. No daemon, no server, no webhook, no
account with anybody but Linear.

That is a deliberately smaller machine than most of the things it is compared
to, and the smallness is the pitch. This page is the honest version of it.

## What it is

**Your board, run as the workflow.** A queue is a Linear status with four
fields of config: the status, a prompt, a runner, and the status to move to
on success. Topology lives in where tickets sit and where those pointers go,
so the process is legible on the board itself — not in a DAG file that a
reader has to hold beside it.

**One loop, and crashes are ordinary.** Desired state is the board; actual
state is the agent processes on this machine. Lerp compares the two and
starts, adopts, or reaps until they match. Killing lerp, or the laptop dying
mid-run, is drift rather than an error case: the worst outcome is a stage
that runs again, because progress is only ever checkpointed at queue
boundaries as artifacts in Linear.

**It is you on the board, not a bot.** Lerp authenticates as the operator,
and claiming a ticket means assigning it to your Linear user. "Sarah has
LERP-42 in Implementing" reads the same whether Sarah or Sarah's agent is
typing, and your colleagues see claims and stage artifacts through Linear
exactly as they see each other's human work.

**Agents stay agents.** A runner is a command template around a coding-agent
CLI — Claude Code, Codex, whatever exits non-zero when it fails. Lerp hands
it a prompt and a working directory and reads the exit code. When you want
the wheel, `eject` stops the agent, frees the lane, and hands you the
vendor's own resume command with the session's context intact.

## What it deliberately is not

- **Not a service.** Nothing listens on a port while lerp works. The single
  exception is the few seconds `lerp login` holds a loopback socket open for
  an OAuth redirect, at setup time, before any work runs.
- **Not a database.** All durable state is Linear's. Local disk holds config,
  your credentials, and evidence of running processes. Deleting it may cost
  compute; it may never cost correctness.
- **Not a workflow engine.** No conditionals, no DAG language, no plugin
  hooks, no retry policy language. If a stage needs to branch, an agent or a
  human moves the ticket somewhere else and the loop respects it.
- **Not a code-host client.** Lerp speaks exactly one external API: Linear.
  Git, GitHub and pull requests belong to the agents' prompts and to you. The
  engine has never heard of a pull request, which is why it works for people
  who do not make them.
- **Not a team scheduler.** No work stealing, no global queue, no fairness
  guarantees. Each developer runs their own lerp against their own clone, and
  claims arbitrate.
- **Not an agent framework, a CI system, a process supervisor, or a
  deployment tool.**

## How that differs from the alternatives

**A hosted agent platform.** You get a queue, a dashboard and somebody else's
compute — and a second system of record for what your team is working on,
seats to buy, and your repository in their cloud. Lerp adds no system of
record: the board you already run stand-ups from is the one the agents work
from, the compute is your laptop, and the only thing that leaves the machine
is what you would have typed into Linear anyway.

**Agents wired into CI.** A workflow that runs an agent on an event is a good
way to do one job on one trigger, and a poor way to run a pipeline: the state
lives in a code host, a stage that needs a human decision has nowhere to
wait, and watching a run means reading logs in a browser tab. Lerp's stages
end at a status, human gates are statuses no queue serves, and the run's
stream is a live tail in the pane beside the board.

**A general workflow engine** — Temporal, Airflow, an n8n-shaped thing.
These are the right tools for durable, distributed, long-running processes,
and they bring their own store, their own topology language, and their own
operational surface. Lerp's bet is that a backlog of coding work does not
need any of it, because Linear is already the durable store and the board is
already the topology.

**Running the agent yourself, a terminal at a time.** This is the real
incumbent, and it is genuinely good at one ticket. It stops scaling at about
two: nothing says which work is claimed, nothing survives closing the
terminal, and the record of what was decided lives in scrollback. Lerp is
that same workflow with lanes, a claim protocol, and the decisions written
back to the ticket — plus `eject` for the moment a ticket turns out to want
your hands after all.

## When lerp is the wrong tool

- **You do not use Linear.** Lerp is not portable to another tracker; the
  tracker being the database is the design, not an adapter.
- **You want work to happen while nobody is watching.** The TUI is the
  engine. Work happens while lerp is open, on the operator's machine.
- **You have fifty developers and want one scheduler over them.** That is a
  different product. Lerp's multiplayer story is Linear's: claims, and
  nothing else.
- **You are on Windows.** macOS and Linux only — the loop uses process
  groups and an advisory `flock` that Windows does not have as such. WSL2 is
  Linux as far as lerp is concerned, and that is the answer there.

Still here? [The manual](docs/) is install to a first promoted ticket and a
page per motion after that, and [SCOPE.md](SCOPE.md) is the fence every one
of the claims above is enforced by.
