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
NGROK_DOMAIN=YOUR_SUBDOMAIN.ngrok-free.dev NGROK_AUTHTOKEN=... \
  docker compose -f docker-compose.yml -f docker-compose.cursor.yml -f docker-compose.ngrok.yml up --build -d
```

To pool two local Codex accounts, place each account's `auth.json` under
`~/.open-agent-api/accounts/{primary,secondary}/` and add the accounts override:

```bash
docker compose \
  -f docker-compose.yml \
  -f docker-compose.cursor.yml \
  -f docker-compose.accounts.yml \
  up --build -d
```

The override admits 32 Codex requests globally with a hard limit of 16 inflight
requests per account (`2 × 16 = 32`). This avoids the upstream tail-latency
cliff observed at 36–40 active requests. Capacity-aware placement keeps new
conversations balanced within that budget, while tentative then successful
affinity keeps concurrent and subsequent turns from an established Cursor
thread on one account. Affinity-less structured requests remain independently
balanced and never create a global pin. The per-key active limit is 1, so one
conversation cannot execute simultaneous turns while separate Cursor agents
still fan out. Connect-time and first-event rate-limit/quota failures cool the
affected account and retry one alternate. A stream cannot move accounts after
output begins.

The accounts override explicitly keeps Gemini and Claude at 20 active requests;
all limits retain their existing environment-variable override syntax.
Credential directories are mounted read-write only so OAuth refresh can
persist; they remain outside the repository.

| Cursor field | Value |
| --- | --- |
| OpenAI API Key | `local-open-agent-api` (any non-empty string) |
| Override OpenAI Base URL | `https://${NGROK_DOMAIN}/v1` (**not** `http://127.0.0.1…`) |
| Model | e.g. `gpt-5.6-terra` — see [model catalog](https://teslashibe.github.io/open-agent-api/docs/models/catalog) |

`docker-compose.cursor.yml` uses the high-throughput baseline proven by the
shared Cursor/Report Studio gateway: 20 active requests globally and per key,
20 inflight requests per Codex account, and a 192-place queue with a 60-minute
wait budget. Structured inference uses the same active and queue limits.
Interactive work has priority over queued structured work without preempting
active requests. Every value remains overridable through the existing
environment variables. If every account is cooling, structured work waits;
interactive work waits only for saturation and otherwise retains quota/fallback
handling.

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
- `GET /metrics` (Prometheus; authenticated when `GATEWAY_BEARER_SECRET` is set)

JSON honors `Accept-Encoding` (**gzip** / **brotli** / **deflate**). Streaming chat completions stay uncompressed so SSE flushes stay timely.

Metrics are enabled by default. Key chat series are
`codex_chat_api_request_duration_seconds`, `codex_chat_api_tokens_total`, and
`codex_chat_api_fast_tier_requests_total`. The Cursor compose profile sets
`CODEX_STREAM_IDLE_TIMEOUT=0s`: valid reasoning can be silent for minutes, so
that profile relies on cancellation and the unchanged 45-minute overall timeout.
Codex model IDs containing `-fast` request the upstream `priority` service tier,
which is faster but consumes increased upstream usage. Gemini, Claude, Spark,
and other models without advertised priority support do not receive synthetic
Fast aliases.

```bash
go run ./cmd/open-agent-api --host 127.0.0.1 --port 8088
curl -s http://127.0.0.1:8088/v1/models | jq '.data[].id'
```

## License

[MIT](LICENSE) — and mind the upstream provider terms noted above.
