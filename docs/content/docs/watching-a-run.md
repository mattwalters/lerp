---
title: Watching a run
summary: The activity line, the live log tail, the raw view, and following.
weight: 80
---

# Watching a run

A run says how it is going twice: once on its row, in a line you get
without asking, and once in full when you open its log.

{{< cast webm="casts/watching.webm" mp4="casts/watching.mp4"
         title="A live log tail in the main pane: scrollback, raw output, and following"
         keys="[enter] · [pgup] · [r] · [end] · [esc]" >}}

## The activity line

Under a running row, once the run has a log, a second line reads how the
run is going: the last call the agent made — a shell command as `$ go test
./...`, anything else by tool and target — and a sparkline of recent
activity, so a run that has fallen quiet reads as a flat line.

A run adopted from a previous `lerp` gets the same line, not a fresh one:
its log's timestamps are read back into the history, so a run that went
quiet twenty minutes ago draws the long flat line it earned. A log that
dates nothing draws the short line of a fresh run.

The line takes the width the row is given: a wide terminal's full-width
list draws back about a quarter of an hour; beside an open main pane it
shows the recent end of the same history. A cell is fifteen seconds
wherever it is drawn, so a narrow row reaches less far back rather than
reading more coarsely.

These are numbers to read, not a timeout. Lerp sets no threshold and never
acts on one; [ejecting](ejecting.md) a run that has stopped making
progress stays the operator's call.

## The log

`enter` on a running ticket opens a live tail of its log in the main pane,
with scrollback that survives the run's exit; `esc` gives the screen back.

The tail reads as agent activity rather than bytes: tool calls one line
each, prose as prose, thinking collapsed to a single line with its token
count. A runner whose output lerp does not recognize is shown exactly as
written, with no configuration.

`pgup`/`pgdn` scroll, `end` resumes following, and `r` toggles the pane to
the runner's raw output and back. The log on disk is untouched either way —
the raw view is the same file, undecorated.

## What the loop itself is doing

Agent output goes to each run's own log, which is what the pane tails. The
loop's diagnostics — provision, dispose, adopt, reap — go to
`.lerp/loop.log`, appended to across sessions. Look there when a run never
started, rather than when a run is going badly.
