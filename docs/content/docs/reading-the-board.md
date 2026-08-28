---
title: Reading the board
summary: The two panels, what each row says, and the main pane that opens beside them.
weight: 60
---

# Reading the board

```sh
lerp
```

`lerp` opens the TUI, and the TUI is the engine: the loop runs while it is
open, and there is no daemon.

Two panels share the screen, and lerp opens focused on **On you**: the
loop runs the board on its own, so the first look belongs to what needs
*you*. Both panels start with their pane closed and their table at full
width.

`1` and `2` choose a panel and `tab` cycles between them; `↑`/`↓` pick a
row. `?` opens the full key list at any time, and `q` quits.

{{< cast webm="casts/board.webm" mp4="casts/board.mp4"
         title="The board opening on the On you panel, switching to the work panel, and opening a lane's log"
         keys="[1] · [2] · [tab] · [enter] · [esc]" >}}

## The Work panel

The Work view is one list of what the machine is doing with the board,
grouped by queue: every ticket in each queue's status, in the loop's own
pickup order, the ones running now at the top of their group.

A running row carries its state — provisioning or running — the run's
elapsed time, and the tokens it has spent as its own log reports them,
plus a dollar figure where the runner's stream states one; no lerp price
table stands in for a runner silent on cost. Where the runner's log
distinguishes subagents and its `[runners.*]` block names a `context`
window (see [lerp.toml](lerp-toml.md)), the row adds a percentage — how
full the fullest agent in the run is — faint until 80%, then `⚠`; no
configured window, tokens only, never a guessed figure. A run inherited
from a previous `lerp` reads as `running` like any other, with the run's
own age and total read back from its log, not the stretch since adoption.
Under it, once the run has a log, a second line reads how the run is
going — see [watching a run](watching-a-run.md).

Claude settles cost only on the line that closes its log — as the row
disappears — so that figure shows up on the status bar's exit note, where
it survives the row.

A waiting row is faint, with the reason it waits: blocked or claimed.
`enter` on one shows where it sits in pickup order and what gates it.

The panel title and the status bar carry the capacity, `2/3 running` —
what says whether anything can start. Every live run counts against it,
whichever lane it landed on, with `· +1 over` beside it while more runs
are live than the limit allows.

Ordering is not a keystroke. To change what runs *next*, move tickets in
Linear; to run one now regardless, see
[starting past the limit](starting-past-the-limit.md).

## The On you panel

The On you panel lists what waits on a human: unclaimed tickets, and the
operator's own claimed tickets, sitting in a status no queue serves.

Above the column header sits the pinned slice tab row: `all` followed by
each Linear status present on the board, each with its ticket count. The
active tab is highlighted with the focus accent, while inactive tabs remain
faint. Slicing with `]` or `[` moves the focus across tabs to show only that
status; `all` returns to showing all active tickets (with the backlog folded).

Under the tab row sits the header naming the columns — identifier, leverage,
the real Linear status, project, priority, and the title, which takes
whatever width is left. The vocabulary is Linear's own, never a category
invented by lerp.

A status the pipeline never names — no queue's status, no `on_success` or
`on_failure` target — is marked, but only where Linear files it as
started: a ticket in a backlog, triage or Todo column has not entered the
pipeline, while one in a working status the pipeline knows nothing about
is the fingerprint of a ticket that left it. `?` spells out that mark and
the other two the table draws.

The panel opens on what is blocked on you — failed runs, finished runs,
tickets in statuses the pipeline never named — with the intake it never
left folded to one line at the foot: `28 waiting to enter the pipeline —
] to browse`. Being blocked-on is an interrupt; pulling from the backlog
is a sit-down motion; [finding tickets](finding-tickets.md) is that one,
with the sorting, scoping, slicing and searching. A ticket you have
claimed is never folded, wherever Linear files it: no pass can pick it up
while the claim stands, so it is blocked on you.

## The main pane

The list owns the screen until you ask for a ticket. `enter` on a row
reads it into a main pane beside the table — the body, where the plan
lives, and the comments, where a run's verdict lands — so a parked ticket
can be decided from one screen. `esc` closes it, and each panel remembers
whether its pane is open.

It is a read and stays one: nothing composes, replies, or navigates to
another ticket. `o` opens the ticket in Linear for everything else.

An open main pane is a surface in the `tab` cycle; while it holds the keys
its border says so, and `↑`/`↓` scroll it a line at a time.

## Colour

Colour marks state and never carries it alone: every state also has a
shape or a word, so what the screen says survives a 16-colour terminal and
a colour-blind operator. Which half of the palette you get, and how to
turn colour off, are [environment variables](cli.md#environment).
