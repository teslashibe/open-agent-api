---
sidebar_position: 1
---

# Open Agent API

A small Go proxy that looks like the OpenAI Chat Completions API. Point Cursor BYOK (or any OpenAI SDK) at it, and it talks to the upstreams you already have logged in — no OpenAI `sk-` keys.

| Surface | Auth | What it serves |
| --- | --- | --- |
| **Codex / ChatGPT** | `codex login` → `~/.codex/auth.json` | GPT-5.6 Sol/Terra/Luna, GPT-5.5, Spark |
| **Gemini / Antigravity** | Antigravity or Gemini CLI oauth | Gemini 2.5/3.x + Antigravity gateway Claude/GPT-OSS IDs |
| **Claude Code** | `claude` CLI login | Haiku / Sonnet / Opus / Fable |

Default model is **`gpt-5.6-sol`**. Current release: **v0.0.25**.

## Quick start

```bash
git clone https://github.com/teslashibe/open-agent-api.git
cd open-agent-api
codex login
docker compose up --build -d
curl -s http://127.0.0.1:8088/health
curl -s http://127.0.0.1:8088/v1/chat/completions \
  -H 'authorization: Bearer local-open-agent-api' \
  -H 'content-type: application/json' \
  -d '{"model":"gpt-5.6-terra","messages":[{"role":"user","content":"Say hi in five words."}]}' | jq .
```

Want the longer version? See [Install](./install/). For Gemini 3.x, run `scripts/sync-antigravity-auth.sh` after login.

## Where to go next

- [Install](./install/) — local run, Docker, ngrok
- [Auth](./auth/) — Codex, Antigravity, Claude Code
- [Cursor](./cursor/) — BYOK settings and Agent mode
- [Cursor tool conventions](./cursor/tool-conventions) — how tool calls travel over the wire
- [Model catalog](./models/catalog) — public IDs and good Cursor picks
- [Contributing](./contributing) — tests, CI, PR habits
- [Agents](./agents) — short bootstrap for coding agents ([`AGENTS.md`](https://github.com/teslashibe/open-agent-api/blob/main/AGENTS.md) is the full one)

## API at a glance

- `GET /health`
- `GET /v1/models` — filtered by `GATEWAY_PROVIDERS` (default `codex,gemini,claude`)
- `POST /v1/chat/completions` — streaming and non-streaming

JSON can be compressed (gzip / brotli / deflate) when the client sends `Accept-Encoding`. Streaming Agent turns stay uncompressed so SSE doesn’t get stuck in a buffer — details in [API](./api).

Model aliases live in [`internal/openai/models.go`](https://github.com/teslashibe/open-agent-api/blob/main/internal/openai/models.go).
