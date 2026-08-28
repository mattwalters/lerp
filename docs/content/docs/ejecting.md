---
title: Ejecting
summary: Taking a headless run over as your own interactive session.
weight: 90
---

# Ejecting

`e` on a running row in the
[Work panel](reading-the-board.md#the-work-panel) stops that agent, frees
the lane, and hands back the runner's own `resume` command — the headless
run becomes your interactive session, in the workspace lerp leaves
standing.

{{< cast webm="casts/eject.webm" mp4="casts/eject.mp4"
         title="Ejecting a running agent, showing the resume command and freeing the lane"
         keys="[e] · the resume command · [esc]" >}}

## What ejecting does not do

Nothing is written to Linear. The ticket keeps its claim and its status:
ejecting is taking the work over, not abandoning it, and the standing
claim is what keeps the next pass from starting the stage again underneath
you.

Nothing is disposed either. The workspace, git worktree included, is yours
to finish in and yours to remove.

## The resume command

The command is shown until you dismiss it, and it also lands in
`.lerp/loop.log`, so it survives the dismissal and the session.

It comes from the runner's `resume` line in [`lerp.toml`](lerp-toml.md),
built from the session ID lerp generated before the run started and the
workspace it provisioned. A runner with no `resume` cannot be ejected, so
`e` is not offered on its runs rather than failing when pressed.

## When to reach for it

Ejecting is the operator's call and nothing else's. A run's activity line
and sparkline say whether an agent is still working (see
[watching a run](watching-a-run.md)), but lerp sets no threshold on them
and never acts on one: there is no timeout that ejects a stalled run for
you.
