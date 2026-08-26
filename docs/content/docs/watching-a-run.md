---
title: Watching a run
summary: The activity line, the live log tail, the raw view, and following.
weight: 100
---

# Watching a run

A run says how it is going twice: once on its row, in a line you get without
asking, and once in full when you open its log.

<!-- Cast slot (LERP-70): a lane's log tailing live — tool calls landing one
     line each, pgup into the scrollback, r for the raw output, end back to
     following.
     keys: [enter] · [pgup] · [r] · [end] · [esc] -->

## The activity line

Under a running row, once the run has a log, a second line reads how that run
is going: the last call the agent made — a shell command as `$ go test
./...`, anything else by tool and target — and a sparkline of its recent
activity, so a run that has fallen quiet reads as a flat line.

The line takes the width the row is given: on a wide terminal's full-width
list it draws back about a quarter of an hour, and beside an open main pane
it shows the recent end of that same history. A cell is fifteen seconds
wherever it is drawn, so a narrow row reaches less far back rather than
reading more coarsely.

Those are numbers to read, not a timeout. Lerp sets no threshold on them and
never acts on one; [ejecting](ejecting.md) a run that has stopped making
progress stays the operator's call.

## The log

`enter` on a running ticket opens a live tail of its log in the main pane,
with scrollback that survives the run's exit, and `esc` gives the screen
back.

The tail reads as agent activity rather than as bytes: tool calls one line
each, prose as prose, and thinking collapsed to a single line with its token
count. A runner whose output lerp does not recognize is shown exactly as it
was written, with no configuration.

`pgup`/`pgdn` scroll it, `end` resumes following, and `r` toggles the pane to
the runner's raw output and back. The log on disk is untouched either way —
the raw view is the same file, undecorated.

## What the loop itself is doing

Agent output goes to each run's own log, which is what the pane tails. The
loop's own diagnostics — provision, dispose, adopt, reap — go somewhere else:
`.lerp/loop.log`, appended to across sessions. That is where to look when a
run never started rather than when a run is going badly.
