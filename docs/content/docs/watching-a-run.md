---
title: Watching a run
summary: The activity line, the live log tail, the raw view, and following.
weight: 80
---

# Watching a run

A run reports twice, on its row and in its log.

{{< cast webm="casts/watching.webm" mp4="casts/watching.mp4"
         title="A live log tail in the main pane: scrollback, raw output, and following"
         keys="[enter] · [pgup] · [r] · [end] · [esc]" >}}

## The row

A running row carries its state, elapsed time, tokens spent, and a dollar
figure when the runner's own stream reports one.

A run adopted from a previous `lerp` reads like any other, with its age
and totals read back from its log rather than the stretch since adoption.

## The activity line

Under a running row, once the run has a log, a second line reads the last
call the agent made, a shell command as `$ go test ./...` and anything
else by tool and target, then a sparkline of recent activity. A run that
has fallen quiet reads as a flat line, and an adopted run draws the flat
line it earned rather than a fresh one.

These are numbers to read, not a timeout. Lerp sets no threshold and
never acts on one, and [ejecting](ejecting.md) a stalled run stays your
call.

## The log

`enter` on a running ticket opens a live tail in the main pane, with
scrollback that survives the run's exit, and `esc` gives the screen back.
`pgup` and `pgdn` scroll, `end` resumes following, and `r` toggles the
raw output the runner wrote.

The tail reads as activity rather than bytes, one line per tool call,
prose as prose, thinking collapsed to a line with its token count. Output
lerp does not recognize is shown exactly as written. The log on disk is
untouched either way.

## The loop's own log

Agent output goes to each run's log, which is what the pane tails. The
loop's diagnostics, provision, dispose, adopt and reap, go to
`.lerp/loop.log`, appended across sessions. Look there when a run never
started, rather than when a run is going badly.
