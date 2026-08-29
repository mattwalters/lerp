# lerp

[![ci](https://github.com/mattwalters/lerp/actions/workflows/ci.yml/badge.svg)](https://github.com/mattwalters/lerp/actions/workflows/ci.yml)

![The lerp board: tickets waiting on a human in the On you panel, a work panel of queues and three lanes running coding agents beneath it, and a main pane that opens beside them to read a ticket or tail a lane's log](docs/demo.gif)

<sub>Recorded from [`docs/tapes/demo.tape`](docs/tapes/demo.tape) against a fake
board and a stub agent; `make demo` regenerates it.</sub>

Lerp is a small TUI, written in Go, that orchestrates software work
through Linear. You put tickets on a board; lerp runs coding agents to
move them across it.

Linear is the database, the board is the workflow, and lerp is the
reconciler between them
([the model at length](https://lerp.sh/latest/docs/how-lerp-works/)).

**[lerp.sh](https://lerp.sh/)** is the site, and
**[the docs](https://lerp.sh/latest/docs/)** take you from install to a
first promoted ticket.

## Install

Lerp runs on macOS and Linux; Windows is not supported and does not
build there.

```sh
go install github.com/mattwalters/lerp/cmd/lerp@latest
```

With Homebrew:

```sh
brew install mattwalters/tap/lerp
```

Without a Go toolchain, `install.sh` downloads the right binary for
your OS and arch from the latest release, verifies it against the
published checksum, and installs it to `$HOME/.local/bin`:

```sh
curl -fsSL https://raw.githubusercontent.com/mattwalters/lerp/main/install.sh | sh
```

Prebuilt binaries (macOS and Linux, amd64 and arm64) are on the
[releases page](https://github.com/mattwalters/lerp/releases), and
`make install` builds one from a clone.

## Getting started

```sh
lerp login                                  # sign in to Linear (loopback OAuth)
lerp init                                   # wire this repo to a Linear team (asks, or pass --team LERP)
lerp                                        # open the board
```

`lerp login` opens your browser to Linear's consent screen, requesting only `read,write` access (never `admin`) to read and update tickets on the teams you serve.

### Headless and CI

In environments without a browser (CI runners, headless servers), set a personal API key instead:

```sh
LINEAR_API_KEY=... lerp init --team LERP
LINEAR_API_KEY=... lerp
```

When set, `LINEAR_API_KEY` takes precedence over any stored OAuth token.

## FAQ

**Does lerp update itself?**
No. Lerp launches coding agents with `bypassPermissions` and your full account; the binary holding that grant changes only by deliberate human action, and a package manager's bookkeeping (Homebrew, `go install`) stays consistent. Lerp performs an anonymous, unauthenticated GET of GitHub's releases API at most once every 24 hours to check for newer tags, caching the result in `$XDG_STATE_HOME/lerp/update.json` (`~/.local/state/lerp/update.json`). The check never runs without a terminal (skipping CI and scripts), never blocks startup or delays the board, and can be disabled entirely with `LERP_NO_UPDATE_CHECK=1`.

**How is authentication handled and scoped?**
`lerp login` is the recommended path: it uses OAuth with PKCE to store an expiring, auto-renewing token in your user config directory (`~/.config/lerp/token.json` on Linux, `~/Library/Application Support/lerp/token.json` on macOS) with `0600` permissions. OAuth tokens are scoped (`read,write`, no `admin`) and expire, whereas personal API keys are non-expiring and carry your full workspace access unless restricted at creation. OAuth also benefits from Linear's higher rate limit (5,000 requests/hr vs 2,500/hr for personal keys). Revocation is immediate via `lerp logout` or in Linear's settings under **Authorized applications**.

**How do I clean up or uninstall?**
`make uninstall` from a clone removes the binary `make install` put in your
`GOBIN`; a brew install is removed by `brew uninstall lerp`; an install.sh
install is removed by deleting `lerp` from
`$HOME/.local/bin` (or the `--bin-dir` you chose). Run `lerp logout` (or
delete the stored token file) to revoke and remove local credentials. To
clean up local state,
`rm -rf .lerp/` once all agents have stopped. Run evidence in
`.lerp/runs/` is how the next `lerp` adopts or reaps live agents, and
workspaces under `.lerp/workspaces/` are git worktrees whose registrations
the stock `dispose` command normally unwinds (`git worktree prune` handles
strays). Losing `.lerp/` costs compute, never correctness. Linear workflow
statuses created by `lerp init` stay on the team until you delete or
archive them in Linear's settings.

## The docs

**[The docs](https://lerp.sh/latest/docs/)** are the site:
[install](https://lerp.sh/latest/docs/install/) and the
[quickstart](https://lerp.sh/latest/docs/quickstart/), the
[model the board is a picture of](https://lerp.sh/latest/docs/how-lerp-works/),
a page per motion through the interface, and the reference for
the repo config, the command line and
[troubleshooting](https://lerp.sh/latest/docs/troubleshooting/).
They are published from this repository, and every release tag freezes
the copy that shipped with it.

Three files stay here in the repository, where they are edited alongside
the code they describe. SCOPE.md is a page of the docs too, mounted into
the site rather than copied:

- [SCOPE.md](SCOPE.md) is the fence around the project: five concepts,
  nine invariants, and the litmus tests every change runs through. Read
  it before proposing anything.
- [CONTRIBUTING.md](CONTRIBUTING.md) is how a proposal gets made, and
  what will and won't be accepted here.
- [SECURITY.md](SECURITY.md) is the trust model, and where to report a
  vulnerability. Read it before the first unattended run: running lerp
  against a team gives everyone who can move a ticket into a served
  status the ability to start an agent on your machine.
