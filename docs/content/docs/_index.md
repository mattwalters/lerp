---
title: Documentation
description: What lerp is, where the boundary with Linear sits, and the path from install to a first promoted ticket.
---

# Documentation

Lerp runs coding agents against a Linear board.

You write tickets in Linear. Your `lerp.toml` names the statuses lerp
watches. When a ticket reaches one, lerp picks it up, runs a coding
agent on it, and moves it when the run finishes. Lerp leaves the rest
of the board alone. You watch it all from one screen.

{{< loop-diagram >}}

Linear stays the source of truth. Lerp reads the board, starts agents,
and moves tickets. It never writes comments, never invents work, and
keeps no queue of its own. There is no server, no scheduler, and no
store. Just one binary and the board.
