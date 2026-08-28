---
title: Quickstart
summary: From `lerp init` to a first promoted ticket, in four steps.
weight: 40
---

# Quickstart

Four steps, from a repository with no lerp in it to an agent working a
ticket. It assumes you have [installed lerp](install.md), signed in with
`lerp login` (or set `LINEAR_API_KEY`), and given the team's status field to
lerp — that last one is a settings change nothing here can do for you, and
skipping it costs every ticket its last hop.

## 1. Wire the repo to a team

Sign in to Linear if you have not already:

```sh
lerp login
```

Then from anywhere inside your Git repository:

```sh
lerp init --team LERP --team-name "Lerp"
```

`--team` is the Linear team key — the ticket prefix, the LERP in LERP-42 —
and `--team-name` is the display name used only if the team must be created.
Check the key against Linear before you run: a key that matches no existing
team exactly is not an error, it quietly creates a new team under that key.

This creates the Linear team if it is missing and, on a first run, holds a
short conversation to fit the [stock planning → approval → implementing
pipeline](lerp-toml.md#the-stock-pipeline) onto the board the team already
has: it shows the team's existing statuses, asks whether to include the
optional planning stage and the review pass, and offers to create the stock
statuses the chosen pipeline references — or, on customize, to map each one
onto a status you already have. Existing statuses are never modified, and
init says which statuses it creates and which it reuses before it acts. It
then writes [`lerp.toml`](lerp-toml.md) at the repository root, uncommitted,
for you to review and check in.

Init also appends `.lerp/` to the repository's `.gitignore`, creating that
file if there is none — lerp's run records, logs and workspace worktrees
live there, and none of it belongs in your history. Commit that change along
with `lerp.toml`: a colleague who clones a repo that already has a
`lerp.toml` never runs `lerp init`, so an uncommitted ignore covers only
your clone. A repository whose ignore list already names `.lerp/` is left
alone, and an ignore file lerp cannot write is reported, not fatal — init
still writes `lerp.toml`.

The conversation's last question is whether the stock Claude runner should
include `--permission-mode bypassPermissions`. The default is no — saying
yes is a real grant, and the diff you commit is where that decision gets
reviewed. Declining has a cost too: a headless run then fails at the first
tool the agent is not allowed to use, unless you curate an `--allowedTools`
list. Both are spelled out under [the stock
pipeline](lerp-toml.md#the-stock-pipeline). Piped input or `--yes` skips the
conversation and takes the stock answers: the full pipeline, stock status
names, no bypass grant.

Init may also print a report about where your pipeline ends — statuses
Linear does not yet count as completing work. Act on what it prints; the
reasoning is under
[troubleshooting](troubleshooting.md#why-does-init-tell-me-to-set-a-statuss-category-myself).

## 2. Give the agent what it needs

Lerp hands the runner a prompt and the ticket identifier, nothing more. The
stock prompts expect the agent to read the ticket and write stage artifacts
itself.

Lerp operates on a **two-credential model**: lerp's own access
(`LINEAR_API_KEY`, or later `lerp login`'s OAuth token) and the agents' access
are separate on purpose. Lerp scrubs its own credential from child environments
before launching runners or workspace commands, so setting up lerp does not
give agents Linear access, and setting up one does not set up the other.

The general rule: **every CLI a runner names needs its own Linear access,
configured once in that CLI.** See [Linear's MCP
documentation](https://linear.app/docs/mcp) for the authoritative endpoint.
For the built-in vendor adapters:

* **Claude Code (`claude`)**:
  ```sh
  claude mcp add --transport http linear https://mcp.linear.app/mcp
  ```
  Then start `claude` and run `/mcp` to complete the interactive OAuth flow.
* **Antigravity (`agy`)**:
  ```sh
  agy mcp add linear https://mcp.linear.app/mcp
  ```
  Then complete the interactive OAuth authentication in agy's own settings
  (tokens land in `~/.gemini/antigravity-cli/mcp_oauth_tokens.json` and refresh
  automatically).
* **Codex (`codex`)**:
  ```sh
  codex mcp add linear --url https://mcp.linear.app/mcp
  ```
  Then run `codex mcp login linear`.

Each CLI manages its own tokens, so adopting a second vendor means a second
one-time MCP setup.

**Shared OAuth bridge option:** If you run multiple CLIs and prefer one shared
browser login across all of them, you can register the stdio bridge in each CLI
instead:

```sh
<cli> mcp add linear -- npx -y mcp-remote https://mcp.linear.app/mcp
```

This shares one browser OAuth across all CLIs through `mcp-remote`'s
`~/.mcp-auth` token cache. The caveats: it adds a Node/npx dependency to the
agent path, and the shared token cache misbehaves across multiple Linear
accounts.

What the agent is permitted to do beyond reading tickets is the other half of
setup: read `lerp.toml` before you run it.

## 3. Promote a ticket

Routing is done by placing a ticket: in Linear, move one into Planning (a big
feature) or straight into Implementing (a small fix). Check while you are
there that nothing makes it [ineligible](the-board.md#the-claim) — an
assignee left on the ticket is the one that surprises people.

## 4. Run it
 
```sh
lerp
```

Bare `lerp` opens the TUI, and [the loop runs while it is
open](reading-the-board.md): each pass claims what it can, provisions a
workspace, runs the queue's agent to exit, and applies the queue's move rule.
A pass runs every twelve seconds, so the ticket you promoted should be on
the Work panel with a lane under it almost at once. [The board](the-board.md) is what that screen is a picture of.

Once a ticket is moving, the rest of the manual is the motions on it: you
will want [promoting](promoting.md) for the gates the stock pipeline waits
at, and [watching a run](watching-a-run.md) for the log.
