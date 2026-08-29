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
lerp init
```

Run it from the directory you want to be the root of lerp's work,
which is where init writes `lerp.toml`. Init lists your workspace's
teams so you can pick one, or creates a new one if you choose (in
scripts and CI, pass `--team KEY` and `--yes`).

Init asks a few questions to fit the [stock
pipeline](configuration.md#the-stock-pipeline) onto your board, then writes
[`lerp.toml`](configuration.md) and adds `.lerp/` to `.gitignore`. Review
both, and commit both. Answer no to the `bypassPermissions` question
unless you have read [what it
grants](configuration.md#permission-grants-in-checked-in-config).

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
