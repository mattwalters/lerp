---
title: Promoting
summary: Moving a ticket into the next stage, the one routing decision and one of the board's two writes.
weight: 100
---

# Promoting

Select a row in [On you](reading-the-board.md#the-on-you-panel), press
`p`, pick a target from the configured queue statuses or a pipeline exit,
and lerp moves the ticket there.

{{< cast webm="casts/promote.webm" mp4="casts/promote.mp4"
         title="A ticket's plan read in the main pane and promoted to the implement queue"
         keys="[enter] · [p] · pick a status · [enter]" >}}

## What it is for

A run that finishes in a status no queue serves has come to rest at [a
gate](how-lerp-works.md#where-a-run-comes-to-rest). The stock pipeline
has two, Plan Review, where you read the plan, and In Review, where you
merge the pull request. Promoting is how a ticket leaves one, and so is
moving it in Linear. Either way the next pass carries it on, and lerp
never promotes a ticket for you.

## What it writes

The move and nothing else. That and
[force-start](starting-past-the-limit.md)'s claim are the only writes the
screen makes, and `o` opens the selected ticket in Linear for everything
else.

`p` drops out of the key line where there is no status to promote into.
In the picker, `↑`/`↓` pick a target, `enter` takes it, and `esc` or `q`
backs out without writing.

## Several at once

`v` on a row starts a visual-mode range, and the movement keys extend it.
`A` selects every shown row in one keystroke, scoped to whatever the
active project filter, search, or status slice has left visible. `esc` drops
the selection, and so does anything that reorders or narrows the rows it is
drawn over, sorting, scoping to a project, slicing or searching. `p` then
opens the picker once, for one target, and every selected ticket goes
through the same promote. One failing never stops the rest. The note says
how many made it, and a row that did not carries a `✗` until it promotes
cleanly or leaves the board.

## After the promote

A promoted ticket is [eligible](how-lerp-works.md#the-claim) the moment
it sits in a queue's status unassigned and unblocked, so the next pass
picks it up. If it does not,
[troubleshooting](troubleshooting.md#why-isnt-my-ticket-being-picked-up)
walks the three conditions.
