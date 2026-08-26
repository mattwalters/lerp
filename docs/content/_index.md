---
title: lerp
tagline: A small, reliable CLI that orchestrates software work through Linear.
install: go install github.com/mattwalters/lerp/cmd/lerp@latest
---

You put tickets on a board; lerp runs coding agents to move them across it.

![The lerp board: an inbox of tickets waiting on a human, a work panel of queues and three lanes running coding agents beneath it, and a main pane that opens beside them to read a ticket or tail a lane's log](docs/demo.gif)

<sub>Recorded against a fake board and a stub agent; `make demo` regenerates
it from [`docs/demo.tape`](docs/demo.tape).</sub>

One binary and one screen. Lerp opens on the Linear board you already have,
fills a few lanes with coding agents, and answers the two questions an
operator actually has: what is waiting on me, and what is the machine doing
right now. Nothing runs on a server and nothing is stored anywhere but
Linear — close the laptop and the work stops; open lerp again and the loop
picks the same board back up.

## The mental model

**Linear is the database**: all durable state — what work exists, what stage
it is in, who has claimed it, what was decided — lives in Linear, and lerp
keeps no store of its own. **The board is the workflow**: a queue is a Linear
status with a prompt, a runner, and a status to move to on success. **Lerp is
a reconciler**: desired state is the board, actual state is the agent
processes running on this machine, and the loop starts, adopts, or reaps
agents until the two match.

## Start here

**[Overview](docs/overview.md)** — what lerp is in a page, and where the rest
of the manual is.

**[Why lerp](why.md)** — what it deliberately is not, and how that differs
from the other ways to put agents on a backlog. Read this one first if you
are still deciding.

**[README.md](README.md)** — the working manual: install, wiring a repo to a
team, running the loop, the stock pipeline, and what to do when something
looks stuck.

**[SCOPE.md](SCOPE.md)** — the fence around the project: five concepts, nine
invariants, and the litmus tests every change runs through.

The docs you are reading are the ones that shipped with this version of lerp;
the picker in the header moves between versions.
