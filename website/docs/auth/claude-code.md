---
sidebar_position: 3
title: Claude Code
description: Authenticate the Claude Code CLI upstream.
---

# Claude Code auth

Haiku / Sonnet / Opus / Fable (and `api/claude-*` aliases) go through the local **`claude`** CLI — not through Antigravity.

:::tip
Some IDs like `claude-sonnet-4-6` can also ride **Antigravity**. That’s a different surface — see [Antigravity](./antigravity) and the [model catalog](../models/catalog).
:::

## Setup

1. Install [Claude Code](https://docs.anthropic.com/en/docs/claude-code) so `claude` is on your `PATH`.
2. Finish CLI login until `claude` works interactively on the host.

## Configuration

| Setting | Env / flag | Notes |
| --- | --- | --- |
| Executable | `CLAUDE_EXECUTABLE` / `CLAUDE_PATH` / `--claude-executable` | Default `claude` |
| OAuth access | `CLAUDE_CODE_OAUTH_TOKEN` | Preferred for Docker/K8s |
| OAuth refresh | `CLAUDE_CODE_OAUTH_REFRESH_TOKEN` | Optional |
| Anthropic token | `ANTHROPIC_AUTH_TOKEN` | Optional alternate |

## Docker

- Mounts `${HOME}/.claude` into the container.
- **Does not** bind `~/.claude.json` — the host CLI replaces that file via atomic rename and leaves the container with a corrupted/stale inode.
- Pass `CLAUDE_CODE_OAUTH_TOKEN` (and refresh if needed) via `.env`.

## Disable / allowlist

Claude Code is skipped entirely when `GATEWAY_PROVIDERS` omits `claude`.

## Kubernetes

`CLAUDE_CODE_OAUTH_TOKEN` is injected from Secret `open-agent-api-secrets` (env, not a file mount). Rotate by updating the SOPS secret and restarting the Deployment.
