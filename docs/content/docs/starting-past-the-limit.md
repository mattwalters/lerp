---
title: Starting past the limit
summary: "`S` runs a queued ticket now, overriding the lane count and nothing else."
weight: 110
---

# Starting past the limit

Select a queued ticket in the [Work
panel](reading-the-board.md#the-work-panel) and press `S`. It starts now,
past the lane limit.

{{< cast webm="casts/force-start.webm" mp4="casts/force-start.mp4"
         title="Force-starting a queued ticket past the lane limit with S"
         keys="[S]" >}}

## What it overrides

The lane count and nothing else. The claim protocol still runs, so a
blocked ticket, or one somebody else has claimed, is refused with the
reason. `S` is a way around capacity, not around
[eligibility](how-lerp-works.md#the-claim).

Your own claim is the exception it takes over. That is how you re-run a
ticket whose claim outlived its run, whether a human assigned it or a run
died where no lerp was watching, and how you re-run a failed stage whose
queue has no `on_failure` route, since such a run keeps its claim and
waits on you.

Every live run counts against the capacity in the panel title, so a
force-start reads as `· +1 over` until one finishes.

## Not an ordering key

`S` runs this ticket now and does not reorder the queue. To change what
runs next, move tickets in Linear.
