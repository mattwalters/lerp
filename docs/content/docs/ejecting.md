---
title: Ejecting
summary: Taking a headless run over as your own interactive session.
weight: 90
---

# Ejecting

`e` on a running row in the [Work
panel](reading-the-board.md#the-work-panel) stops that agent, frees the
lane, and hands back the runner's own `resume` command. The headless run
becomes your interactive session, in the workspace lerp leaves standing.
Lerp never ejects anything on its own, so a stalled run waits for you.

{{< cast webm="casts/eject.webm" mp4="casts/eject.mp4"
         title="Ejecting a running agent, showing the resume command and freeing the lane"
         keys="[e] · the resume command · [esc]" >}}

## What stays

Nothing is written to Linear. The ticket keeps its claim and its status,
so the next pass will not start the stage again underneath you. Nothing
is disposed either. The workspace, git worktree included, is yours to
finish in and yours to remove.

## The resume command

The command shows until you dismiss it, and it also lands in
`.lerp/loop.log`, so it survives both the dismissal and the session.

It comes from the runner's `resume` line in [`lerp.toml`](lerp-toml.md),
built from the session ID lerp generated before the run started and the
workspace it provisioned. A runner with no `resume` cannot be ejected, so
`e` is not offered on its runs.
