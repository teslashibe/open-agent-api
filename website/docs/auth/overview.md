---
sidebar_position: 1
title: Auth overview
description: How Codex, Antigravity, and Claude Code credentials get into the proxy.
---

# Auth overview

Upstream auth uses **local CLI / OAuth credentials** — not OpenAI `sk-` keys. The dummy key you put in Cursor or an SDK is only there so the client thinks it’s talking to OpenAI.

| Surface | Credential | Setup |
| --- | --- | --- |
| **Codex / ChatGPT** | `~/.codex/auth.json` | [`codex login`](./codex); chat plus account usage/redemption |
| **Gemini / Antigravity** | `~/.gemini/antigravity_oauth_creds.json` (preferred) or `oauth_creds.json` | [Antigravity sync](./antigravity) |
| **Claude Code** | `claude` CLI login / `CLAUDE_CODE_OAUTH_TOKEN` | [Claude Code](./claude-code) |

## Local / Docker

Compose mounts host auth directories (and reads Claude OAuth from `.env`). Finish logins on the host before `docker compose up`.

## Kubernetes

Credentials land as Secret keys (`CODEX_AUTH_JSON`, `GEMINI_OAUTH_JSON`, `CLAUDE_CODE_OAUTH_TOKEN`) plus a shared `GATEWAY_BEARER_TOKEN` for inbound `/v1` auth. See [Install on Kubernetes](../install/kubernetes).

## Which providers are on

`GATEWAY_PROVIDERS` (default `codex,gemini,claude`) controls which surfaces get built and show up in `/v1/models`. Drop `claude` if you want Claude Code off entirely (common on some free-tier gateways).
