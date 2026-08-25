# lerp — scope

Lerp is a small, reliable CLI, written in Go, that orchestrates software
work through Linear. You put tickets on a board; lerp runs coding agents
to move them across it.

This document is the fence around the project. It exists because lerp's
predecessor died of bloat — the code and the conceptual framework grew
until nobody could hold the whole thing in their head. Lerp is the
opposite bet: a tool whose every layer one person can understand, whose
surface area stays small enough to be elegant, and whose workflow is a
well-lit path, not a cage. When a proposal conflicts with this document,
the proposal loses or the document is amended — deliberately, never by
drift.

## The model

**Linear is the database.** All durable state — what work exists, what
stage it is in, who has claimed it, what was decided — lives in Linear.
Lerp keeps no store of its own: no SQLite, no journal, no server.

**The board is the DAG.** A queue is a Linear status with instructions
attached. Workflow topology exists only in where tickets sit and where
queues point; lerp never contains an `if` about your process. Routing is
done by placing a ticket: a big feature enters at Planning, a small fix
enters at Implementing. Branching is a human or an agent moving a
ticket, never config syntax.

A hop on the board is a decision somebody makes. Iteration is not a
decision: a stage that hands work back to an earlier one draws a cycle,
and bounding a cycle needs a round count — state outside Linear, or an
`if` about your process. So bounded iteration happens inside a single
queue run, where the count is the agent's own context and the board
never hears about it. Review-and-fix is the worked example: it lives in
the implement prompt, and only what the loop could not settle comes back
to a human as a move.

**Lerp is a reconciler.** Desired state is the board; actual state is
the agent processes running on this machine. Lerp runs one loop:
compare the two, then start, adopt, or reap agents until they match. A
crash — of an agent, of lerp, of the laptop — is not an error case; it
is drift, and the loop repairs drift.

Lerp is not privileged. Humans, agents, and Linear's own automations
(a merged PR advancing a ticket) may all move tickets; the loop
reconciles whatever board it finds, without caring who changed it.

## The five concepts

Lerp's entire ontology. Each is a noun a new reader can learn in a
sentence.

1. **Ticket** — a Linear issue. The unit of work. Lerp never invents
   work items of its own.
