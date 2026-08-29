---
title: Finding tickets
summary: Filtering, sorting, searching, and slicing to a status.
weight: 70
---

# Finding tickets

Five keys narrow the [On you
panel](reading-the-board.md#the-on-you-panel). All of them are
session-only, and none changes which tickets the pass fetched.

| Key | What it does |
| --- | --- |
| `F` | Filter by project, status or priority, in a two-step modal. The value list carries row counts and takes type-ahead. `enter` applies, `esc` backs out a level, and `F` on an active filter reopens it to change or clear. |
| `P` | Straight to the project value list. |
| `s` | Cycle the sort through status, project, leverage and priority. |
| `/` | Search as you type, a case-insensitive substring over the identifier, title, status and project on the row, with matches marked. |
| `]` `[` | Slice to one status, in board order, and back to all. |

{{< cast webm="casts/finding.webm" mp4="casts/finding.mp4"
         title="Narrowing the list: sorting, filtering by project, searching, and cycling status slices"
         keys="[F] · [P] · [s] · [/] · []] · [esc]" >}}

## Order

Rows group by status, in the pipeline's own order. Within a group they
fall through to **leverage**, how many other listed tickets promoting
this one would transitively unblock, then priority, then identifier.
Status and project group with a header at each boundary, while leverage
and priority order the whole list flat.

## How they combine

Project and priority compose on top of whichever status slice is showing.
Picking a value under status sets that slice rather than a second filter,
so `F` and `]`/`[` can never narrow the panel to two statuses at once.
Slices are display over the list the pass fetched, so only the unstarted
and active statuses present appear.

## The search prompt

`enter` keeps the filter and hands the keys back, so you can
[promote](promoting.md) what you found. `esc` cancels the prompt, and
`esc` again clears a filter the prompt already closed on. While the
prompt is open it owns the keyboard, so a `p` typed into it is text, and
`ctrl+c` still quits.

## What the screen says

The active tab shows a fraction like `4/17` when a search or a project
filter narrows it. The title shows an active search (`· /goreleaser`), a
non-default sort (`· by priority`), or a project filter. The status bar's
`● n on you` does not move when a slice is active, since it answers the
other question, what is blocked on you.
