---
title: Reading the board
summary: The two panels, what each row says, and the main pane that opens beside them.
weight: 60
---

# Reading the board

```sh
lerp
```

The TUI is the engine. The loop runs while the screen is open, and there
is no daemon.

Two panels share it. **On you** lists what waits on a human, **Work**
lists what the machine is doing, and lerp opens focused on On you.

`1` and `2` choose a panel, `tab` cycles, `↑`/`↓` pick a row, `?` lists
every key, and `q` quits.

{{< cast webm="casts/board.webm" mp4="casts/board.mp4"
         title="The board opening on the On you panel, switching to the work panel, and opening a lane's log"
         keys="[1] · [2] · [tab] · [enter] · [esc]" >}}

## The Work panel

One list, grouped by queue, in the loop's pickup order, with the running
tickets at the top of each group.

A running row carries its state and [what the run is
doing](watching-a-run.md). A waiting row is faint and says why it waits,
blocked or claimed. `enter` on one shows where it sits in pickup order
and what gates it.

The panel title and the status bar carry capacity, `2/3 running`, with
`· +1 over` while more runs are live than the limit allows.

Ordering is not a keystroke. To change what runs next, move tickets in
Linear, or [start one past the limit](starting-past-the-limit.md).

## The On you panel

Unclaimed tickets, and your own claimed tickets, sitting in a status no
queue serves. The columns are identifier, leverage, the Linear status,
project, priority and title, in Linear's own vocabulary.

A tab row above the columns slices the list to one status, each tab with
its count. `]` and `[` move across it, and `all` comes back.

It opens on what is blocked on you, with the tickets that never entered
the pipeline folded into one line at the foot. [Finding
tickets](finding-tickets.md) covers browsing those. A claimed ticket is
never folded, since no pass can pick it up while the claim stands.

A ticket in a working status your pipeline never names is marked, the
fingerprint of a ticket that left the pipeline. `?` spells out that mark
and the other two.

## The main pane

`enter` reads the selected ticket into a pane beside the table, the body
where the plan lives and the comments where a run's verdict lands. `esc`
closes it. The pane is a read and stays one, so `o` opens the ticket in
Linear for anything else. `↑`/`↓` scroll it while it holds the keys.

## Colour

Colour marks state and never carries it alone. Every state has a shape or
a word too, so the screen survives a 16-colour terminal and a
colour-blind operator. [Environment variables](cli.md#environment) pick the
palette or turn it off.
