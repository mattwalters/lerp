# Changelog

All notable changes to lerp are documented in this file. The format is based
on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

The changelog covers the binary, not the website. Each version's section is
what GitHub publishes as that release's body. From 0.2.0 onward, the
load-bearing sections are Changed and Removed.

## [Unreleased]

### Added

- Lane pane activity chart: a fixed braille line chart above the live log tail showing recent event activity across the pane's width via `ntcharts`.
- `lerp init` automatically sets colliding mid-stage pull-request automations to No action when creating a new Linear team.

### Changed

- `pulse` event ring records at 3-second bucket resolution across 300 buckets (15 minutes), downsampling to 15-second cells for the work row sparkline.
- Work row sparkline is drawn as a braille line via `ntcharts/sparkline` rather than block runes.

## [0.1.0] — 2026-08-29

### Added

- Initial release of lerp, a small TUI orchestrating software work through Linear.
- Board TUI: two panels (Inbox and Work) and a collapsible main pane for reading ticket details or tailing live runner logs, ticket promotion (`p`), force-start past lane limits (`S`), and agent ejection into an interactive session.
- Reconciler loop: single reconciliation loop matching Linear desired state against local agent processes across bounded concurrency lanes in config-provisioned worktree workspaces.
- Setup and bootstrap: `lerp init` to configure repository queues and bootstrap Linear team statuses.
- Authentication: `lerp login` and `lerp logout` using loopback OAuth with PKCE (scoped `read,write`), alongside `LINEAR_API_KEY` personal API key support.
- Runner adapters: built-in adapters for Claude Code, Codex, and Antigravity, alongside a generic command runner.
- Telemetry: local append-only run telemetry recording token counts, costs, durations, and outcomes.
- Stock pipeline: default planning, human plan approval, and review-and-fix implement stages in checked-in `lerp.toml`.
- Distribution: installation via `go install`, Homebrew (`mattwalters/tap/lerp`), `install.sh`, and prebuilt release archives for macOS and Linux (amd64 and arm64).
