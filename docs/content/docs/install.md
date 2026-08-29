---
title: Install
summary: Platforms, the ways to get the binary, and the prerequisites lerp cannot satisfy for you.
weight: 30
---

# Install

Lerp runs on macOS and Linux. Under WSL2 it is Linux as far as lerp is
concerned.

## Get the binary

With Homebrew:

```sh
brew install mattwalters/tap/lerp
```

With a Go toolchain:

```sh
go install github.com/mattwalters/lerp/cmd/lerp@latest
```

With curl:

```sh
curl -fsSL https://raw.githubusercontent.com/mattwalters/lerp/main/install.sh | sh
```

Prebuilt binaries are on the
[releases page](https://github.com/mattwalters/lerp/releases).

## Hacking on lerp

To work on lerp itself, build from a clone:

```sh
git clone https://github.com/mattwalters/lerp
cd lerp
make install
```

[CONTRIBUTING.md](CONTRIBUTING.md) has the rest.

Whichever route you took, `lerp version` prints what you got.

## What else you need

- Make sure Git is installed.
- Linear credentials. Either from `lerp login` or a `LINEAR_API_KEY`.
- Coding agents. One or more of Claude Code CLI, Codex CLI, or
  Antigravity CLI.
- Linear automations that move tickets mid-run break the pipeline. Lerp
  names the offenders at startup, and
  [troubleshooting](troubleshooting.md#why-did-my-ticket-skip-a-stage)
  has the fix.
- Read [SECURITY.md](SECURITY.md). Anyone with write access to your
  Linear team could run code on your machine.
