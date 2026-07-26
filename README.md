# Open Agent API

[![License: MIT](https://img.shields.io/badge/License-MIT-0d7377.svg)](LICENSE)
[![Docs](https://img.shields.io/badge/docs-GitHub%20Pages-14212b.svg)](https://teslashibe.github.io/open-agent-api/)

**Docs:** https://teslashibe.github.io/open-agent-api/

A Go proxy that looks like OpenAI Chat Completions. Point Cursor BYOK (or any OpenAI SDK) at it and it uses the CLI/OAuth sessions you already have — no OpenAI `sk-` keys:

| Surface | Auth | Serves |
| --- | --- | --- |
| **Codex / ChatGPT** | `codex login` → `~/.codex/auth.json` | GPT-5.6 Sol/Terra/Luna, GPT-5.5, Spark |
| **Gemini / Antigravity** | `scripts/sync-antigravity-auth.sh` | Gemini 2.5/3.x + Antigravity gateway IDs |
| **Claude Code** | `claude` CLI login / OAuth env | Haiku / Sonnet / Opus / Fable |

> The Codex path is reverse-engineered from `codex_cli_rs`. Stay within each provider’s terms. Licensed under [MIT](LICENSE).

## Quick start

```bash
git clone https://github.com/teslashibe/open-agent-api.git
cd open-agent-api

codex login
scripts/sync-antigravity-auth.sh   # optional: Gemini 3.x / Antigravity

docker compose up --build -d
curl -s http://127.0.0.1:8088/health
curl -s http://127.0.0.1:8088/v1/chat/completions \
  -H 'authorization: Bearer local-open-agent-api' \
  -H 'content-type: application/json' \
  -d '{"model":"gpt-5.6-terra","messages":[{"role":"user","content":"Say hi in five words."}]}' | jq .
```

Cursor BYOK **can’t use localhost** — tunnel it with your own ngrok domain:

```bash
NGROK_AUTHTOKEN=... docker compose -f docker-compose.yml -f docker-compose.ngrok.yml up -d
```

| Cursor field | Value |
| --- | --- |
| OpenAI API Key | `local-open-agent-api` (any non-empty string) |
| Override OpenAI Base URL | `https://YOUR_SUBDOMAIN.ngrok-free.dev/v1` (**not** `http://127.0.0.1…`) |
| Model | e.g. `gpt-5.6-terra` — see [model catalog](https://teslashibe.github.io/open-agent-api/docs/models/catalog) |

## Documentation

The full guide is on GitHub Pages (install, auth, Cursor tools, models, contributing):

**https://teslashibe.github.io/open-agent-api/**

Local preview:

```bash
cd website && npm install && npm start
```

Coding agents: see [`AGENTS.md`](AGENTS.md).

## API

- `GET /health`
- `GET /v1/models`
- `POST /v1/chat/completions` (stream + non-stream)

JSON honors `Accept-Encoding` (**gzip** / **brotli** / **deflate**). Streaming chat completions stay uncompressed so SSE flushes stay timely.

```bash
go run ./cmd/open-agent-api --host 127.0.0.1 --port 8088
curl -s http://127.0.0.1:8088/v1/models | jq '.data[].id'
```

## License

[MIT](LICENSE) — and mind the upstream provider terms noted above.
