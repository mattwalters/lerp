---
title: Starting past the limit
summary: "`S` runs a queued ticket now, overriding the lane count and nothing else."
weight: 110
---

# Starting past the limit

Select a queued ticket in the [Work panel](reading-the-board.md#the-work-panel)
and press `S`: it starts now, past the lane limit.

{{< cast webm="casts/force-start.webm" mp4="casts/force-start.mp4"
         title="Force-starting a queued ticket past the lane limit with S"
         keys="[S]" >}}

## What it overrides, and what it does not

Force-start overrides the lane count and nothing else. The claim protocol
still runs, so a blocked ticket, or one somebody else has claimed, is refused
with the reason — `S` is not a way around
[eligibility](the-board.md#the-claim), only around capacity.

Your own claim is the exception it takes over. That is how a ticket left
claimed by a run nothing was left to reap gets run again, and it is the fix
for both of the ways a claim outlives its run: a human who assigned the
ticket, and a run that died where no lerp was watching. It is also how to
re-run a failed stage whose queue has no `on_failure` route, since such a run
keeps its claim and waits on you.

Every live run counts against the capacity in the panel title, whichever lane
it landed on, so a force-start shows up there as `· +1 over` until one
finishes.

## Not an ordering key

`S` runs *this* ticket now; it does not reorder the queue. To change what
runs next, move tickets in Linear — pickup order is the board's, and lerp
does not keep a queue of its own to shuffle.
