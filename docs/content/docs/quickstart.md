---
title: Quickstart
summary: From `lerp init` to a first promoted ticket, in three steps.
weight: 40
---

# Quickstart

Three steps, from an unconfigured repository to an agent working a
ticket. Finish [Install](install.md) first, through [giving the agents
Linear access](install.md#give-the-agents-linear-access).

## 1. Wire the repo to a team

```sh
lerp login
lerp init --team LERP --team-name "Lerp"
```

Run it from the directory you want to be the root of lerp's work,
which is where init writes `lerp.toml`. `--team` is the Linear team
key, the LERP in LERP-42. Check it, because init creates the team when no
team has that key.

Init asks a few questions to fit the [stock
pipeline](lerp-toml.md#the-stock-pipeline) onto your board, then writes
[`lerp.toml`](lerp-toml.md) and adds `.lerp/` to `.gitignore`. Review
both, and commit both. Answer no to the `bypassPermissions` question
unless you have read [what it
grants](lerp-toml.md#permission-grants-in-checked-in-config).

## 2. Promote a ticket

In Linear, move a ticket into Planning for a big feature, or straight
into Implementing for a small fix. Where you put it is the whole routing
decision. Leave it unassigned and unblocked, or lerp will not
[claim](how-lerp-works.md#the-claim) it.

## 3. Run it

```sh
lerp
```

The board opens and [the loop runs while it is
open](reading-the-board.md). A pass runs every twelve seconds, so your
ticket should take a lane almost at once.

[The board](how-lerp-works.md) explains that screen. [Watching a
run](watching-a-run.md) covers the log. [Promoting](promoting.md) covers
the gates ahead.
