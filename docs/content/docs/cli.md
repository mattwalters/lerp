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

That is the whole surface. `lerp -h` prints it, subcommands included.

## `lerp`

Bare `lerp` opens the board — [the screen, and why opening it is what runs
the loop](reading-the-board.md). Run it from inside the Git repository
whose `lerp.toml` it should read.

`-concurrency N` caps how many agents run at once. The default is 10;
each lane is a whole workspace, so lower it in a repo whose `provision`
command is heavy. A value below 1 is refused.

The TUI needs a terminal: in a pipe or a script, `lerp` prints usage and
exits 2 rather than quietly starting to claim tickets. An engine run is an
operator's decision, made at a terminal.

Before the first pass, lerp refuses to start unless every status the
config names exists on its team — a misspelled queue status would
otherwise poll as a permanently empty queue, and a missing `on_success`
target would fail only after a whole agent run. A team automation that
would move a ticket mid-stage is shown at the same moment but only
warns. See
[Why did my ticket skip a stage?](troubleshooting.md#why-did-my-ticket-skip-a-stage).

An advisory lock at `.lerp/lock` keeps it to one loop per clone: a second
`lerp` on the same clone fails on the lock rather than racing the first.

**Quitting.** `q` or `ctrl+c` closes the screen, stops future passes, and
waits briefly for a pass in flight to settle. The agents are never
touched: they are their own processes, with run evidence on disk, and the
next `lerp` adopts them.

## `lerp login`

```sh
lerp login
```

Signs in to Linear via OAuth 2.0 with PKCE (RFC 8252): an ephemeral
loopback listener on `127.0.0.1`, your browser to Linear's authorization
page, and the token pair saved on redirect. No flags.

Login requests only `read,write` permissions (`actor=user`), never
`admin`. The token is written to `token.json` under your user config
directory (`~/.config/lerp/token.json` on Linux,
`~/Library/Application Support/lerp/token.json` on macOS), file mode
`0600` inside a `0700` directory. The stored access token auto-renews
before expiry.

## `lerp logout`

```sh
lerp logout
```

Sends a best-effort revocation request to Linear's token revocation
endpoint and deletes the local `token.json`. No flags.

## `lerp init`

```sh
lerp init --team LERP --team-name "Lerp"
```

| Flag | Meaning |
| --- | --- |
| `--team KEY` | the Linear team key — the ticket prefix, the LERP in LERP-42. Required. |
| `--team-name NAME` | display name, used only if the team must be created |
| `--yes` | take the stock answer to every question |

Init creates the Linear team if it is missing, fits the stock pipeline
onto the board through a short conversation, writes `lerp.toml`, and
appends `.lerp/` to the repository's `.gitignore`.
[Quickstart](quickstart.md) walks the whole of it.

It is safe to repeat: it creates only missing Linear structure, adds
nothing to `.gitignore` twice, and never replaces an existing `lerp.toml` —
it verifies the existing config serves the requested team and ensures the
statuses its queues name, instead.

Piped input implies `--yes`.

## `lerp version`

Prints the version the binary was stamped with (`--version` answers the
same question). A release archive carries its tag; `make install` stamps
`git describe`, dirty tree included, so a local build never claims to be
a release. A binary built without either falls back to the module version
Go's toolchain records: the requested version for a
`go install pkg@version` build, a pseudo-version (`+dirty` if the tree
had changes) for a plain `go build` in a VCS checkout. Only a build with
no VCS info at all — `go build -buildvcs=false`, or a source archive with
no `.git` — reports the literal `dev`.

When the local update cache knows a newer release tag, `lerp version`
prints a second line pointing to `brew upgrade lerp`. It is read-only and
never makes network requests of its own; it reads only what a previous
board session cached.

## Authentication

Every lerp command needs a Linear credential, resolved in this order:

1. **`LINEAR_API_KEY`**: if set, lerp uses this personal API key directly
   and skips the token file.
2. **Stored OAuth token**: `token.json` in your user config directory,
   saved by `lerp login`.
3. **Neither**: exit with an error directing you to `lerp login` or
   `LINEAR_API_KEY`.

`lerp login` is the recommended path: OAuth tokens are scoped
(`read,write`, no `admin`) and expiring, and OAuth clients get Linear's
5,000 requests/hour rate limit. Personal API keys default to full
workspace access, never expire, and share the 2,500 requests/hour
personal limit.

Credentials are used only by lerp itself: stored OAuth tokens are never
passed to child processes (`provision`, `dispose`, or runners), and
`LINEAR_API_KEY` is dropped from lerp's environment after reading.

## Environment

| Variable | What it does |
| --- | --- |
| `LINEAR_API_KEY` | the Linear personal API key, supported as a fallback for headless servers and CI. When set, it takes precedence over the token `lerp login` stores. Defaults to your full workspace access unless restricted at creation. Dropped from lerp's own environment after reading, never passed to a provision, dispose or runner command. |
| `LERP_BACKGROUND` | `light` or `dark`, saying which half of the palette to draw. Read once at startup; any other value is an error. |
| `LERP_NO_UPDATE_CHECK` | any non-empty value disables the daily anonymous GitHub releases check for newer versions. |
| `LERP_OPEN` | `desktop` rewrites ticket URLs to Linear's `linear://` deep-link scheme on `o`, opening the desktop app instead of the browser. Unset or any other value keeps `https://`. |
| `NO_COLOR` | any value turns colour off entirely. |
| `XDG_STATE_HOME` | overrides where [telemetry](telemetry.md) is written (`$XDG_STATE_HOME/lerp/runs.jsonl`, defaulting to `~/.local/state/lerp/runs.jsonl`). |

Which half of the palette you get is otherwise decided by asking the
terminal for its background colour; a terminal that does not answer —
tmux and screen among them, and plenty of ssh and CI terminals — is read
as dark. That is what `LERP_BACKGROUND` is for.

Legibility is measured: a test computes the WCAG contrast ratio of every
palette colour against likely light and dark backgrounds and fails below
the 4.5:1 text floor, so a retune cannot quietly cost it. It measures the
colours as written — what a 24-bit terminal draws; a terminal with fewer
colours draws the nearest it has, which can sit under the floor.

Lerp sets variables of its own on the commands it starts: `LERP_LANE`,
`LERP_TICKET_ID` and `LERP_WORKSPACE` on a provision or dispose command,
and `LERP_TICKET` on a runner. Those are
[`lerp.toml`'s side](lerp-toml.md#workspace-commands) of the contract.

## Cleanup and uninstall

Walking away cleanly takes three steps:

1. **Remove the binary:** `brew uninstall lerp` for a brew install,
   `make uninstall` from a clone, or delete the binary from
   `$HOME/.local/bin` or your `GOBIN`.
2. **Remove stored credentials:** `lerp logout` (or delete `token.json`
   from your user config directory).
3. **Remove local state:** `rm -rf .lerp/` once all agents have stopped —
   agents outlive lerp, and run evidence is how the next `lerp` adopts or
   reaps them, so only delete it when none are live. Workspaces under
   `.lerp/workspaces/` are git worktrees; `git worktree prune` cleans up
   stray registrations. To clear run metrics too, remove
   `$XDG_STATE_HOME/lerp/runs.jsonl` (or
   `~/.local/state/lerp/runs.jsonl`).

Linear workflow statuses created by `lerp init` remain on your team until
deleted or archived in Linear's settings.
