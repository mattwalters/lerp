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

- Linear credentials. Either from `lerp login` or a `LINEAR_API_KEY`.
- Coding agents. One or more of Claude Code CLI, Codex CLI, or
  Antigravity CLI. Each one needs its own Linear access, set up below.
- Linear automations that move tickets mid-run break the pipeline. Lerp
  names the offenders at init and every start, and
  [troubleshooting](troubleshooting.md#why-did-my-ticket-skip-a-stage)
  has the fix.
- Read [SECURITY.md](SECURITY.md). Anyone with write access to your
  Linear team could run code on your machine.

## Give the agents Linear access

Lerp hands a runner a prompt and a ticket identifier, nothing more. The
agent reads the ticket from Linear itself, so every agent CLI needs its
own Linear access, set up once in that CLI. Do it before your first run.

Your access is not the agents'. Lerp drops `LINEAR_API_KEY` from the
environment of every process it launches, and `lerp login` writes its
token outside the repository, so neither one reaches an agent.

Linear's [MCP documentation](https://linear.app/docs/mcp) has the
authoritative endpoint. For the built-in vendor adapters:

* **Claude Code (`claude`)**:
  ```sh
  claude mcp add --transport http linear https://mcp.linear.app/mcp
  ```
  Then start `claude` and run `/mcp` to complete the OAuth flow.
* **Antigravity (`agy`)**:
  ```sh
  agy mcp add linear https://mcp.linear.app/mcp
  ```
  Then complete the OAuth flow in agy's own settings. Tokens land in
  `~/.gemini/antigravity-cli/mcp_oauth_tokens.json` and refresh
  automatically.
* **Codex (`codex`)**:
  ```sh
  codex mcp add linear --url https://mcp.linear.app/mcp
  ```
  Then run `codex mcp login linear`.

To share one browser login across several CLIs, register the stdio
bridge in each instead of the URL:

```sh
<cli> mcp add linear -- npx -y mcp-remote https://mcp.linear.app/mcp
```

Every CLI then reads the token `mcp-remote` caches in `~/.mcp-auth`. It
puts Node and npx on the agent path, and the shared cache misbehaves
across multiple Linear accounts.
