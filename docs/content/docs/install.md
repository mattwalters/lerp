---
title: Install
summary: Platforms, the three ways to get the binary, and the prerequisites lerp cannot satisfy for you.
weight: 30
---

# Install

Lerp runs on macOS and Linux. Windows is not supported and does not build
there: the loop holds an advisory `flock` on the clone, starts each agent in
its own process group, and reaps by killing that group — none of which
Windows has as such. Under WSL2 it is Linux as far as lerp is concerned, and
that is the Windows answer.

## Get the binary

With a Go toolchain:

```sh
go install github.com/mattwalters/lerp/cmd/lerp@latest
```

Without one, take a prebuilt binary — macOS and Linux, amd64 and arm64 —
from the [releases page](https://github.com/mattwalters/lerp/releases).
Every release tag — `v0.1.0` and the like — publishes an archive per
platform and a `checksums.txt` beside them.

Or from a clone:

```sh
git clone https://github.com/mattwalters/lerp
cd lerp
make install
```

`make install` builds with the version stamped from `git describe` and
installs into Go's bin dir — `GOBIN` when set, else `GOPATH/bin`. Whichever
route you took, `lerp version` prints what you got.

A clone also carries the gate and the release command. `make check` is
gofmt, vet, build and test, and CI runs that same target on Linux and macOS
rather than its own copy of the steps. `make release VERSION=v0.1.0` tags
merged main and pushes the tag; pushing a version tag is the only thing that
cuts a release, and the build itself happens in CI from a clean checkout of
the tag rather than from whatever was lying around on somebody's laptop.
[CONTRIBUTING.md](CONTRIBUTING.md) is the rest of that story.

## What else you need

Lerp speaks exactly one external API: Linear, and every command needs a
credential for it. Today that is a personal API key in the `LINEAR_API_KEY`
environment variable — create one in Linear's settings and export it. Beyond that key lerp itself needs
only Git, but the stock pipeline shells out to `claude`
([Claude Code](https://docs.claude.com/en/docs/claude-code)) as its runner
and its implementing prompt opens pull requests with `gh`, so install both
before the [quickstart](quickstart.md) reaches its first run.

Before the first unattended run, read
[SECURITY.md](SECURITY.md). Running lerp against a team gives everyone who
can move a ticket into a served status the ability to start an agent on your
machine; that page is the whole trust model in one place.

## Lerp needs the status field

The other prerequisite lerp cannot satisfy from here, in the same class as
the API key above. On the teams lerp serves, the status field is lerp's: a
queue *is* a status, and a stage finishes by moving the ticket to the next
one. Lerp is not privileged — it keeps whatever move it finds, because that
is how an agent escalates or refuses — so an automation that moves a ticket
*during* a stage takes that stage's own move away, and the queue's
`on_success` hop never happens.

Linear's GitHub integration is the one nearly everybody has. Its automations
are configured **per team**, under the team's workflow settings, as a list of
pull-request triggers each mapping to a status or to No action. On the teams
lerp serves:

- **On draft PR open**, **On PR open**, **On PR review request or
  activity** and **On PR ready for merge** all fire while a pull
  request is open, which for a ticket a queue is running is mid-stage.
  Set every one of them to **No action** — all four, not just the
  obvious one: the stock implement prompt opens its pull request as a
  draft and flips it to ready at the end, so a single run can trip any
  of them.
- **On PR merge** fires once the pull request has landed. Under the
  stock pipeline that is after lerp is finished with the ticket —
  moving a merged ticket to Done is the benign automation, and it is
  what carries "In Review" to the end, so leave it on. If your pipeline
  has a stage that runs *after* the merge, that trigger is mid-stage
  for you too: either set it to No action as well, or take the road
  less travelled below and point that stage's `on_success` at the
  status it moves to. This repo's own `lerp.toml` takes the second
  road, with a merge queue whose `on_success` is the Done the
  automation is already heading for.

It is a two-minute settings change, and it is scoped: teams lerp does not
serve are unaffected. Skip it and the failure is total rather than
intermittent — the implement stage's whole job is to open a pull request, so
"On PR open" moves its ticket out of Implementing before the run exits, and
every ticket, every time, stops short of review.

Lerp says what it can about this on its own. Every `lerp` start reads the
served teams' real automations and, before the board opens, names each
mid-stage one whose target status `lerp.toml` never mentions — what each
queue would lose to it, and the fix. That check is deliberately quiet about
an automation pointing at a status your config *does* name, because that is a
configuration somebody chose on purpose (below); the cost is that it cannot
tell a deliberate one from a rule aimed at the wrong named status. It is also
silent about the merge trigger by construction, which is the one case above
it cannot warn you about. So the settings screen is still worth the two
minutes. After the fact the symptom reaches the status bar from the run
itself, where a narrow terminal will truncate it — `.lerp/loop.log` has the
whole line:

> LERP-42 left "Implementing" for "In Progress" during its run — the
> on_success hop to "In Review" was skipped. "In Progress" is not a
> status your pipeline names; an external automation (e.g. Linear's
> GitHub integration) may be moving tickets.

**The road less travelled.** If you would rather keep an automation than turn
it off, point the pipeline at what it does: give the queue whose runs open
the pull request an `on_success` of the status the integration moves tickets
*to*, and that automation becomes the trigger for the next stage rather than
the thief of the last one's hop. That is the configuration the startup check
stays quiet about, for exactly this reason. It is not the stock config and it
is the harder road, in three ways: it couples your pipeline to integration
behaviour you do not control; a setting changed in Linear breaks the chain
with no diff to read; and the queue's `on_failure` route goes with it, since
the automation has already moved the ticket to the success status by the time
a failing run ends. A failed run then comes to rest at the success gate with
only a status-bar line to say so — and if a queue watches that status, the
next pass simply runs the following stage on the failed work.
