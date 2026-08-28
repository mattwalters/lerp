---
title: Documentation
description: Lerp's docs — what lerp is, install to a first promoted ticket, the concepts, the interface, and reference.
---

# Documentation

Lerp is one binary and one screen. You put tickets on a Linear board; lerp
runs coding agents to move them across it. `lerp init` wires a repository
to a Linear team, and running `lerp` opens the board that moves the work.
There is no server, no scheduler, and no store of lerp's own.

## What is in the box

The board plus four commands. Bare `lerp` opens the TUI, and the
reconciling loop runs while it is open — N lanes, adopting live runs,
reaping dead ones, repairing drift. The board's only two write actions are
the On you panel's [promote](promoting.md) and the work panel's
[force-start](starting-past-the-limit.md). `lerp login`, `lerp logout`,
`lerp init` and `lerp version` complete [the command line](cli.md).

Everything else about a ticket happens in Linear. Lerp reads the board,
runs agents, and moves tickets between statuses; it does not compose
comments, invent work items, or keep a queue of its own.

## Where to start

**Start** is [Install](install.md) — the binary and the two prerequisites
lerp cannot satisfy for you — then [Quickstart](quickstart.md), `lerp init`
to a first promoted ticket. **Concepts** is [The board](the-board.md), the
model both of those sit on. **The interface** is one page per motion
through the screen. **Reference** is [the config file](lerp-toml.md),
telemetry, the command line, and what to do when something looks stuck.
[SCOPE.md](SCOPE.md) is published beside them, as it is written rather
than rewritten — the fence around the project; read it before proposing a
change.
