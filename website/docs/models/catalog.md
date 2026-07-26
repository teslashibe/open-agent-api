---
sidebar_position: 1
---

# Model catalog

OpenAI-compatible **public IDs** resolved server-side before the upstream call.

**Default model** (when the client omits `model`): **`gpt-5.6-sol`**

**Source of truth:** [`internal/openai/models.go`](https://github.com/teslashibe/codex-chat-api/blob/main/internal/openai/models.go)

**Listing filter:** `GATEWAY_PROVIDERS` (default `codex,gemini,claude`) controls which providers appear in `GET /v1/models` and can be completed. Disabled providers return `404 model not found`. Unknown IDs pass through with default effort/verbosity.

**Discovery:**

```bash
curl -s http://127.0.0.1:8088/v1/models | jq '.data[].id'
```

GPT-5.6 ChatGPT/Codex context is ~272K tokens (not the API card's 1.05M).

`ultra` is a Codex product multi-agent mode, **not** a `reasoning.effort` value — there is no `-ultra` alias.

---

## Surface: Codex / ChatGPT

Routed when the upstream model is not Gemini/Claude/Antigravity-gateway. Auth: `CODEX_HOME` / `CODEX_AUTH_PATH` (`~/.codex/auth.json`).

### GPT-5.6 Sol (default tier)

| Public ID | Upstream | Effort | Verbosity |
| --- | --- | --- | --- |
| `gpt-5.6-sol` | `gpt-5.6-sol` | `low` | `low` |
| `gpt-5.6` | `gpt-5.6-sol` | `medium` | `low` |
| `gpt-5.6-sol-low` | `gpt-5.6-sol` | `low` | `low` |
| `gpt-5.6-sol-medium` | `gpt-5.6-sol` | `medium` | `low` |
| `gpt-5.6-sol-high` | `gpt-5.6-sol` | `high` | `low` |
| `gpt-5.6-sol-xhigh` | `gpt-5.6-sol` | `xhigh` | `low` |
| `gpt-5.6-sol-max` | `gpt-5.6-sol` | `max` | `low` |
| `codex-sol` | `gpt-5.6-sol` | `low` | `low` |

### GPT-5.6 Terra

| Public ID | Upstream | Effort | Verbosity |
| --- | --- | --- | --- |
| `gpt-5.6-terra` | `gpt-5.6-terra` | `medium` | `low` |
| `gpt-5.6-terra-low` | `gpt-5.6-terra` | `low` | `low` |
| `gpt-5.6-terra-medium` | `gpt-5.6-terra` | `medium` | `low` |
| `gpt-5.6-terra-high` | `gpt-5.6-terra` | `high` | `low` |
| `gpt-5.6-terra-xhigh` | `gpt-5.6-terra` | `xhigh` | `low` |
| `gpt-5.6-terra-max` | `gpt-5.6-terra` | `max` | `low` |
| `codex-terra` | `gpt-5.6-terra` | `medium` | `low` |

### GPT-5.6 Luna

| Public ID | Upstream | Effort | Verbosity |
| --- | --- | --- | --- |
| `gpt-5.6-luna` | `gpt-5.6-luna` | `medium` | `low` |
| `gpt-5.6-luna-low` | `gpt-5.6-luna` | `low` | `low` |
| `gpt-5.6-luna-medium` | `gpt-5.6-luna` | `medium` | `low` |
| `gpt-5.6-luna-high` | `gpt-5.6-luna` | `high` | `low` |
| `gpt-5.6-luna-xhigh` | `gpt-5.6-luna` | `xhigh` | `low` |
| `gpt-5.6-luna-max` | `gpt-5.6-luna` | `max` | `low` |
| `gpt-5.6-luna-fast` | `gpt-5.6-luna` | `low` | `low` |
| `codex-luna` | `gpt-5.6-luna` | `medium` | `low` |

### GPT-5.5 (legacy)

| Public ID | Upstream | Effort | Verbosity |
| --- | --- | --- | --- |
| `gpt-5.5` | `gpt-5.5` | `medium` | `medium` |
| `gpt-5.5-low` | `gpt-5.5` | `low` | `medium` |
| `gpt-5.5-high` | `gpt-5.5` | `high` | `medium` |
| `gpt-5.5-fast` | `gpt-5.5` | `low` | `low` |
| `gpt-5.5-mini` | `gpt-5.5` | `low` | `low` |
| `gpt-5.5-lite` | `gpt-5.5` | `low` | `medium` |
| `gpt-5.5-deep` | `gpt-5.5` | `high` | `medium` |
| `gpt-5.5-verbose` | `gpt-5.5` | `medium` | `high` |
| `gpt-5.5-fast-verbose` | `gpt-5.5` | `low` | `high` |

### Spark (overflow / small context)

96 KiB hard context. Faithful Codex turns may inject `image_generation`, which Spark rejects — use `faithful:false` or client `tools` for plain chat.

| Public ID | Upstream | Effort | Verbosity |
| --- | --- | --- | --- |
| `gpt-5.3-codex-spark` | `gpt-5.3-codex-spark` | `low` | `low` |
| `gpt-5.3-codex-spark-preview` | `gpt-5.3-codex-spark` | `low` | `low` |

---

## Surface: Gemini / Antigravity

Routed for `gemini-*` and the Antigravity gateway IDs below. Auth: `GEMINI_AUTH_PATH` (prefer Antigravity oauth for 3.x). Run `scripts/sync-antigravity-auth.sh` before Docker.

### Gemini 2.5 (CLI oauth)

| Public ID | Upstream | Effort | Verbosity |
| --- | --- | --- | --- |
| `gemini-2.5-flash` | `gemini-2.5-flash` | `medium` | `medium` |
| `gemini-2.5-flash-lite` | `gemini-2.5-flash-lite` | `medium` | `medium` |
| `gemini-2.5-pro` | `gemini-2.5-pro` | `medium` | `medium` |

### Gemini 3.x (Antigravity oauth)

Some public IDs remap to Cloud Code Assist wire IDs:

| Public ID | Upstream (wire) | Effort | Verbosity |
| --- | --- | --- | --- |
| `gemini-3.1-pro-low` | `gemini-3.1-pro-low` | `medium` | `medium` |
| `gemini-3.1-pro-high` | `gemini-pro-agent` | `medium` | `medium` |
| `gemini-3.5-flash-low` | `gemini-3.5-flash-extra-low` | `medium` | `medium` |
| `gemini-3.5-flash-medium` | `gemini-3.5-flash-low` | `medium` | `medium` |
| `gemini-3.5-flash-high` | `gemini-3-flash-agent` | `medium` | `medium` |
| `gemini-3.1-flash-lite` | `gemini-3.1-flash-lite` | `medium` | `medium` |
| `gemini-3-flash` | `gemini-3-flash` | `medium` | `medium` |

### Antigravity gateway (non-Gemini IDs)

These look like Claude/GPT names but **do not** use the Claude Code CLI — they go through Antigravity / Cloud Code Assist. They remain available when `GATEWAY_PROVIDERS=codex,gemini` (Claude Code disabled).

| Public ID | Upstream | Effort | Verbosity |
| --- | --- | --- | --- |
| `claude-sonnet-4-6` | `claude-sonnet-4-6` | `medium` | `medium` |
| `claude-opus-4-6-thinking` | `claude-opus-4-6-thinking` | `medium` | `medium` |
| `gpt-oss-120b-medium` | `gpt-oss-120b-medium` | `medium` | `medium` |

---

## Surface: Claude Code CLI

Routed for Claude Code short names, dated IDs, and `api/claude-*` prefixes. Auth: local `claude` executable + Claude Code login. Disabled when `GATEWAY_PROVIDERS` omits `claude`.

| Public ID | Upstream | Effort | Verbosity |
| --- | --- | --- | --- |
| `opus` | `opus` | `medium` | `medium` |
| `sonnet` | `sonnet` | `medium` | `medium` |
| `haiku` | `haiku` | `medium` | `medium` |
| `fable` | `fable` | `medium` | `medium` |
| `claude-opus-4-8` | `claude-opus-4-8` | `medium` | `medium` |
| `claude-sonnet-5` | `claude-sonnet-5` | `medium` | `medium` |
| `claude-haiku-4-5-20251001` | `claude-haiku-4-5-20251001` | `medium` | `medium` |
| `claude-fable-5` | `claude-fable-5` | `medium` | `medium` |
| `api/claude-opus-4-8` | `claude-opus-4-8` | `medium` | `medium` |
| `api/claude-sonnet-5` | `claude-sonnet-5` | `medium` | `medium` |
| `api/claude-haiku-4-5-20251001` | `claude-haiku-4-5-20251001` | `medium` | `medium` |
| `api/claude-fable-5` | `claude-fable-5` | `medium` | `medium` |
| `api/claude-fable-5-low` | `claude-fable-5` | `low` | `medium` |
| `api/claude-fable-5-medium` | `claude-fable-5` | `medium` | `medium` |
| `api/claude-fable-5-high` | `claude-fable-5` | `high` | `medium` |

---

## Cursor picks (recommended)

| Use case | Model ID |
| --- | --- |
| Everyday Agent | `gpt-5.6-terra` |
| Hard tasks | `gpt-5.6-sol-high` |
| Fast Codex | `gpt-5.6-luna-fast` |
| Fastest cheap turn | `gemini-3.1-flash-lite` |
| Gemini Pro (Antigravity) | `gemini-3.1-pro-high` |
| Antigravity Claude | `claude-sonnet-4-6` |
| Claude Code Haiku | `haiku` |
| Overflow / tiny context | `gpt-5.3-codex-spark` |
