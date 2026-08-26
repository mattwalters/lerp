---
title: Finding tickets
summary: Sorting, scoping to a project, searching, and unfolding the backlog.
weight: 70
---

# Finding tickets

Four keys on the [Inbox panel](reading-the-board.md#the-inbox-panel) decide
what it shows and in what order: `s` sorts, `P` scopes to a project, `/`
searches, and `B` unfolds the backlog. All four are session-only — no saved
views, no filter syntax, and none of them changes which tickets are fetched.

<!-- Cast slot (LERP-70): a full inbox narrowed — s through the sort modes,
     P onto one project, / typed into, B unfolding the backlog and folding
     it back.
     keys: [s] · [P] · [/] · [B] · [esc] -->

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

## Scope

`P` scopes the panel to one project and cycles back to all. It drops out of
the key line on a list with no project in it, because a key that does nothing
costs one that does.

## Search

`/` opens a prompt on the panel and narrows it as you type — a plain
case-insensitive substring over the identifier, title, status and project
already on the row, with the matches marked inside it.

`enter` keeps the filter and hands the keys back to the list, so you can
[promote](promoting.md) what you found; `esc` cancels the prompt, and `esc`
again clears a filter the prompt already closed on. While the prompt is open
it has the keyboard — a `p` or a `q` typed into it is text — and `ctrl+c`
still quits.

## The backlog

`B` unfolds the summary line at the foot of the table into the rows it stands
for, under their own status header, and folds them back.

## What the title says

The panel title carries all of it — `● 4/17 · /goreleaser · by status ·
backlog` — so a narrowed list is never mistaken for an empty board. Its count
is what this panel can show under the current fold; the status bar's `● n in
the inbox` is the other question, what is blocked on you, and does not move
when `B` opens the fold.
