# Agent bootstrap — open-agent-api

OpenAI-compatible Go HTTP proxy for **Cursor BYOK** and any OpenAI SDK client. Routes `POST /v1/chat/completions` across three upstream surfaces — no `sk-` API keys.

| Surface | Auth | Models |
| --- | --- | --- |
| **Codex / ChatGPT** | `~/.codex/auth.json` (`codex login`) | GPT-6.1 Sol, GPT-6 Astra/Sol/Luna, GPT-5.6 Sol/Terra/Luna, GPT-5.5, Spark |
| **Gemini / Antigravity** | `~/.gemini/antigravity_oauth_creds.json` (preferred) or CLI oauth | Gemini 2.5/3.x + Antigravity gateway IDs |
| **Claude Code** | `claude` on PATH + CLI login | `haiku`, `sonnet`, `opus`, `fable`, `api/claude-opus-5-5[-effort]`, `api/claude-fable-5-1[-effort]`, `api/claude-sonnet-5[-effort]` |

Default model when `model` is omitted: **`gpt-5.6-sol`**. GPT-6 Astra exposes
`low` through `max`; Ultra is intentionally unsupported because official
Codex Ultra adds proactive multi-agent delegation, while this gateway makes
one upstream API request and cannot provide that orchestration honestly.

## Run instantly

```bash
git clone https://github.com/teslashibe/open-agent-api.git
cd open-agent-api
codex login                                 # required for Codex models
# scripts/sync-antigravity-auth.sh          # optional Gemini 3.x / Antigravity

docker compose up --build -d                # or: go run ./cmd/open-agent-api --host 127.0.0.1 --port 8088
curl -s http://127.0.0.1:8088/health
curl -s http://127.0.0.1:8088/v1/chat/completions \
  -H 'authorization: Bearer local-open-agent-api' \
  -H 'content-type: application/json' \
  -d '{"model":"gpt-5.6-terra","messages":[{"role":"user","content":"Say hi in five words."}]}' | jq .
```

## Auth prerequisites (one-liners)

```bash
codex login                                    # Codex → ~/.codex/auth.json
scripts/sync-antigravity-auth.sh               # Antigravity → ~/.gemini/antigravity_oauth_creds.json
claude --version && claude                     # Claude Code CLI login (optional)
```

## Health and discovery

```bash
curl -s http://127.0.0.1:8088/health
curl -s http://127.0.0.1:8088/v1/models | jq '.data[].id'
```

Endpoints: `GET /health`, `GET /v1/models`, `POST /v1/chat/completions` (stream + non-stream), plus bearer-protected Codex account usage and reset-credit redemption under `/v1/accounts`. Redemption requires `confirm: true` and a caller-generated UUID in `redeem_request_id`.

`GATEWAY_PROVIDERS` (default `codex,gemini,claude`) filters `/v1/models` listing and routable models.

## Cursor BYOK + ngrok (one-liner)

```bash
NGROK_AUTHTOKEN=... docker compose -f docker-compose.yml -f docker-compose.ngrok.yml up -d
```

Cursor settings: API key = any non-empty string (e.g. `local-open-agent-api`); base URL = `https://YOUR_SUBDOMAIN.ngrok-free.dev/v1` (ngrok overlay). **Cursor does not allow localhost** — private URLs fail with "Access to private networks is forbidden".

## Key model slugs

| Use case | ID |
| --- | --- |
| Default | `gpt-5.6-sol` |
| Everyday Agent | `gpt-5.6-terra` |
| Hard tasks | `gpt-5.6-sol-high` |
| Fast Codex | `gpt-5.6-luna-fast` |
| Fast Gemini | `gemini-3.1-flash-lite` |
| Claude Code (Cursor) | `api/claude-opus-5-5-high` — Cursor needs the `api/` prefix |
| Claude Code fast | `api/claude-opus-5-5-fast` |

Full tables: [`internal/openai/models.go`](internal/openai/models.go) and [website/docs/models/catalog.md](website/docs/models/catalog.md).

## Bulk Luna throughput (structured inference)

Load-tested 2026-10-12 on the local Cursor/Report Studio gateway with two pooled ChatGPT accounts, using a real tagging workload (40 customer-feedback records per request, about 2.5k output tokens).

Peak: 6,300 to 6,850 records/min at 80 requests in flight (40 per account), with p50 latency of about 13 s per 40-record batch. At 100, 120 and 160 in flight, upstream throttled and throughput fell to 4,000 to 5,600 records/min while latency tripled. The gateway caps therefore sit at the peak, and extra client concurrency waits in the queue instead of reaching upstream.

| In flight | Records/min | p50 latency |
| --- | --- | --- |
| 40 | 3,233 | 13 s |
| 60 | 5,142 | 13 s |
| 80 | 6,295 / 6,853 (two runs) | 13 s |
| 100 | 3,991 | 27 s |
| 120 | 5,594 | 20 s |
| 160 | 4,341 | 47 s |

Recommended request shape for bulk extraction:

- `POST /v1/structured/inference` with `model: "gpt-5.6-luna-fast"` and `reasoning_effort: "low"`. On `/v1/chat/completions`, `gpt-5.6-luna-fast-low` took about 40 s for the same batch and `gpt-5.6-luna-medium` about 70 s. Chat throughput peaked near 2,900 records/min. The strict schema had zero parse failures across about 1,700 batches (0.4% hit transient upstream 502s, which a retry clears); on chat, 5 to 10% of Luna replies carried a stray trailing `]}`.
- 80 concurrent requests across two accounts. Scale this with the number of pooled accounts.

