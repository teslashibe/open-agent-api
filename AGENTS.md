# Agent bootstrap — open-agent-api

OpenAI-compatible Go HTTP proxy for **Cursor BYOK** and any OpenAI SDK client. Routes `POST /v1/chat/completions` across three upstream surfaces — no `sk-` API keys.

| Surface | Auth | Models |
| --- | --- | --- |
| **Codex / ChatGPT** | `~/.codex/auth.json` (`codex login`) | GPT-5.6 Sol/Terra/Luna, GPT-5.5, Spark |
| **Gemini / Antigravity** | `~/.gemini/antigravity_oauth_creds.json` (preferred) or CLI oauth | Gemini 2.5/3.x + Antigravity gateway IDs |
| **Claude Code** | `claude` on PATH + CLI login | `haiku`, `sonnet`, `opus`, `fable`, dated/`api/claude-*` IDs |

Default model when `model` is omitted: **`gpt-5.6-sol`**.

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

Endpoints: `GET /health`, `GET /v1/models`, `POST /v1/chat/completions` (stream + non-stream).

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
| Claude Code fast | `haiku` |

Full tables: [`internal/openai/models.go`](internal/openai/models.go) and [website/docs/models/catalog.md](website/docs/models/catalog.md).

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
