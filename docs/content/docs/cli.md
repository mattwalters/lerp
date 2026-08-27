---
title: The command line
summary: The whole surface — `lerp`, `lerp login`, `lerp init`, `lerp version` — and the environment they read.
weight: 130
---

# The command line

```
usage:
  lerp [-concurrency N]         open the TUI; the loop runs while it is open
  lerp version, --version       print the version
  lerp login                    sign in to Linear (loopback OAuth); no flags
  lerp logout                   sign out of Linear and revoke the token; no flags
  lerp init --team KEY [--yes]  map lerp's queues onto the team's board and write this repo's lerp.toml
```

That is the whole surface. `lerp -h` prints it, subcommands included, rather
than only the flags.

## `lerp`

Bare `lerp` opens the board — [the screen, and why opening it is what runs
the loop](reading-the-board.md). It must be run from inside the Git
repository whose `lerp.toml` it should read.

`-concurrency N` caps how many agents run at once. The default is 10; each
lane is a whole workspace, so lower it in a repo whose `provision` command is
heavy. A value below 1 is refused.

The TUI needs a terminal: in a pipe or a script, `lerp` prints usage and
exits 2 rather than quietly starting to claim tickets. An engine run is an
operator's decision, made at a terminal.

Before the first pass, lerp refuses to start unless every status the config
names exists on its team — a misspelled queue status would otherwise poll as
a permanently empty queue rather than an error, and a missing `on_success`
target would fail only after a whole agent run. A team automation that would
move a ticket mid-stage is shown at the same moment but only warns; see
[Lerp needs the status field](install.md#lerp-needs-the-status-field).

An advisory lock at `.lerp/lock` keeps it to one loop per clone, so a second
`lerp` on the same clone fails on the lock rather than racing the first.

**Quitting.** `q` or `ctrl+c` closes the screen, stops future passes, and
waits briefly for a pass already in flight to settle. The agents are never
touched: they are their own processes, with run evidence on disk, and the
next `lerp` adopts them.

## `lerp init`

```sh
LINEAR_API_KEY=... lerp init --team LERP --team-name "Lerp"
```

| Flag | Meaning |
| --- | --- |
| `--team KEY` | the Linear team key — the ticket prefix, the LERP in LERP-42. Required. |
| `--team-name NAME` | display name, used only if the team must be created |
| `--yes` | take the stock answer to every question |

Init creates the Linear team if it is missing, fits the stock pipeline onto
the board through a short conversation, writes `lerp.toml`, and appends
`.lerp/` to the repository's `.gitignore`. [Quickstart](quickstart.md) walks
the whole of it.

It is safe to repeat: it creates only missing Linear structure, adds nothing
to `.gitignore` twice, and never replaces an existing `lerp.toml` — it
verifies that the existing config serves the requested team, and ensures the
statuses that config's queues name, instead.

Piped input implies `--yes`.

## `lerp version`

`--version` answers the same question. Prints the version the binary was
stamped with. A release archive carries its tag; `make install` stamps
`git describe`, dirty tree included, so a local build never claims to be a
release. A binary built without either of those — `go install
github.com/mattwalters/lerp/cmd/lerp@vX.Y.Z` chief among them — falls back to
the module version Go's own toolchain records in the binary: the requested
version for a `go install pkg@version` build, or a pseudo-version for a plain
`go build` inside a VCS checkout, `+dirty` appended if the tree had changes.
Only a build with no VCS info at all — `go build -buildvcs=false`, or a
source archive with no `.git` — reports the literal `dev`.

## Environment

| Variable | What it does |
| --- | --- |
| `LINEAR_API_KEY` | the Linear personal API key. Every command needs a credential, and this is one of two ways to hold one — set, it wins over the token `lerp login` stores. Personal API keys carry your full workspace access across every team (Linear has no per-team key scoping); create a dedicated Linear user account for automation if you need narrower scoping. Lerp drops it from its own environment after reading it, and never passes it to a provision, dispose or runner command. |
| `LERP_BACKGROUND` | `light` or `dark`, saying which half of the palette to draw. Read once at startup; any other value is an error rather than a shrug. |
| `NO_COLOR` | set to any value, turns colour off entirely. |

Which half of the palette you get is otherwise decided by asking the terminal
for its background colour, and a terminal that does not answer — tmux and
screen among them, and plenty of ssh and CI terminals — is read as dark.
That is what `LERP_BACKGROUND` is for.

Legibility is measured: a test computes the WCAG contrast ratio of every
colour in the palette against the light and dark backgrounds a terminal is
likely to have, and fails below the 4.5:1 floor for text, so a retune cannot
quietly cost it. It measures the colours as written, which is what a 24-bit
terminal draws; a terminal with fewer draws the nearest colour it has, and
that one can sit under the floor — a little under on 256 colours, further
under on 16.

Lerp sets variables of its own on the commands it starts: `LERP_LANE`,
`LERP_TICKET_ID` and `LERP_WORKSPACE` on a provision or dispose command, and
`LERP_TICKET` on a runner. Those are
[`lerp.toml`'s side](lerp-toml.md#workspace-commands) of the contract, not
this one.

## Cleanup and uninstall

Walking away from lerp cleanly takes two steps:

1. **Remove the binary:** `make uninstall` from a clone, or delete the binary from `$HOME/.local/bin` or your `GOBIN`.
2. **Remove local state:** `rm -rf .lerp/` once all agents have stopped. `.lerp/` holds run records, logs, and lane workspaces. Agents are separate process groups and outlive lerp, and run evidence is how the next `lerp` adopts or reaps them — so only delete `.lerp/` when no agents are live. Workspaces under `.lerp/workspaces/` are git worktrees whose registrations the stock `dispose` unwinds on exit; run `git worktree prune` to clean up any stray registrations.

Linear workflow statuses created by `lerp init` remain on your team until deleted or archived in Linear's settings.

<!-- Slot (LERP-111): the auth section — `lerp login`, `lerp logout`, the
     stored token, and how it and LINEAR_API_KEY relate. The commands exist
     (LERP-109) and `internal/credentials` resolves both sources
     (LINEAR_API_KEY first, then the token file, then an error naming both
     remedies); what is missing is the page saying so. -->
