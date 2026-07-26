---
sidebar_position: 1
title: Install
description: Clone the repo, start the API, and make your first chat completion.
---

# Install

End-to-end path from an empty machine to a working completion. Pick Docker (recommended) or Go.

## 1. Clone

```bash
git clone https://github.com/teslashibe/open-chat-api.git
cd open-chat-api
```

## 2. Authenticate upstream

At least one surface is required. Codex is the default path:

```bash
# Codex / ChatGPT (required for gpt-* models)
codex login

# Optional — Gemini 3.x / Antigravity
scripts/sync-antigravity-auth.sh

# Optional — Claude Code CLI
# install Claude Code, then complete `claude` login
```

Details: [Auth overview](../auth/overview).

## 3. Start the API

### Docker (recommended)

Requires Docker + Docker Compose.

```bash
docker compose up --build -d
```

API listens on **`http://127.0.0.1:8088`**. More detail: [Install with Docker](./docker).

### Go (no Docker)

Requires Go 1.24+.

```bash
go run ./cmd/open-chat-api --host 127.0.0.1 --port 8088
```

## 4. Health check

```bash
curl -s http://127.0.0.1:8088/health
# → {"status":"ok"}

curl -s http://127.0.0.1:8088/v1/models | jq '.data[].id'
```

## 5. Make a chat completion

```bash
curl -s http://127.0.0.1:8088/v1/chat/completions \
  -H 'authorization: Bearer local-open-chat-api' \
  -H 'content-type: application/json' \
  -d '{
    "model": "gpt-5.6-terra",
    "messages": [
      {"role": "user", "content": "Say hi in five words."}
    ]
  }' | jq .
```

You should get an OpenAI-shaped JSON response with `choices[0].message.content`. Streaming:

```bash
curl -N http://127.0.0.1:8088/v1/chat/completions \
  -H 'authorization: Bearer local-open-chat-api' \
  -H 'content-type: application/json' \
  -d '{
    "model": "gpt-5.6-terra",
    "stream": true,
    "messages": [
      {"role": "user", "content": "Count to five."}
    ]
  }'
```

The bearer value can be any non-empty string when `GATEWAY_BEARER_SECRET` is unset (local default).

## Next

- [Cursor BYOK + ngrok](../cursor/byok-ngrok) — point Cursor Agent at a public HTTPS tunnel
- [Model catalog](../models/catalog) — all public model IDs
- [Kubernetes](./kubernetes) — in-cluster gateway for apps
