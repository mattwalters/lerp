---
title: Install
summary: Platforms, the ways to get the binary, and the prerequisites lerp cannot satisfy for you.
weight: 30
---

# Install

Lerp runs on macOS and Linux. Windows is not supported and does not build
there: the loop holds an advisory `flock` on the clone, starts each agent
in its own process group, and reaps by killing that group — none of which
Windows has as such. Under WSL2 it is Linux as far as lerp is concerned.

## Get the binary

With Homebrew:

```sh
brew install mattwalters/tap/lerp
```

With a Go toolchain:

```sh
go install github.com/mattwalters/lerp/cmd/lerp@latest
```

With neither, `install.sh` downloads the release binary for your OS and
arch, verifies it against the published checksum, and installs it to
`$HOME/.local/bin` — no sudo, no PATH edits:

```sh
curl -fsSL https://raw.githubusercontent.com/mattwalters/lerp/main/install.sh | sh
```

Prebuilt binaries — macOS and Linux, amd64 and arm64 — are on the
[releases page](https://github.com/mattwalters/lerp/releases), each tag
with a `checksums.txt` beside its archives. Or from a clone:

```sh
git clone https://github.com/mattwalters/lerp
cd lerp
make install
```

`make install` builds with the version stamped from `git describe` and
installs into Go's bin dir — `GOBIN` when set, else `GOPATH/bin`.
Whichever route you took, `lerp version` prints what you got.

A clone also carries the gate and the release command: `make check` is
gofmt, vet, build and test (CI runs the same target on Linux and macOS),
and `make release VERSION=v0.1.0` tags merged main and pushes the tag —
pushing a version tag is the only thing that cuts a release, and the build
happens in CI from a clean checkout of the tag.
[CONTRIBUTING.md](CONTRIBUTING.md) is the rest of that story.

## What else you need

Lerp speaks exactly one external API: Linear, and every command needs a
credential for it. The standard way is `lerp login`: run it once and it
signs you in to Linear via OAuth in your browser (`read,write` scopes,
never `admin`), storing an auto-renewing token in your user config
directory (`0600`). OAuth also gets Linear's higher rate limit: 5,000
requests/hour against 2,500/hour for personal API keys.

For headless environments and CI, `LINEAR_API_KEY` is the fallback: create
a personal API key in Linear's settings and export it. A personal key
defaults to full workspace access unless restricted at creation, and when
set it takes precedence over any stored OAuth token.

Beyond Linear credentials, lerp itself needs only Git — but the stock
pipeline shells out to `claude`
([Claude Code](https://docs.claude.com/en/docs/claude-code)) as its runner,
and its implementing prompt opens pull requests with `gh`, so install both
before the [quickstart](quickstart.md) reaches its first run.

Before the first unattended run, read [SECURITY.md](SECURITY.md). Running
lerp against a team gives everyone who can move a ticket into a served
status the ability to start an agent on your machine; that page is the
trust model in one place.

## Lerp needs the status field

The other prerequisite lerp cannot satisfy from here. On the teams lerp
serves, the status field is lerp's: a queue *is* a status, and a stage
finishes by moving the ticket to the next one. Lerp keeps whatever move it
finds — that is how an agent escalates or refuses — so an automation that
moves a ticket *during* a stage takes that stage's own move away, and the
queue's `on_success` hop never happens.

Linear's GitHub integration is the one nearly everybody has. Its
automations are configured **per team**, under the team's workflow
settings, as pull-request triggers each mapping to a status or to No
action. On the teams lerp serves:

- **On draft PR open**, **On PR open**, **On PR review request or
  activity** and **On PR ready for merge** all fire while a pull request
  is open — mid-stage, for a ticket a queue is running. Set all four to
  **No action**: the stock implement prompt opens its PR as a draft and
  flips it to ready at the end, so a single run can trip any of them.
- **On PR merge** fires after the stock pipeline is finished with the
  ticket. Moving a merged ticket to Done is the benign automation — it is
  what carries "In Review" to the end — so leave it on. If your pipeline
  has a stage that runs *after* the merge, this trigger is mid-stage for
  you too: set it to No action, or point that stage's `on_success` at the
  status the automation moves to (below). This repo's own `lerp.toml`
  does the latter, with a merge queue whose `on_success` is the Done the
  automation is already heading for.

It is a two-minute settings change, scoped to the teams lerp serves. Skip
it and the failure is total, not intermittent: the implement stage's whole
job is to open a pull request, so "On PR open" moves its ticket out of
Implementing before the run exits, and every ticket stops short of review.

Lerp says what it can on its own: every start reads the served teams'
real automations and, before the board opens, names each mid-stage one
whose target status `lerp.toml` never mentions — what each queue would
lose to it, and the fix. It stays quiet about an automation pointing at a
status your config *does* name (the deliberate configuration below), and
it cannot see the merge trigger at all — so the settings screen is still
worth the two minutes. After the fact, the symptom reaches the status bar
from the run itself; `.lerp/loop.log` has the whole line:

> LERP-42 left "Implementing" for "In Progress" during its run — the
> on_success hop to "In Review" was skipped. "In Progress" is not a
> status your pipeline names; an external automation (e.g. Linear's
> GitHub integration) may be moving tickets.

**Keeping an automation instead.** Point the pipeline at what it does:
give the queue whose runs open the pull request an `on_success` of the
status the integration moves tickets *to*, and the automation becomes the
trigger for the next stage instead of the thief of the last one's hop.
This is the configuration the startup check stays quiet about. It costs
three things: your pipeline is coupled to integration behaviour you do
not control; a setting changed in Linear breaks the chain with no diff to
read; and the queue's `on_failure` route is dead, since the automation
has already moved the ticket by the time a failing run ends — the failed
run rests at the success gate with only a status-bar line to say so, and
a queue watching that status runs the next stage on the failed work.
