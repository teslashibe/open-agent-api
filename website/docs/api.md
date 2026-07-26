---
sidebar_position: 5
title: API
description: Endpoints, auth, and response compression.
---

# API

The public surface is intentionally small — enough to look like OpenAI Chat Completions:

| Method | Path | Notes |
| --- | --- | --- |
| `GET` | `/health`, `/health/live` | Liveness — `{"status":"ok"}` when the process is up |
| `GET` | `/health/ready` | Readiness — `503` while draining |
| `GET` | `/v1/models` | Model list (filtered by `GATEWAY_PROVIDERS`) |
| `POST` | `/v1/chat/completions` | Streaming and non-streaming chat |

If you set `GATEWAY_BEARER_SECRET`, `/v1/*` needs `Authorization: Bearer …`. Health endpoints stay open for probes. Locally the secret is usually unset, so any non-empty bearer is fine.

## Response compression

Ask for it with `Accept-Encoding` and you’ll often get **gzip**, **brotli**, or **deflate** back (Fiber’s compress middleware), with `Content-Encoding` set to match.

```bash
curl -s -H 'Accept-Encoding: gzip' -H 'authorization: Bearer local-open-agent-api' \
  --compressed http://127.0.0.1:8088/v1/models | jq '.data | length'
```

`--compressed` tells curl to decode the body for you.

**Streaming chat completions skip compression.** Agent / SSE traffic (`"stream": true`) stays raw so flushes aren’t held until a whole blob is ready. That’s deliberate for Cursor BYOK.

## See also

- [Install](./install/) — from clone to first completion
- [Cursor Agent tool calling](./cursor/tool-conventions) — tools / `delta.tool_calls`
- [Model catalog](./models/catalog) — public IDs
