---
sidebar_position: 5
title: API
description: OpenAI-compatible endpoints, response compression, and auth notes.
---

# API

Open Agent API speaks a small OpenAI-compatible surface:

| Method | Path | Notes |
| --- | --- | --- |
| `GET` | `/health`, `/health/live` | Liveness — always `{"status":"ok"}` when the process is up |
| `GET` | `/health/ready` | Readiness — `503` while draining |
| `GET` | `/v1/models` | Model list (filtered by `GATEWAY_PROVIDERS`) |
| `POST` | `/v1/chat/completions` | Streaming and non-streaming chat |

When `GATEWAY_BEARER_SECRET` is set, `/v1/*` requires `Authorization: Bearer …`. `/health*` stays open for probes. Locally the secret is usually unset, so any non-empty bearer works.

## Response compression

JSON responses are compressed when the client asks for it.

Send an `Accept-Encoding` header; the server may reply with **gzip**, **brotli**, or **deflate** and set `Content-Encoding` accordingly (via Fiber’s compress middleware).

```bash
curl -s -H 'Accept-Encoding: gzip' -H 'authorization: Bearer local-open-agent-api' \
  --compressed http://127.0.0.1:8088/v1/models | jq '.data | length'
```

`--compressed` makes curl decode the body for you.

**Streaming chat completions are not compressed.** Agent / SSE responses (`"stream": true`) stay uncompressed so flushes are not buffered into one blob. That is intentional for Cursor BYOK.

## See also

- [Install](./install/) — clone → first completion
- [Cursor Agent tool calling](./cursor/tool-conventions) — tools / `delta.tool_calls`
- [Model catalog](./models/catalog) — public IDs
