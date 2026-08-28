---
title: The command line
summary: The whole surface — `lerp`, `lerp login`, `lerp logout`, `lerp init`, `lerp version` — and the environment they read.
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

## `lerp login`

```sh
lerp login
```

Signs in to Linear via OAuth 2.0 with PKCE (RFC 8252). It opens an ephemeral
loopback listener on `127.0.0.1`, launches your browser to Linear's
authorization page, catches the redirect callback, and saves the resulting
token pair. It takes no flags.

Login requests only `read,write` permissions (`actor=user`), never `admin`.
The token is written to `token.json` under your user config directory
(`~/.config/lerp/token.json` on Linux, `~/Library/Application Support/lerp/token.json`
on macOS) with file mode `0600` inside a `0700` directory. The stored access
token auto-renews before expiry.

## `lerp logout`

```sh
lerp logout
```

Signs out of Linear: sends a best-effort revocation request to Linear's token
revocation endpoint and deletes the local `token.json` file. It takes no flags.

## `lerp init`

```sh
lerp init --team LERP --team-name "Lerp"
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

## Authentication

Every lerp command needs a Linear credential. Lerp resolves credentials in
this order:

1. **`LINEAR_API_KEY` environment variable:** if set, lerp uses this
   personal API key directly and skips reading the token file.
2. **Stored OAuth token:** loaded from `token.json` in your user config
   directory, saved by `lerp login`.
3. **No credentials:** exits with an error directing you to run `lerp login`
   or export `LINEAR_API_KEY`.

`lerp login` is the recommended path: OAuth tokens are scoped (`read,write`,
no `admin`) and expiring, and OAuth clients receive Linear's 5,000
requests/hour rate limit. Personal API keys in `LINEAR_API_KEY` default to
full workspace access, never expire, and share Linear's 2,500 requests/hour
personal key limit.

Credentials are used only by lerp itself: lerp never passes stored OAuth
tokens to child processes (`provision`, `dispose`, or runners), and drops
`LINEAR_API_KEY` from its environment after reading it.

## Environment

| Variable | What it does |
| --- | --- |
| `LINEAR_API_KEY` | the Linear personal API key. Supported as a fallback for headless servers and CI runners. When set, it takes precedence over the token `lerp login` stores. A personal API key defaults to your full workspace access unless restricted at creation. Lerp drops it from its own environment after reading it, and never passes it to a provision, dispose or runner command. |
| `LERP_BACKGROUND` | `light` or `dark`, saying which half of the palette to draw. Read once at startup; any other value is an error rather than a shrug. |
| `LERP_OPEN` | `desktop` to rewrite ticket URLs to Linear's `linear://` deep-link scheme on `o`, opening directly in Linear's desktop app instead of bouncing through the browser. Unset or any other value keeps the default `https://` behavior. |
| `NO_COLOR` | set to any value, turns colour off entirely. |
| `XDG_STATE_HOME` | overrides where local [telemetry](telemetry.md) is written (`$XDG_STATE_HOME/lerp/runs.jsonl`, defaulting to `~/.local/state/lerp/runs.jsonl`). |

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

Walking away from lerp cleanly takes three steps:

1. **Remove the binary:** `make uninstall` from a clone, or delete the binary from `$HOME/.local/bin` or your `GOBIN`.
2. **Remove stored credentials:** `lerp logout` (or delete `token.json` from your user config directory) to revoke and remove local OAuth tokens.
3. **Remove local state:** `rm -rf .lerp/` once all agents have stopped. `.lerp/` holds run records, logs, and lane workspaces. Agents are separate process groups and outlive lerp, and run evidence is how the next `lerp` adopts or reaps them — so only delete `.lerp/` when no agents are live. Workspaces under `.lerp/workspaces/` are git worktrees whose registrations the stock `dispose` unwinds on exit; run `git worktree prune` to clean up any stray registrations. To clear run metrics too, remove `$XDG_STATE_HOME/lerp/runs.jsonl` (or `~/.local/state/lerp/runs.jsonl`).

Linear workflow statuses created by `lerp init` remain on your team until deleted or archived in Linear's settings.
