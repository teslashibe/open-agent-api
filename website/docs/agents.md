---
sidebar_position: 7
---

# Agents

For coding agents (Cursor Agent, Codex, Claude Code, etc.), start with the repo-root **[AGENTS.md](https://github.com/teslashibe/open-agent-api/blob/main/AGENTS.md)** — the canonical bootstrap for this service.

## Summary

**What it is:** OpenAI-compatible proxy routing to Codex/ChatGPT, Gemini/Antigravity, and Claude Code CLI — no `sk-` keys.

**Run:**

```bash
codex login && go run ./cmd/open-agent-api --host 127.0.0.1 --port 8088
# or: docker compose up --build -d
```

**Verify:**

```bash
curl -s http://127.0.0.1:8088/health
curl -s http://127.0.0.1:8088/v1/models | jq '.data[].id'
```

**Cursor BYOK:** dummy API key + `https://<tunnel>/v1` base URL + model from [catalog](./models/catalog) (e.g. `gpt-5.6-terra`). Ngrok: `docker compose -f docker-compose.yml -f docker-compose.ngrok.yml up -d`. Agent tool wire: [tool conventions](./cursor/tool-conventions).

**Rules for agents:**

- Model IDs only from [`internal/openai/models.go`](https://github.com/teslashibe/open-agent-api/blob/main/internal/openai/models.go) or live `/v1/models` — do not invent slugs.
- No secrets in commits; do not casually bind `~/.claude.json`.
- Update [model catalog](./models/catalog) when changing aliases in `models.go`.

**Key paths:** `cmd/open-agent-api/`, `internal/openai/models.go`, `docker-compose*.yml`, `scripts/sync-antigravity-auth.sh`.

See [AGENTS.md](https://github.com/teslashibe/open-agent-api/blob/main/AGENTS.md) for the full table, do/don't list, and doc index.
