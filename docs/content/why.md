---
title: Why lerp
description: The case for running coding agents from your Linear board, and why the smallness is the point.
splash: true
install: brew install mattwalters/tap/lerp
---

# Why lerp

Running a coding agent in a terminal is genuinely good at one ticket.

It stops scaling at about two.

By the third you are naming tmux panes: nothing says which work is
claimed, nothing survives closing the laptop, and the record of what was
decided lives in scrollback. Meanwhile your tracker, the tool whose
entire job is *who is doing what, and what happened*, knows none of it.

The usual fixes are all bigger machines: a hosted agent platform, agents
wired into CI, a workflow engine. Each one adds a second system of
record and asks you to keep it fed.

Lerp's bet is that the system of record you need already exists. It is
your Linear board.

## What a day with lerp looks like

Lerp is an operator's TUI over that board. You open it; it fills a few
lanes with coding agents and moves tickets across the board as they
finish; you close it and everything stops. No daemon, no server, no
webhook, no account with anybody but Linear.

{{< cast webm="casts/board.webm" mp4="casts/board.mp4"
         title="The board opening on the On you panel, switching to the work panel, and opening a lane's log"
         keys="[1] · [2] · [tab] · [enter] · [esc]" >}}

Drop a ticket into a status configured as a queue and an agent picks it
up, works it, and moves it on. Statuses no queue serves are where work
waits for a human: a finished plan parks there until you promote it, and
the promote is the approval. The two questions that actually nag an
operator are the two panels of the screen:

1. What's blocked on me?
2. Are the agents still working?

**The board is the pipeline.** A queue is a Linear status plus a prompt,
a runner, and where to go on success. There is no DAG file; the topology
is where tickets sit and where those pointers go, legible to anyone who
can read the board.

**Close the laptop.** Desired state is the board; actual state is the
agent processes on your machine; lerp reconciles the two. Killing lerp,
or the laptop dying mid-run, is drift, not damage: progress checkpoints
at queue boundaries as artifacts in Linear, and the worst outcome is a
stage that runs again.

**It is you on the board, not a bot.** Lerp authenticates as the
operator, and claiming a ticket means assigning it to your Linear user.
"Sarah has LERP-42 in Implementing" reads the same whether Sarah or
Sarah's agent is typing. No integration to install, no bot seat to
explain.

**Take the wheel whenever.** A runner wraps a coding-agent CLI: Claude
Code, Codex, whatever exits non-zero when it fails. When a ticket turns
out to want your hands, `eject` stops the agent, frees the lane, and
hands you the vendor's own resume command with the session's context
intact. Nothing is trapped inside lerp, because nothing lives inside
lerp.

## What it deliberately is not

The missing features are load-bearing: each is a thing you never have to
operate, secure, or debug.

- **Not a service.** Nothing listens on a port while lerp works, except
  the few seconds `lerp login` holds a loopback socket open for an OAuth
  redirect at setup time.
- **Not a database.** All durable state is Linear's. Local disk holds
  config, credentials, and evidence of running processes. Deleting it may
  cost compute; it may never cost correctness.
- **Not a workflow engine.** No conditionals, no DAG language, no plugin
  hooks, no retry policies. If a stage needs to branch, an agent or a
  human moves the ticket and the loop respects it.
- **Not a code-host client.** Lerp speaks exactly one external API:
  Linear. Git, GitHub and pull requests belong to the agents' prompts and
  to you. The engine has never heard of a pull request.
- **Not a team scheduler.** No work stealing, no global queue, no
  fairness guarantees. Each developer runs their own lerp against their
  own clone, and claims arbitrate.
- **Not an agent framework, a CI system, a process supervisor, or a
  deployment tool.**

## The bigger machines

**A hosted agent platform** gets you a queue, a dashboard, and somebody
else's compute. It also gets you a second system of record, seats to
buy, and your repository in their cloud. Lerp adds none of that: the board you already
run stand-ups from is the one the agents work from, and nothing leaves
your machine but what you would have typed into Linear anyway.

**Agents wired into CI** are good for one job on one trigger and poor
for a pipeline: state lives in the code host, a stage that needs a human
decision has nowhere to wait, and watching a run means reading logs in a
browser tab. Lerp's stages end at a status, human gates are statuses no
queue serves, and a run's stream is a live tail beside the board.

**A general workflow engine** (Temporal, Airflow, an n8n-shaped thing)
is the right tool for durable, distributed, long-running processes, with
its own store, topology language, and operational surface. A backlog of
coding work needs none of it: Linear is already the durable store, and
the board is already the topology.

The pitch in one line: everyone else sells you a bigger machine. Lerp is
smaller, because you already have the big parts: the store, the
topology, the shared view.

## When lerp is the wrong tool

- **You do not use Linear.** The tracker being the database is the design,
  not an adapter.
- **You want work to happen while nobody is watching.** The TUI is the
  engine. Work happens while lerp is open, on the operator's machine.
- **You have fifty developers and want one scheduler over them.** Different
  product. Lerp's multiplayer story is Linear's: claims, and nothing else.
- **You are on Windows.** macOS and Linux only: the loop uses process
  groups and an advisory `flock` that Windows does not have. WSL2 counts as
  Linux.

## Try it

[The docs](docs/) take you from install to a first promoted ticket, with
a page per motion after that.

If you want the design argued at full length, that lives in
[SCOPE.md](SCOPE.md).

{{< install >}}

{{< cta "/docs/quickstart" >}}$ lerp init →{{< /cta >}}
