---
sidebar_position: 1
title: Auth overview
description: Credential surfaces for Codex, Antigravity, and Claude Code.
---

# Auth overview

Upstream auth uses **local CLI / OAuth credentials**, not OpenAI `sk-` API keys. The dummy key you put in Cursor or an SDK is only for client compatibility.

| Surface | Credential | Setup |
| --- | --- | --- |
| **Codex / ChatGPT** | `~/.codex/auth.json` | [`codex login`](./codex) |
| **Gemini / Antigravity** | `~/.gemini/antigravity_oauth_creds.json` (preferred) or `oauth_creds.json` | [Antigravity sync](./antigravity) |
| **Claude Code** | `claude` CLI login / `CLAUDE_CODE_OAUTH_TOKEN` | [Claude Code](./claude-code) |

## Local / Docker

Compose mounts host auth directories (and reads Claude OAuth from `.env`). Complete logins on the host before `docker compose up`.

## Kubernetes

Credentials are delivered as Secret keys (`CODEX_AUTH_JSON`, `GEMINI_OAUTH_JSON`, `CLAUDE_CODE_OAUTH_TOKEN`) plus a shared `GATEWAY_BEARER_TOKEN` for inbound `/v1` auth. See [Install on Kubernetes](../install/kubernetes).

## Provider allowlist

`GATEWAY_PROVIDERS` (default `codex,gemini,claude`) controls which surfaces are constructed and listed in `/v1/models`. Omitting `claude` disables Claude Code entirely (common for some free-tier gateways).
