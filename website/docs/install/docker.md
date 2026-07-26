---
sidebar_position: 2
title: Install with Docker
description: Clone the repo and run open-agent-api with Docker Compose.
---

# Install with Docker

Full local path: clone → auth → Compose → completion.

## Prerequisites

- Git
- Docker and Docker Compose
- Host credentials (see [Auth](../auth/overview)):
  - `~/.codex` from `codex login` (required for Codex models)
  - `~/.gemini` after `scripts/sync-antigravity-auth.sh` (Gemini 3.x / Antigravity)
  - Claude Code OAuth via `.env` `CLAUDE_CODE_OAUTH_TOKEN` (optional; do **not** bind `~/.claude.json`)

## Clone and start

```bash
git clone https://github.com/teslashibe/open-agent-api.git
cd open-agent-api

codex login
# optional:
# scripts/sync-antigravity-auth.sh

docker compose up --build -d
```

This builds/runs image `teslashibe/open-agent-api:local` and binds the API to **`127.0.0.1:8088`**.

## Verify and call

```bash
curl -s http://127.0.0.1:8088/health
# → {"status":"ok"}

curl -s http://127.0.0.1:8088/v1/models | jq '.data[].id'

curl -s http://127.0.0.1:8088/v1/chat/completions \
  -H 'authorization: Bearer local-open-agent-api' \
  -H 'content-type: application/json' \
  -d '{
    "model": "gpt-5.6-terra",
    "messages": [{"role":"user","content":"Say hi in five words."}]
  }' | jq .
```

## What Compose mounts

| Host / volume | Container | Notes |
| --- | --- | --- |
| `${HOME}/.codex` | `/home/codex/.codex` | Read-only |
| `${HOME}/.gemini` | `/home/codex/.gemini` | Writable (OAuth refresh) |
| `${HOME}/.claude` | `/home/codex/.claude` | CLI home; auth token still comes from env |
| `agent-queue-locks` | `/var/lib/open-agent-api/agent-locks` | Shared agent-queue locks |

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

## Cursor / public HTTPS (required for Cursor)

Cursor **does not allow localhost** for BYOK (`Access to private networks is forbidden`). Always use the ngrok overlay:

```bash
NGROK_AUTHTOKEN=... docker compose -f docker-compose.yml -f docker-compose.ngrok.yml up -d
```

Cursor base URL: `https://YOUR_SUBDOMAIN.ngrok-free.dev/v1` — never `http://127.0.0.1:8088/v1`.

Full steps: [Cursor BYOK + ngrok](../cursor/byok-ngrok).

## Logs and stop

```bash
docker compose logs -f api
docker compose down
```
