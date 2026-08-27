---
title: Reading the board
summary: The two panels, what each row says, and the main pane that opens beside them.
weight: 60
---

# Reading the board

```sh
LINEAR_API_KEY=... lerp
```

`lerp` opens the TUI, and the TUI is the engine: the loop runs while it is
open, and there is no daemon.

Two panels share the screen, and lerp opens focused on the **Inbox**: the
loop runs the board on its own, so what is worth the first look is what
needs *you*. The list owns that screen — both panels start with their pane
closed, so each table opens at full width — and `2` moves to the **Work**
panel.

`1` and `2` choose a panel and `tab` cycles between them; `↑`/`↓` pick a row.
`?` opens the full key list at any time, and `q` quits.

<!-- Cast slot (LERP-70): the board opening on the inbox, 2 across to the
     work panel, enter into a lane's log, esc back out.
     keys: [1] · [2] · [tab] · [enter] · [esc] -->

## The Work panel

The Work view is one list of what the machine is doing with the board,
grouped by queue: every ticket sitting in each queue's status, in the loop's
own pickup order, with the ones running now at the top of their own group.

A running row carries its state — provisioning or running — the run's elapsed
time, and the tokens it has spent, as its own log reports them. A run
inherited from a previous `lerp` reads as `running` like any other, and
carries the run's own age and the run's own total, not the stretch since it
was adopted: the log it has already written is the evidence, and lerp reads
it back rather than starting the count over. Under it, once the run has a
log, a second line reads how that run is going — see [watching a
run](watching-a-run.md).

A waiting row is shown faint with the reason it waits, blocked or claimed.
`enter` on one shows where it sits in pickup order and what gates it.

The panel title and the status bar carry the capacity, `2/3 running`, which
is what says whether anything can start — every live run counts against it,
whichever lane it landed on, with `· +1 over` beside it while more runs are
live than the limit allows.

Ordering is not a keystroke. To change what runs *next*, move tickets in
Linear; to run one now regardless, see
[starting past the limit](starting-past-the-limit.md).

## The Inbox panel

The Inbox view lists what waits on a human: unclaimed tickets, and the
operator's own claimed tickets, sitting in a status no queue serves. It is a
table, one row per ticket, under a header naming its columns — the
identifier, the leverage, the real Linear status, the project, the priority,
and then the title, which takes whatever the panel has left. The vocabulary
is Linear's own, never a category invented by lerp.

A status the configured pipeline never names — neither a queue's status nor
any `on_success` or `on_failure` target — is marked, but only where Linear
files it as started: a ticket resting in a backlog, a triage or a Todo column
has not entered the pipeline, which is the ordinary state of most of a board,
while one moved into a status that means work is under way by something the
pipeline knows nothing about is the fingerprint of a ticket that left it. `?`
spells out that mark and the other two the table draws.

The panel opens on what is blocked on you — where runs fail, where they
finish, and the statuses the pipeline never named the ticket into — with the
intake it never left folded to one line at the foot of the table: `28 waiting
to enter the pipeline — B to browse`. Being blocked-on is an interrupt, while
pulling from the backlog is a sit-down motion, so only one of them owns the
default view; [finding tickets](finding-tickets.md) is the other one, and the
sorting, scoping and searching that go with it. A ticket you have claimed
resting in an intake status is never folded: no pass can pick it up again
while the claim stands, so it is blocked on you wherever Linear files it.

## The main pane

The list owns the screen until you ask for a ticket. Selecting a row and
pressing `enter` reads it into a main pane that opens beside the table and
closes again with `esc` — its body, where the plan lives, and the comments on
it, the verdict a run left behind, so a parked ticket can be decided from
that one screen. Each panel remembers its own answer about whether the pane
is open.

That is a read and stays one: nothing composes, replies, or navigates on to
another ticket, and `o` opens the ticket in Linear for everything else.

An open main pane is a surface in the `tab` cycle, and while it holds the
keys its border says so and `↑`/`↓` scroll it a line at a time.

## Colour

Colour marks state and never carries it alone: every state also has a shape
or a word, so what the screen *says* survives a 16-colour terminal and a
colour-blind operator. Which half of the palette you get, and how to turn
colour off, are [environment variables](cli.md#environment).
