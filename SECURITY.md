# Security

Lerp runs coding agents unattended on your machine, driven by a board
other people can write to. That is the product, not a corner of it, and
it puts lerp in an unusual trust class — so this page is the one
authoritative statement of what running lerp grants and to whom.

The short version: **running lerp against a Linear team gives everyone
who can move a ticket into a served status the ability to run code on
your machine.** Everything below is that sentence in detail.

## The threat model

### The repo config is code

Lerp reads one config file at the repo root (`lerp.toml`, `lerp.yaml`,
`lerp.yml`, or `lerp.json`), and that file contains shell commands:
`provision`, `dispose`, and every runner's `command` — or, for a runner
that names a built-in `vendor` instead, the `args` line it hands that
vendor's adapter, which reaches the same place a hand-written `command`
would. Cloning a repository and running `lerp` in it executes those
commands — the same trust class as running `make` in a strange repository.
A runner's `resume` is a fourth: lerp never runs it, it prints it for you
to paste when you eject a run — which makes your paste the trigger and the
file's author the one who chose the command.

That is why the file is checked in rather than generated per machine:
the pipeline, and the permissions it grants, are versioned and reviewed
like any other code. Read it before you run it, and read the diff when
it changes.

### Ticket access is code-execution access

A ticket is eligible for pickup when it sits in a queue's status, has no
assignee, and is not blocked. That is the whole test
(`internal/loop/claim.go`) — there is no allowlist of authors, no
approval step, and no check on who put the ticket there.

So anyone who can put a ticket into a served status — by creating it
there or by moving it there — starts an unattended agent run on a
machine running lerp against that team. Nothing narrows that to a
machine you picked: the claim is an assignment to a Linear *user*, so
it arbitrates between users and not between machines — two lerps signed
in as the same user both read the claim back as their own, and across
users the race lerp accepts resolves to duplicated compute. Size the
exposure as every machine running lerp against that team.

That reach belongs to workspace members and guests, to Linear's own
automations, and to any integration with write access to the board — a
GitHub integration that moves a ticket on merge is triggering runs just
as a person would.

Ticket text is a second channel. Lerp never passes ticket bodies into
prompts — it hands the runner the queue's prompt and the ticket
identifier, nothing more — but the stock prompts tell the agent to go
read the ticket over Linear's MCP server. Whatever a ticket's
description and comments say is therefore instruction reaching an agent
that may be running with your full user account. Treat write access to
a served team as equivalent to commit access to the repository it
serves, and scope Linear permissions accordingly.

### The worktree is a tidiness boundary, not a security one

The stock `provision` builds a git worktree per lane. It keeps
concurrent runs from tripping over each other's files; it is not
containment. Nothing stops an agent from reading `~/.ssh`, or from
writing outside its workspace.

`--permission-mode bypassPermissions` in the stock Claude runner makes
that concrete: the agent edits files and runs commands without asking,
as your user. `lerp init` asks before including that flag and defaults
to leaving it out — but that is a default, not a fact about your repo:
the shipped `lerp.example.toml`, and any config copied from another
repo, carries the flag. Read your own repo config's `[runners.claude]`
block: the flag sits on its `args` line for the stock vendor runner, or
inside `command` for a hand-written one — either way, that block is the
only place the answer lives. Declining has a
cost — a headless run then fails at the first tool it is not allowed to
use unless you curate an `--allowedTools` list — but it is a real
grant, and the checked-in repo config is where you make it
deliberately.

### No sandbox is provided or implied

Lerp does not sandbox agents and has no plans to. Isolation is the
operator's job, and it takes both halves of the config: `provision` and
`dispose` build and tear the sandbox down — a container, a VM, a
throwaway user account instead of a worktree — and the runner
`command` has to be the thing that *enters* it (`docker exec ...`, an
`ssh` into the VM). A vendor runner cannot express that: it names an
adapter, not an entry point, so containerizing means writing the
runner as `command` by hand instead of `vendor`. Lerp always starts the
runner itself with `sh -c` on the host, in the workspace directory, so
the natural half-step — keep provisioning the worktree, add a
container beside it, leave the runner reading `claude -p ...` — leaves
the container idle and the agent on your machine. Nothing in lerp's
defaults does any of this for you.

### What lerp itself does

Lerp's own footprint is small: it speaks one external API (Linear)
plus an anonymous GET of GitHub's releases API to check for updates,
listens on no port while the board runs, and runs no daemon.
The one port exception is setup time: `lerp login` opens a loopback socket
on `127.0.0.1` on an ephemeral port for the few seconds an OAuth redirect
takes, and closes it before anything runs.

