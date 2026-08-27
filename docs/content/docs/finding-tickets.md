---
title: Finding tickets
summary: Filtering, sorting, searching, and slicing to a status.
weight: 70
---

# Finding tickets

Four controls on the [Inbox panel](reading-the-board.md#the-inbox-panel) decide
what it shows and in what order: `F` filters by field and value, `s` sorts, `/`
searches, and `]`/`[` slice to a status. All four are session-only — no saved
views, no filter syntax, and none of them changes which tickets are fetched.

<!-- Cast slot (LERP-70): a full inbox narrowed — s through the sort modes,
     F onto one project, / typed into, ] cycling through status slices and
     back.
     keys: [F] · [P] · [s] · [/] · []] · [esc] -->

## Order

Rows are grouped by status by default, in an order derived from the pipeline
itself — the same order — so the run to retriage and the review to read are
the top rows rather than two of sixteen.

Within a group, rows fall through to **leverage**: how many other listed
tickets promoting this one would transitively unblock, then priority, then
identifier. The promote worth making is the top row of its group.

`s` cycles that to project, leverage or priority. The two grouping modes draw
a header per boundary — none, when every row is in the same group — and the
two flat ones order the whole list.

## Filter

`F` opens a two-step modal to filter the inbox: pick a field (project, status,
or priority), then pick a value. The value list displays row counts and includes
a type-ahead prompt to narrow the options. `enter` applies the filter; `esc`
backs out a level. `F` on an active filter reopens it for changing or clearing
(by choosing the `all <field>` option at the top of the list).

`P` is a shortcut straight to the project value list.

Picking a value under **status** sets the [status slice](#status-slices)
rather than a separate filter, so `F` and `]`/`[` are two ways of reaching one
control and can never narrow the panel to two different statuses at once.
Project and priority compose on top of whichever slice is showing.

## Search

`/` opens a prompt on the panel and narrows it as you type — a plain
case-insensitive substring over the identifier, title, status and project
already on the row, with the matches marked inside it.

`enter` keeps the filter and hands the keys back to the list, so you can
[promote](promoting.md) what you found; `esc` cancels the prompt, and `esc`
again clears a filter the prompt already closed on. While the prompt is open
it has the keyboard — a `p` or a `q` typed into it is text — and `ctrl+c`
still quits.

## Status slices

`]` and `[` cycle the panel through Linear status slices in board order — all,
then each Linear status present on the board, and back. Slices are display
over the fetched list, so only unstarted and active statuses present appear.

`F` → status is the same control with the options on screen: it lists every
status with its row count, so picking one is a choice rather than a guess at
where the cycle stops.

## What the title says

The panel title carries all of it — `● 4/17 · /goreleaser · by status ·
Backlog` — so a narrowed list is never mistaken for an empty board. Its count
is what this panel can show under the current slice; the status bar's `● n in
the inbox` is the other question, what is blocked on you, and does not move
when a slice is active.
