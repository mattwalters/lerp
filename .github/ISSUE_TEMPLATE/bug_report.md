---
name: Bug report
about: Lerp did something it shouldn't have
title: ''
---

<!--
Proposing a change instead? Open a blank issue and make the scope case
against SCOPE.md first — see CONTRIBUTING.md.

Reporting a vulnerability? Not here. SECURITY.md has the private
reporting path.
-->

**What happened, and what you expected instead**

**`lerp version`** — paste what it prints. Only a build with no VCS info at
all reports the bare `dev`, and that build has no commit to name either; if
that is what you get, paste `go version -m $(command -v lerp)` instead — it
still shows the Go version and module path the binary was built with.

**OS** — macOS or Linux, and which version.

**Your `lerp.toml`, in shape** — the queues, runners and workspace
commands, with anything private taken out. Don't paste secrets; the
runner `command` line matters most.

**What the board showed** — the row, its status, and what the panel or
the main pane said. If a run is involved, its log is at
`.lerp/runs/<run-id>/run.log` and the loop's own diagnostics are in
`.lerp/loop.log`; a tail of either often has the answer. Those
transcripts carry whatever the agent read aloud, so read before you
paste.