That lack of a daemon cuts both ways, and it is worth knowing before you
need it: quitting lerp does not stop a run. Agents are their own process
groups and keep working, and the next lerp adopts them. To stop one, eject
it with `e` — or kill its process group. Values interpolated into a runner
`command` are shell-quoted, so nothing in a ticket can alter the command
you configured — the injection surface is the ticket the agent reads, not
the command line lerp builds.

Three details of that footprint are worth stating plainly:

- **Where credentials live and how they are scoped.**
  `lerp login` authenticates via PKCE OAuth with `actor=user`, requesting
  `read,write` scopes (never `admin`). It stores an auto-renewing token
  pair in your user config directory (`token.json` under
  `~/.config/lerp/` on Linux, `~/Library/Application Support/lerp/` on
  macOS) with mode `0600` inside a `0700` directory. Stored tokens live
  outside the clone (SCOPE invariant 1): deleting the file removes local
  access, and losing it costs a re-login, never correctness. Revocation
  lives in `lerp logout` and in Linear's settings under **Authorized
  applications**.

  OAuth tokens are scoped and expiring where personal API keys
  (`LINEAR_API_KEY`) are neither — a personal key carries full workspace
  access unless manually restricted at creation, and does not expire —
  which is why `lerp login` is the recommended path. OAuth also carries
  Linear's 5,000 requests/hour rate limit versus 2,500/hour for personal
  API keys.

- **Agents inherit lerp's environment, minus its credentials.**
  `provision`, `dispose` and every runner are started with lerp's own
  environment plus a few `LERP_` variables — with `LINEAR_API_KEY`
  removed, and stored OAuth tokens never passed down. Lerp's Linear
  access is its own — a personal API key is write access to your entire
  Linear workspace, not just the served team; the agent's Linear access
  is meant to arrive through its own authorization, under its own
  identity (configured via its CLI's Linear MCP server), so lerp does
  not hand credentials down: it drops `LINEAR_API_KEY` once read and
  never writes it to disk. Read that as hygiene, not containment: it
  closes the accidental path — a `provision` script that logs its
  environment into the lane log — and not a determined one. An agent
  running as you can still read the shell profile you exported the key
  in (such as `~/.zprofile` or `~/.bashrc`), or on Linux, lerp's own
  `/proc/<pid>/environ`.

  An exported `LINEAR_API_KEY` reaching agents through login shells is
  precisely that environment leak, not a supported access path: on a machine
  without that exported key, any runner whose CLI lacks Linear MCP configured
  will silently fail to read its ticket or leave a verdict.

  Everything else in the environment does go down: your cloud tokens,
  your registry credentials, whatever else the shell you started lerp
  in was carrying. Run lerp with an environment you would hand to the
  agent, because you are.
- **Run logs persist locally.** `.lerp/` holds no durable truth —
  losing all of it may cost compute, never correctness — but it does
  hold each run's full agent transcript, the loop's diagnostics, and
  the lane workspaces. Those transcripts contain whatever the agent
  read aloud. `lerp init` adds `.lerp/` to your `.gitignore` for you,
  and says so when it cannot — take it at its word and check, because
  what a committed transcript publishes is not recoverable.

## Reporting a vulnerability

Report privately through GitHub's
[private vulnerability reporting](https://github.com/mattwalters/lerp/security/advisories/new)
— the "Report a vulnerability" button under the repository's Security
tab. Please do not open a public issue for something exploitable.

Expect an acknowledgement within 7 days and an assessment within 30.
Lerp is maintained by one person, so those are honest limits rather
than an SLA. If a report lands, you will be credited in the advisory
unless you ask otherwise.

Releases are tagged; the latest release is what is supported, and fixes
land on `main` first.

**What is in scope:** anything that lets a party who can neither place
a ticket in a served status nor write text a served ticket carries
influence what an agent does, anything that escalates beyond the grants
documented above, any path that puts Linear credentials (stored token or
API key) somewhere this page does not say they go, and anything in a
ticket that escapes the TUI's sanitizing and reaches your terminal as
escape sequences.

**What is not:** the trust model on this page. An agent doing damage
because someone who could write into a served ticket — its description
or its comments — told it to, or because a repo config was run unread,
is lerp working as designed and documented. If you think the design
itself is wrong, that is an issue, not an advisory — open one.
