# lerp

[![ci](https://github.com/mattwalters/lerp/actions/workflows/ci.yml/badge.svg)](https://github.com/mattwalters/lerp/actions/workflows/ci.yml)

![The lerp board: an inbox of tickets waiting on a human, a work panel of queues and three lanes running coding agents beneath it, and a main pane that opens beside them to read a ticket or tail a lane's log](docs/demo.gif)

<sub>Recorded from [`docs/tapes/demo.tape`](docs/tapes/demo.tape) against a fake
board and a stub agent; `make demo` regenerates it.</sub>

Lerp is a small, reliable CLI, written in Go, that orchestrates
software work through Linear. You put tickets on a board; lerp runs
coding agents to move them across it.

Linear is the database, the board is the workflow, and lerp is the
reconciler between them —
[the model at length](https://lerp.sh/latest/docs/the-board/).

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

Prebuilt binaries — macOS and Linux, amd64 and arm64 — are on the
[releases page](https://github.com/mattwalters/lerp/releases), and
`make install` builds one from a clone. Then:

```sh
LINEAR_API_KEY=... lerp init --team LERP    # wire this repo to a Linear team
LINEAR_API_KEY=... lerp                     # open the board
```

## FAQ

**How is the Linear API key scoped?**
`LINEAR_API_KEY` is a Linear personal API key. Left unrestricted it carries
your user's full workspace access, but Linear lets you restrict a key when
you create it — to specific teams, and to permission scopes (read, write,
admin, create issues, create comments) — so give lerp a key restricted to
the teams it serves. For harder isolation, create that key on a dedicated
Linear user account for automation, so lerp acts as its own member.

**How do I clean up or uninstall?**
`make uninstall` from a clone removes the binary `make install` put in your
`GOBIN`; a brew install is removed by `brew uninstall lerp`; an install.sh
install is removed by deleting `lerp` from
`$HOME/.local/bin` (or the `--bin-dir` you chose). To clean up local state,
`rm -rf .lerp/` once all agents have stopped — run evidence in
`.lerp/runs/` is how the next `lerp` adopts or reaps live agents, and
workspaces under `.lerp/workspaces/` are git worktrees whose registrations
the stock `dispose` command normally unwinds (`git worktree prune` handles
strays). Losing `.lerp/` costs compute, never correctness. Linear workflow
statuses created by `lerp init` stay on the team until you delete or
archive them in Linear's settings.

## The manual

**[The manual](https://lerp.sh/latest/docs/)** is the docs site:
[install](https://lerp.sh/latest/docs/install/) and the
[quickstart](https://lerp.sh/latest/docs/quickstart/), the
[model the board is a picture of](https://lerp.sh/latest/docs/the-board/),
a page per motion through the interface, and the reference for
`lerp.toml`, the command line and
[troubleshooting](https://lerp.sh/latest/docs/troubleshooting/).
It is published from this repository, and every release tag freezes the
copy that shipped with it.

Three files stay here in the repository, where they are edited alongside
the code they describe. SCOPE.md is a page of the manual too, mounted into
the site rather than copied:

- [SCOPE.md](SCOPE.md) is the fence around the project — five concepts,
  nine invariants, and the litmus tests every change runs through. Read
  it before proposing anything.
- [CONTRIBUTING.md](CONTRIBUTING.md) is how a proposal gets made, and
  what will and won't be accepted here.
- [SECURITY.md](SECURITY.md) is the trust model, and where to report a
  vulnerability. Read it before the first unattended run: running lerp
  against a team gives everyone who can move a ticket into a served
  status the ability to start an agent on your machine.
