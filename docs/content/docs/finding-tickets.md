---
title: Finding tickets
summary: Filtering, sorting, searching, and slicing to a status.
weight: 70
---

# Finding tickets

Four controls on the [On you panel](reading-the-board.md#the-on-you-panel)
decide what it shows and in what order: `F` filters by field and value,
`s` sorts, `/` searches, and `]`/`[` slice to a status. All four are
session-only — no saved views, no filter syntax — and none of them changes
which tickets are fetched.

{{< cast webm="casts/finding.webm" mp4="casts/finding.mp4"
         title="Narrowing the list: sorting, filtering by project, searching, and cycling status slices"
         keys="[F] · [P] · [s] · [/] · []] · [esc]" >}}

## Order

Rows are grouped by status by default, in the pipeline's own order, so the
run to retriage and the review to read are the top rows rather than two of
sixteen.

Within a group, rows fall through to **leverage** — how many other listed
tickets promoting this one would transitively unblock — then priority,
then identifier. The promote worth making is the top row of its group.

`s` cycles the order to project, leverage or priority. The two grouping
modes draw a header per boundary; the two flat ones order the whole list.

## Filter

`F` opens a two-step modal: pick a field (project, status, or priority),
then pick a value. The value list shows row counts and takes type-ahead.
`enter` applies; `esc` backs out a level. `F` on an active filter reopens
it for changing or clearing (the `all <field>` option at the top).

`P` is a shortcut straight to the project value list.

Picking a value under **status** sets the [status slice](#status-slices)
rather than a separate filter, so `F` and `]`/`[` are two ways to reach
one control and can never narrow the panel to two different statuses at
once. Project and priority compose on top of whichever slice is showing.

## Search

`/` opens a prompt (`filter the list`) and narrows the panel as you type — a case-insensitive
substring over the identifier, title, status and project already on the
row, with matches marked.

`enter` keeps the filter and hands the keys back to the list, so you can
[promote](promoting.md) what you found; `esc` cancels the prompt, and
`esc` again clears a filter the prompt already closed on. While the prompt
is open it has the keyboard — a `p` or a `q` typed into it is text — and
`ctrl+c` still quits.

## Status slices

`]` and `[` cycle the panel through Linear status slices in board order —
all, then each status present, and back. The active tab is highlighted in
the slice tab row. Slices are display over the fetched list, so only
unstarted and active statuses present appear.

`F` → status is the same control with the options on screen: every status
with its row count, so picking one is a choice rather than a guess at
where the cycle stops.

## What the title and tabs say

The slice tab row and panel title carry all of it: the active tab shows a
fraction (`4/17`, for example) when narrowed by search or project filter,
and the title shows any active search (`· /goreleaser`), non-default sort
(`· by priority`), or project filter. The status bar's `● n on you` is the
other question — what is blocked on you — and does not move when a slice
is active.
