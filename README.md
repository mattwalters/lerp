# lerp

[![ci](https://github.com/mattwalters/lerp/actions/workflows/ci.yml/badge.svg)](https://github.com/mattwalters/lerp/actions/workflows/ci.yml)

![The lerp board: an inbox of tickets waiting on a human, a work panel of queues and three lanes running coding agents beneath it, and a main pane that opens beside them to read a ticket or tail a lane's log](docs/demo.gif)

<sub>Recorded from [`docs/tapes/demo.tape`](docs/tapes/demo.tape) against a fake
board and a stub agent; `make demo` regenerates it.</sub>

Lerp is a small, reliable CLI, written in Go, that orchestrates
software work through Linear. You put tickets on a board; lerp runs
coding agents to move them across it.

The mental model is three sentences. **Linear is the database**: all
durable state — what work exists, what stage it is in, who has claimed
it, what was decided — lives in Linear, and lerp keeps no store of its
own. **The board is the workflow**: a queue is a Linear status with a
prompt, a runner, and a status to move to on success; workflow topology
exists only in where tickets sit and where `on_success` points, never
in config syntax. **Lerp is a reconciler**: desired state is the board,
actual state is the agent processes running on this machine, and the
loop starts, adopts, or reaps agents until the two match — a crash is
not an error case, it is drift, and the loop repairs drift.

## Install

Lerp runs on macOS and Linux; Windows is not supported and does not
build there.

```sh
go install github.com/mattwalters/lerp/cmd/lerp@latest
```

Prebuilt binaries — macOS and Linux, amd64 and arm64 — are on the
[releases page](https://github.com/mattwalters/lerp/releases), and
`make install` builds one from a clone. Then:

```sh
LINEAR_API_KEY=... lerp init --team LERP    # wire this repo to a Linear team
LINEAR_API_KEY=... lerp                     # open the board
```

## The manual

**[The manual](https://mattwalters.github.io/lerp/)** is the docs site:
[install](https://mattwalters.github.io/lerp/docs/install/) and the
[quickstart](https://mattwalters.github.io/lerp/docs/quickstart/), the
[model the board is a picture of](https://mattwalters.github.io/lerp/docs/the-board/),
a page per motion through the interface, and the reference for
`lerp.toml`, the command line and
[troubleshooting](https://mattwalters.github.io/lerp/docs/troubleshooting/).
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