### Gateway settings

`docker-compose.cursor.yml` defaults:

| Variable | Value | Why |
| --- | --- | --- |
| `CODEX_CLIENT_MAX_INFLIGHT` | 40 | per-account peak |
| `CODEX_AGENT_MAX_ACTIVE` | 80 | global cap for agent turns and, while `CODEX_AGENT_QUEUE_ENABLED=true`, for structured inference too |
| `CODEX_AGENT_MAX_ACTIVE_PER_KEY` | 20 | per Cursor conversation, and per structured caller while the agent queue is on |
| `STRUCTURED_MAX_ACTIVE` / `_PER_KEY` | 80 | used only when the agent queue is disabled |

Structured inference shares the Codex agent queue whenever `CODEX_AGENT_QUEUE_ENABLED` is true, so `STRUCTURED_MAX_ACTIVE` has no effect in that mode. Plain `/v1/chat/completions` without tools bypasses the agent queue, and `CODEX_CLIENT_MAX_INFLIGHT × accounts` is its only gateway limit. The pool waits for a free slot when saturated; it doesn't reject.

Admission is keyed per caller: the tenant header if `GATEWAY_TENANT_HEADER` is set, else a hash of `Authorization`, else `structured:global`. The cross-process lock in `acquireDistributedLock` holds up to `maxPerKey` slot files per key (`<hash>.lock`, `<hash>.1.lock`, and so on), so one caller gets `CODEX_AGENT_MAX_ACTIVE_PER_KEY` concurrent requests. Gateways older than this change locked a single file per key and ran each caller one request at a time. To exceed the per-key limit from one client, give each lane its own `Authorization` value (any value works while `GATEWAY_BEARER_SECRET` is empty) or its own tenant header value.

The account pool is a local override layered last, and it stays out of Git because it names accounts:

```bash
docker compose -p open-agent-api-cursor \
  -f docker-compose.yml -f docker-compose.cursor.yml \
  -f ~/.open-agent-api/compose.pool-accounts.yml \
  up -d --no-build api
```

```yaml
# ~/.open-agent-api/compose.pool-accounts.yml
services:
  api:
    environment:
      CODEX_CLIENTS: '[{"label":"tertiary","codex_home":"/home/codex/accounts/tertiary","auth_path":"/home/codex/accounts/tertiary/auth.json"},{"label":"quaternary","codex_home":"/home/codex/accounts/quaternary","auth_path":"/home/codex/accounts/quaternary/auth.json"}]'
    volumes:
      - "${HOME}/.open-agent-api/accounts/tertiary:/home/codex/accounts/tertiary"
      - "${HOME}/.open-agent-api/accounts/quaternary:/home/codex/accounts/quaternary"
```

Pool only accounts dedicated to the gateway. If an account is also logged in for local development, the gateway's copy of its tokens and the developer's copy refresh independently and can revoke each other. When that happens, the upstream usage poll reports `auth_error` even though the access token hasn't expired.

To check capacity, `GET /metrics` exposes `codex_chat_api_client_inflight`, `codex_chat_api_client_pool_queue_depth` and `codex_chat_api_upstream_rate_limit{field="used_percent"}`. That last one is the weekly window, in whole percents. The full load test, about 95k records, used 4% of each account's week.

## Important files

| Path | Purpose |
| --- | --- |
| `cmd/open-agent-api/` | Main entrypoint |
| `internal/openai/models.go` | Model alias catalog (source of truth) |
| `internal/server/` | HTTP handlers, routing, queue |
| `docker-compose.yml` | Default Docker run |
| `docker-compose.ngrok.yml` | Ngrok overlay for Cursor |
| `scripts/sync-antigravity-auth.sh` | Host keyring → Antigravity oauth file |

## Do / don't

**Do:**

- Use only model IDs from `models.go` or live `GET /v1/models`.
- Run `go build ./...`, `go vet ./...`, `gofmt -l $(go list -f '{{.Dir}}' ./...)`, and `go test -race ./...` before release — CI runs exactly these and blocks the image build on them.
- Keep `website/docs/models/catalog.md` in sync when changing aliases.

**Don't:**

- Commit secrets (`auth.json`, OAuth files, bearer tokens, ngrok tokens).
- Bind `~/.claude.json` into containers casually.
- Invent model IDs — unknown IDs pass through raw; documented aliases are in `models.go` only.

## Website docs

Local site: `cd website && npm start` (docs under `website/docs/`).

| Doc | Path |
| --- | --- |
| Intro | [website/docs/intro.md](website/docs/intro.md) |
| Docker install | [website/docs/install/docker.md](website/docs/install/docker.md) |
| Kubernetes | [website/docs/install/kubernetes.md](website/docs/install/kubernetes.md) |
| Auth | [website/docs/auth/overview.md](website/docs/auth/overview.md) |
| Cursor BYOK + ngrok | [website/docs/cursor/byok-ngrok.md](website/docs/cursor/byok-ngrok.md) |
| Cursor tool conventions | [website/docs/cursor/tool-conventions.md](website/docs/cursor/tool-conventions.md) |
| Model catalog | [website/docs/models/catalog.md](website/docs/models/catalog.md) |
| Contributing | [website/docs/contributing.md](website/docs/contributing.md) |
| Agents (site) | [website/docs/agents.md](website/docs/agents.md) |

Human README: [README.md](README.md).
