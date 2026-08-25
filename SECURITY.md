# Security

Lerp runs coding agents unattended on your machine, driven by a board
other people can write to. That is the product, not a corner of it, and
it puts lerp in an unusual trust class — so this page is the one
authoritative statement of what running lerp grants and to whom.

The short version: **running lerp against a Linear team gives everyone
who can move a ticket into a served status the ability to run code on
your machine.** Everything below is that sentence in detail.

## The threat model

### `lerp.toml` is code

Lerp reads one config file, `lerp.toml` at the repo root, and that file
contains shell commands: `provision`, `dispose`, and every runner's
`command` and `resume`. Cloning a repository and running `lerp` in it
executes those commands — the same trust class as running `make` in a
strange repository.

That is why the file is checked in rather than generated per machine:
the pipeline, and the permissions it grants, are versioned and reviewed
like any other code. Read it before you run it, and read the diff when
it changes.

### Ticket access is code-execution access

A ticket is eligible for pickup when it sits in a queue's status, has no
assignee, and is not blocked. That is the whole test
(`internal/loop/claim.go`) — there is no allowlist of authors, no
approval step, and no check on who put the ticket there.

So anyone who can create a ticket in a served team, or move one into a
served status, can start an unattended agent run on **every** machine
running lerp against that team. That includes workspace members and
guests, Linear's own automations, and any integration with write access
to the board — a GitHub integration that moves a ticket on merge is
triggering runs just as a person would.

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
to leaving it out. Declining has a cost — a headless run then fails at
the first tool it is not allowed to use unless you curate an
`--allowedTools` list — but it is a real grant, and the checked-in
`lerp.toml` is where you make it deliberately.

### No sandbox is provided or implied

Lerp does not sandbox agents and has no plans to. Isolation is the
operator's job, and `provision` / `dispose` are the seam for it: point
them at a container, a VM, or a throwaway user account instead of a
worktree, and lerp will not know the difference. Nothing in lerp's
defaults does this for you.

### What lerp itself does

For completeness, lerp's own footprint is small: it speaks exactly one
external API (Linear), listens on no port, runs no daemon, and stores
nothing durable — `.lerp/` holds evidence of running processes, and
losing it may cost compute but never correctness. It reads your Linear
API key from `LINEAR_API_KEY` and never writes it to disk. Values
interpolated into a runner `command` are shell-quoted, so nothing in a
ticket can alter the command you configured — the injection surface is
the prompt the agent reads, not the command line lerp builds.

## Reporting a vulnerability

Report privately through GitHub's
[private vulnerability reporting](https://github.com/mattwalters/lerp/security/advisories/new)
— the "Report a vulnerability" button under the repository's Security
tab. Please do not open a public issue for something exploitable.

Expect an acknowledgement within 7 days and an assessment within 30.
Lerp is maintained by one person, so those are honest limits rather
than an SLA. If a report lands, you will be credited in the advisory
unless you ask otherwise.

Only the latest release is supported; fixes land on `main` and ship in
the next tag.

**What is in scope:** anything that lets a party without board write
access influence what an agent does, anything that escalates beyond the
grants documented above, and any leak of the Linear API key.

**What is not:** the trust model on this page. An agent doing damage
because someone with ticket-write access told it to, or because a
`lerp.toml` was run unread, is lerp working as designed and documented.
If you think the design itself is wrong, that is an issue, not an
advisory — open one.
