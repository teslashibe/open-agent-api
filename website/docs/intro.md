---
sidebar_position: 1
---

# Open Chat API

OpenAI-compatible Go HTTP proxy for **Cursor BYOK** and any OpenAI SDK client. It routes chat completions across three upstream surfaces without OpenAI `sk-` API keys.

| Surface | Auth | What it serves |
| --- | --- | --- |
| **Codex / ChatGPT** | `codex login` → `~/.codex/auth.json` | GPT-5.6 Sol/Terra/Luna, GPT-5.5, Spark |
| **Gemini / Antigravity** | Antigravity or Gemini CLI oauth | Gemini 2.5/3.x + Antigravity gateway Claude/GPT-OSS IDs |
| **Claude Code** | `claude` CLI login | Haiku / Sonnet / Opus / Fable |

Default model: **`gpt-5.6-sol`**. Current release: **v0.0.25**.

## Quick start

```bash
codex login
go run ./cmd/open-chat-api --host 127.0.0.1 --port 8088
curl -s http://127.0.0.1:8088/health
```

For Gemini 3.x / Antigravity before Docker: `scripts/sync-antigravity-auth.sh`.

## Docs

- [Install](./install/) — local run, Docker Compose, ngrok
- [Auth](./auth/) — Codex, Antigravity, Claude Code prerequisites
- [Cursor](./cursor/) — BYOK settings, tunnels, Agent mode
- [Cursor tool conventions](./cursor/tool-conventions) — OpenRouter-style tool wire contracts
- [Model catalog](./models/catalog) — full alias tables and Cursor picks
- [Contributing](./contributing) — develop, test, CI, PR expectations
- [Agents](./agents) — coding-agent bootstrap (see also root [`AGENTS.md`](https://github.com/teslashibe/open-chat-api/blob/main/AGENTS.md))

## API surface

- `GET /health`
- `GET /v1/models` — filtered by `GATEWAY_PROVIDERS` (default `codex,gemini,claude`)
- `POST /v1/chat/completions` — streaming and non-streaming

Model aliases are defined in [`internal/openai/models.go`](https://github.com/teslashibe/open-chat-api/blob/main/internal/openai/models.go).
