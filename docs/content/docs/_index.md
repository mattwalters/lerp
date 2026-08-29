---
title: Documentation
description: What lerp is, where the boundary with Linear sits, and the path from install to a first promoted ticket.
---

# Documentation

Lerp runs coding agents against a Linear board.

You write tickets in Linear. Your `lerp.toml` names the statuses lerp
watches. When a ticket reaches one, lerp picks it up, runs a coding agent
on it, and moves it when the run finishes. Lerp leaves the rest of the
board alone, and you watch all of it from one screen.

{{< loop-diagram >}}

Linear stays the source of truth. Lerp reads the board, starts agents and
moves tickets, and moving them is all it writes. There is no server and
no store. Just one binary and the board.
