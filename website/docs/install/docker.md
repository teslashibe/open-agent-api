---
sidebar_position: 2
title: Install with Docker
description: Run open-chat-api locally with Docker Compose.
---

# Install with Docker

## Prerequisites

- Docker and Docker Compose
- Host credentials already set up (see [Auth](../auth/overview)):
  - `~/.codex` from `codex login` (required for Codex models)
  - `~/.gemini` after `scripts/sync-antigravity-auth.sh` (Gemini 3.x / Antigravity)
  - Claude Code OAuth via `.env` `CLAUDE_CODE_OAUTH_TOKEN` (optional; do **not** bind `~/.claude.json`)

## Start

From the repo root:

```bash
docker compose up --build -d
```

This builds/runs image `teslashibe/open-chat-api:local` and binds the API to **`127.0.0.1:8088`**.

## Verify

```bash
curl -s http://127.0.0.1:8088/health
```

Expected:

```json
{"status":"ok"}
```

List models:

```bash
curl -s http://127.0.0.1:8088/v1/models | jq '.data[].id'
```

## What Compose mounts

| Host / volume | Container | Notes |
| --- | --- | --- |
| `${HOME}/.codex` | `/home/codex/.codex` | Read-only |
| `${HOME}/.gemini` | `/home/codex/.gemini` | Writable (OAuth refresh) |
| `${HOME}/.claude` | `/home/codex/.claude` | CLI home; auth token still comes from env |
| `agent-queue-locks` | `/var/lib/open-chat-api/agent-locks` | Shared agent-queue locks |

Compose deliberately **does not** bind-mount `~/.claude.json` (host CLI atomic renames leave a stale inode in the container).

## Important environment

Defaults live in `docker-compose.yml`. Notable ones:

| Variable | Default / role |
| --- | --- |
| `GEMINI_AUTH_PATH` | `/home/codex/.gemini/antigravity_oauth_creds.json` |
| `CODEX_AGENT_QUEUE_ENABLED` | `true` |
| `CODEX_AGENT_MAX_ACTIVE` | `4` (compose; binary default is `2`) |
| `CODEX_AGENT_MAX_ACTIVE_PER_KEY` | `1` |
| `CODEX_AGENT_QUEUE_KEY_MODE` | `cursor` |
| `CLAUDE_CODE_OAUTH_TOKEN` | From host `.env` (optional) |
| `CODEX_CHAT_API_PORT` | Host port mapping (default `8088`) |

Put secrets in a local `.env` (gitignored), never in committed files.

## Cursor / public HTTPS

Most Cursor BYOK paths need a tunnel. Use the ngrok overlay:

```bash
NGROK_AUTHTOKEN=... docker compose -f docker-compose.yml -f docker-compose.ngrok.yml up -d
```

Full steps: [Cursor BYOK + ngrok](../cursor/byok-ngrok).

## Logs and stop

```bash
docker compose logs -f api
docker compose down
```
