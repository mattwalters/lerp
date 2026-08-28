---
title: lerp
tagline: A small CLI that runs coding agents over your Linear board.
install: brew install mattwalters/tap/lerp
---

You put tickets on the board; lerp moves them across it.

{{< cast webm="casts/demo.webm" mp4="casts/demo.mp4"
         poster="posters/demo.png"
         title="The lerp board: an inbox of tickets waiting on a human, a work panel of queues and three lanes running coding agents beneath it, and a main pane that opens beside them to read a ticket or tail a lane's log"
         autoplay=true >}}

One binary and one screen. Lerp opens on the Linear board you already have,
fills a few lanes with coding agents, and answers the two questions an
operator actually has: what is waiting on me, and what is the machine doing
right now.

## Where it runs

On this machine, and nowhere else. No server, no daemon, no webhook, no
account with anybody but Linear. Lerp authenticates as you and runs agents
as ordinary local processes — close the laptop and the work stops; open
lerp again and the loop picks the same board back up.

## What you need

Two things. A Linear workspace, because the board is the whole model — if
you don't use Linear, lerp is not your tool. And a coding-agent CLI on this
machine: Claude Code, Codex, and Antigravity have adapters out of the box,
and anything else works if it takes a prompt and a working directory and
exits non-zero when it fails.

## The mental model

**Linear is the database**: all durable state — what work exists, what stage
it is in, who has claimed it, what was decided — lives in Linear, and lerp
keeps no store of its own. **The board is the workflow**: a queue is a Linear
status with a prompt, a runner, and a status to move to on success. **Lerp is
a reconciler**: desired state is the board, actual state is the agent
processes running on this machine, and the loop starts, adopts, or reaps
agents until the two match.

## Where next

**Still deciding?** [Why lerp](why.md) — what it deliberately is not, and
how that differs from the other ways to put agents on a backlog.

**Ready to try it?** [Quickstart](docs/quickstart.md) — `lerp init`, a
first run, and a first promoted ticket, following only the manual.

**Adopting it?** [The manual](docs/_index.md) — the board, the interface,
and the reference, versioned by release.

And beside the manual sits [SCOPE.md](SCOPE.md): the written list of things
lerp will refuse to become, kept in the repository so every change is
checked against it.

---

```
brew install mattwalters/tap/lerp
```

<sub>*lerp* (v.) — to interpolate linearly; to move smoothly between two
points.</sub>

This page isn't versioned — the manual is. Open Docs and the picker in its
header moves between releases.
