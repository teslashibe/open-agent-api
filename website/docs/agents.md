---
sidebar_position: 7
---

# Agents

If you’re a coding agent (Cursor Agent, Codex, Claude Code, and friends), start with the repo-root **[AGENTS.md](https://github.com/teslashibe/open-agent-api/blob/main/AGENTS.md)**. That’s the canonical bootstrap.

## Quick version

This service is an OpenAI-compatible proxy in front of Codex/ChatGPT, Gemini/Antigravity, and Claude Code — still no `sk-` keys.

**Run it:**

```bash
codex login && go run ./cmd/open-agent-api --host 127.0.0.1 --port 8088
# or: docker compose up --build -d
```

**Check it’s alive:**

```bash
curl -s http://127.0.0.1:8088/health
curl -s http://127.0.0.1:8088/v1/models | jq '.data[].id'
```

**Cursor BYOK:** use a dummy API key, a public `https://<tunnel>/v1` base URL (not localhost), and a model from the [catalog](./models/catalog) (e.g. `gpt-5.6-terra`). Ngrok overlay: `docker compose -f docker-compose.yml -f docker-compose.ngrok.yml up -d`. Tool wire details: [tool conventions](./cursor/tool-conventions).

**Please don’t:**

- Invent model slugs — take them from [`internal/openai/models.go`](https://github.com/teslashibe/open-agent-api/blob/main/internal/openai/models.go) or live `/v1/models`
- Commit secrets, or casually bind-mount `~/.claude.json`
- Change aliases in `models.go` without updating the [model catalog](./models/catalog)

**Useful paths:** `cmd/open-agent-api/`, `internal/openai/models.go`, `docker-compose*.yml`, `scripts/sync-antigravity-auth.sh`.

The full do/don’t list and doc map live in [AGENTS.md](https://github.com/teslashibe/open-agent-api/blob/main/AGENTS.md).