2. **Queue** — a Linear status with four fields of config: the status,
   a prompt, a runner, and the status to move to on success (optionally,
   on failure). Nothing else. No conditionals, no templating logic, no
   DAG syntax. On a clean exit, lerp moves the ticket only if the agent
   didn't — `on-success` is the default, not a verdict. An agent
   escalates, branches, or refuses by moving the ticket itself; lerp
   respects whatever it finds. A ticket blocked by an unfinished ticket
   (Linear's `blockedBy`) is not eligible for pickup, no matter what
   queue it sits in — blocking relations are how humans sequence
   concurrent work.
3. **Runner** — an adapter to a coding-agent CLI (Claude Code, Codex,
   Antigravity, …). The contract: takes a prompt and a working
   directory, runs to exit, exit code means done or failed. The
   contract is the lowest common denominator — a capability every
   runner can't offer is a capability lerp doesn't have.
4. **Lane** — a concurrency unit. Lerp runs at most N agents at once
   (N is small, ~5). A lane's occupant is a run: a pid, a log file, a
   ticket, and a workspace (see invariant 9).
5. **The loop** — the reconciler described above. There is exactly one.

Amendment rule: adding a sixth concept requires removing one of these
five. If that trade is unappealing, the feature is out of scope.

## Invariants

1. **Linear is the only durable store.** Local disk holds config and
   evidence of running processes, nothing else. Losing all local state
   may cost compute; it may never cost correctness. `rm -rf .lerp/runs`
   under live agents means orphaned processes and re-run stages — never
   lost or corrupted tickets.

2. **Team → repo is a function** (many-to-one allowed, not a
   bijection). Every ticket must resolve to exactly one working
   directory. Two repos may not claim the same team; one repo may serve
   several teams (the monorepo case falls out for free). At startup,
   lerp verifies the function and refuses to run if it doesn't hold.

3. **Every queue run is safe to kill and restart from its beginning.**
   Progress is checkpointed only at queue boundaries, as artifacts in
   Linear (a plan in the ticket, a PR link, a verdict comment). Lerp
   never checkpoints inside a run. Kill anything at any time, on any
   machine; the worst case is a re-run stage.

4. **The claim is the assignment, and the lock is in Linear.** Picking
   up a ticket means assigning it to the operating developer's Linear
   user. Claim protocol: assign to self, settle, read back; if the
   assignee is you, you won, otherwise walk away. The race window that
   remains resolves to duplicated compute, which invariant 3 already
   tolerates. No lerp server, no coordination service, ever.

5. **The engine is generic; the opinion ships as config.** Lerp's stock
   config encodes planning → human plan approval → implementing → a
   human merge, and the implement stage reviews its own work before it
   hands over. The engine knows nothing about that sequence — each queue
   is independent, and the topology exists only in the `on-success`
   pointers. The approval step is not engine either: it is a status no
   queue serves, where a ticket rests until a human promotes it.

6. **Setup time and run time never mix.** `lerp init` (and humans) may
   create board structure — teams, statuses matching queues. The loop
   only moves tickets. A reconciler that edits its own board schema is
   how you get spooky action.

7. **Durable = decisions; ephemeral = process.** Linear receives
   stage-boundary artifacts. The high-fidelity agent stream — every
   tool call and rationale — goes to a local log file, tailed live by
   the TUI and discarded without ceremony. Never post the firehose to
   Linear.

8. **Lerp speaks exactly one external API: Linear.** Git, GitHub, and
   PRs belong to agents (via their prompts) and to humans. A PR is a
   stage-boundary artifact like any other — created by an agent with
   `gh`, attached to the ticket by Linear's GitHub integration, read
   by the next stage's prompt. Lerp never calls a code host and never
   inspects a branch. The engine that has never heard of PRs is the
   engine that works for people who don't make PRs.

9. **Workspaces are provisioned by config, not by lerp.** A lane's
   workspace is created and destroyed by two config-supplied commands,
   provision and dispose; stock config uses git worktrees. Lerp invokes
   them with a unique lane/run identity (lane number, ticket id,
   workspace path) and otherwise knows nothing about what provision
   does. Environment isolation — ports, databases, containers — is the
   project's problem, solved inside its provision command. Lerp will
   never grow an isolation subsystem: no health checks, no readiness
   probes, no service definitions. If provision exits non-zero, the
   lane doesn't start and the ticket stays queued.

## The interface

TUI-first (Bubble Tea; lazygit is the spiritual reference). The TUI is
the engine — no daemon. Work happens while lerp is open; a headless
`lerp run` is the same loop without the chrome, if and when it earns
its existence.

Two panels on one screen, one per question an operator actually has,
beside a main pane that details whichever row is selected:

1. **Inbox** — what waits on a human: unclaimed tickets, and the
   operator's own claimed tickets, sitting in a status no queue serves
   — reviews to read, questions agents have raised, failed runs to
   retriage. It reads as a table, one row per ticket, and the Linear
   status is a column: the vocabulary is the operator's own, never a
   category invented here. Sorting it (by leverage, priority, status or
   project) and scoping it to one project are display over the one list
   the pass already fetched, session-only, with no saved views and no
   filter syntax; filtering that changed *which* tickets were fetched
   would not be. Select a ticket and press `p` to promote it: pick a
   target from the configured queue statuses or a pipeline exit, and
   lerp moves it there. A promote into a status some queue serves also
   releases the claim the parked ticket was holding — an assigned ticket
   is never eligible, so keeping it would strand the ticket in a queue
   that could never pick it up. That release is invariant 4's protocol,
   not a second capability. This is the only write the TUI makes anywhere.
2. **Work** — what the machine is doing with the board: one list,
   grouped by queue, holding the tickets running in each queue and the
   tickets waiting behind them. What is running and what runs next are
   the same question about the same tickets; the separate question is
   what needs a human. The main pane follows the selected row — a live
   log for a running ticket, what gates it for a waiting one. It is
   read-only: to change what runs next, move a ticket in Linear.

One escape hatch: **eject**. Select a running ticket; lerp stops the
agent, frees its lane, and hands you the vendor's own resume command
(`claude --resume <session-id>`) so the headless run becomes your
interactive session, full context intact. Finish the work yourself or
toss the ticket back into a queue. Lerp does not implement
interactivity; it hands you the door.

## Multiplayer

Inherited from Linear, not built. Each developer runs their own lerp
against their own clone; a lockfile keeps it to one lerp per clone, and
invariant 4 arbitrates claims across machines. The board reads like a
human team's board — "Sarah has LERP-42 in Implementing" is true
whether Sarah or Sarah's agent is doing the work. Colleagues see claims
and stage artifacts, not each other's live streams — exactly the
visibility they have into each other's human work.

No work stealing, no global scheduler, no fairness guarantees. Each
lerp fills its own lanes from what is unclaimed. A fifty-developer shop
that wants a scheduler wants a different product.

## What lerp is not

- Not a workflow engine: no conditionals, no DAG language, no plugin
  hooks. The board is the workflow.
- Not a process supervisor, CI system, or deployment tool.
- Not a server, daemon, or web service. Nothing listens on a port.
- Not a database. See invariant 1.
- Not an agent framework. Runners are command templates, not SDKs.
- Not a Linear client, with one narrow exception: the inbox lists
  unassigned tickets in statuses no queue serves, promote moves a ticket
  the operator selected and settles its claim by the same rule a finished
  run uses, and the main pane reads that selected ticket's body and
  comments — lerp's own stage-boundary artifacts — read-only, never
  composing or replying, never navigating on to another ticket.
  Everything else — create, edit, comment, assign outside the claim
  protocol — stays in Linear.
- Not infrastructure for any other product to depend on. It is a
  standalone tool.

## Not yet, maybe never

Deferred consciously — none of these may sneak in as a subsystem:

- Hang detection (pid alive, no progress) — timeouts, later, maybe.
- Live mid-run steering of agents — eject covers grabbing the wheel;
  revisit only if usage screams, and only within the runner contract.
- Headless `lerp run` — same loop, no TUI; wait until it hurts.
- Per-operator config layered over the repo's — the checked-in
  lerp.toml is the whole config, pipeline included, so the permissions
  it grants are versioned and reviewed and every developer runs the
  same pipeline. Repos share a pipeline by copying the file. A
  personal override or merge layer is complexity waiting for a reason.

## Litmus tests

For any proposed change, in order:

1. Does it add a sixth concept? Then which of the five does it remove?
2. Does it put durable state anywhere but Linear?
3. Does it make any queue run unsafe to kill?
4. Does it require a runner capability not all runners have?
5. Does it require lerp to speak to any API other than Linear?
6. Could it be config pointing at what exists, instead of code?

A "yes" to 1–5 without an amendment to this document means no.
