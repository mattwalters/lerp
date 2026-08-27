---
title: Promoting
summary: Moving a ticket into the next stage — the one routing decision, and one of the board's two writes.
weight: 80
---

# Promoting

Routing is done by placing a ticket, and promoting is that act from the
board: select a row in the [Inbox](reading-the-board.md#the-inbox-panel) and
press `p`, pick a target from the configured queue statuses or a pipeline
exit, and lerp moves it there.

<!-- Cast slot (LERP-70): a plan read in the main pane, then promoted out of
     the gate into the next queue and picked up on the following pass.
     keys: [enter] · [p] · pick a status · [enter] -->

## What promoting is for

A run that finishes in a status no queue serves has come to rest at [a
gate](the-board.md#where-a-run-comes-to-rest), and the stock pipeline has
two: "Plan Review", where you read the plan, and "In Review", where you merge
the pull request. Promoting is how a ticket leaves one. So is moving it in
Linear; either way the next pass carries it on.

Which is the whole of the routing decision, and it is a human's: lerp never
invents work items of its own, and it never promotes a ticket for you.

## What it writes

The move and nothing else. That MoveIssue and
[force-start](starting-past-the-limit.md)'s claim are the only writes any
view makes; everything else about a ticket still happens in Linear, and `o`
opens the selected ticket there.

`p` drops out of the key line where there is no status to promote into, or no
room to draw the picker. Inside the picker, `↑`/`↓` pick a target, `enter`
takes it, and `esc` — or `q` — backs out without writing anything.

## After the promote

A promoted ticket is [eligible](the-board.md#the-claim) the moment it is
sitting in a queue's status unassigned and unblocked, so the next pass picks
it up on its own. If it does not, [troubleshooting](troubleshooting.md#why-isnt-my-ticket-being-picked-up)
walks the three conditions.
