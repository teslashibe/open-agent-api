---
sidebar_position: 1
---

# Model catalog

These are the OpenAI-compatible **public IDs** clients should send. The server resolves them before calling upstream.

**Default model** (when the client omits `model`): **`gpt-5.6-sol`**

**Source of truth:** [`internal/openai/models.go`](https://github.com/teslashibe/open-agent-api/blob/main/internal/openai/models.go)

`GATEWAY_PROVIDERS` (default `codex,gemini,claude`) decides which providers show up in `GET /v1/models` and can actually complete. Disabled providers return `404 model not found`. Unknown raw IDs retain passthrough compatibility. Unknown Claude IDs do not get a guessed effort; explicit unreviewed effort/fast controls are rejected.

To see what’s live:

```bash
curl -s http://127.0.0.1:8088/v1/models | jq '.data[].id'
```

GPT-5.6 ChatGPT/Codex context is ~272K tokens (not the API card’s 1.05M).

Ultra is intentionally unsupported. Official Codex Ultra adds proactive
multi-agent delegation beyond `max` reasoning; this gateway makes one upstream
API request and cannot provide that orchestration honestly. Raw Astra
`reasoning.effort` values stop at `max`.

---

## Surface: Codex / ChatGPT

Routed when the upstream model is not Gemini/Claude/Antigravity-gateway. Auth: `CODEX_HOME` / `CODEX_AUTH_PATH` (`~/.codex/auth.json`).

### Current CLI models and controls

Reviewed on October 2, 2026 against Codex desktop CLI 0.159.2 `model/list`.
The older Homebrew CLI 0.154.0 returns a smaller catalog. Model availability
still depends on the signed-in account. The wrapper sends compatibility version
0.159.2 with an explicit API-wrapper user agent.

| Canonical model | Supported effort | Normal and Fast |
| --- | --- | --- |
| `gpt-6.1-sol` | `low`, `medium`, `high`, `xhigh`, `max` | Both |
| `gpt-6-astra` | `low`, `medium`, `high`, `xhigh`, `max` | Both |
| `gpt-6-sol` | `low`, `medium`, `high`, `xhigh`, `max` | Both |
| `gpt-6-luna` | `low`, `medium`, `high`, `xhigh`, `max` | Both |
| `gpt-5.6-sol` | `low`, `medium`, `high`, `xhigh`, `max` | Both |
| `gpt-5.6-terra` | `low`, `medium`, `high`, `xhigh`, `max` | Both |
| `gpt-5.6-luna` | `low`, `medium`, `high`, `xhigh`, `max` | Both |
| `gpt-5.5` | `low`, `medium`, `high`, `xhigh` | Both |

Every row exposes the base ID and `-effort`, `-normal`, `-normal-effort`,
`-fast`, and `-fast-effort` aliases. For example:
`gpt-6.1-sol-normal-max` and `gpt-6.1-sol-fast-max`. Fast requests upstream
`service_tier: priority`; normal uses the default tier. Existing bare Sol and
Astra aliases retain `low`; the other current bare IDs retain `medium`.
Fast aliases retain their existing `low` default. GPT-6.1 Sol uses low verbosity,
as do the GPT-6 Sol/Luna and GPT-5.6 families; Astra and GPT-5.5 retain medium.

Alternatively send the canonical model with `reasoning_effort` and
`speed: "normal"` or `speed: "fast"`. Explicit fields override alias controls.
An unsupported effort, speed or recognized model variant returns HTTP 400 before
provider work. CLI metadata does not advertise the API-only `none` effort.
Ultra requires multi-agent orchestration and returns an explicit unsupported-mode
error; it is never mapped to `max`.

The default stays `gpt-5.6-sol`. Existing `gpt-5.6`, `codex-sol`, `codex-terra`,
`codex-luna` and GPT-5.5 mini/lite/deep/verbose aliases retain their mappings.
Historical `gpt-5.4` aliases retain low through xhigh and normal/priority behavior;
they are compatibility aliases, not part of the current eight-model CLI inventory.
GPT-5.5 ChatGPT sign-in access is scheduled to retire October 14, 2026; retained
aliases do not guarantee upstream availability after retirement.

Public model details: [GPT-6.1 Sol](https://developers.openai.com/api/docs/models/gpt-6.1-sol).

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

Routed for Claude Code short names, `claude-*` IDs, and `api/` or `anthropic/` prefixed IDs. Auth: the pinned `claude` executable plus a Claude Code login (`CLAUDE_CODE_OAUTH_TOKEN` in Docker). Disabled when `GATEWAY_PROVIDERS` omits `claude`.

:::warning Cursor: use `api/claude-*` IDs
Cursor sends any model whose name starts with `claude-` to its **Anthropic** key slot, not to the OpenAI base URL override, so bare `claude-*` IDs never reach this gateway from Cursor. Add the `api/claude-*` (or `anthropic/claude-*`) IDs instead. The request's `reasoning_effort` overrides the alias effort.
:::

The Docker image pins Claude Code 2.1.286. Reviewed against that CLI's runtime metadata and the official
[model configuration](https://code.claude.com/docs/en/model-config) and
[fast mode](https://code.claude.com/docs/en/fast-mode) documentation.

| Canonical model | Supported effort | Fast |
| --- | --- | --- |
| `claude-fable-5-1` | `low`, `medium`, `high`, `xhigh`, `max` | No |
| `claude-opus-5-5` | `low`, `medium`, `high`, `xhigh`, `max` | Yes |
| `claude-sonnet-5-5` | `low`, `medium`, `high`, `xhigh`, `max` | No |
| `claude-haiku-4-5-20251001` | No effort control | No |
| `claude-fable-5` | `low`, `medium`, `high`, `xhigh`, `max` | No |
| `claude-opus-5` | `low`, `medium`, `high`, `xhigh`, `max` | Yes |
| `claude-opus-4-8` | `low`, `medium`, `high`, `xhigh`, `max` | Yes |
| `claude-opus-4-7` | `low`, `medium`, `high`, `xhigh`, `max` | No |
| `claude-opus-4-6` | `low`, `medium`, `high`, `max` | No |
| `claude-opus-4-5-20251101` | `low`, `medium`, `high` | No |
| `claude-sonnet-5` | `low`, `medium`, `high`, `xhigh`, `max` | No |
| `claude-sonnet-4-6` | `low`, `medium`, `high`, `max` | No |
| `claude-sonnet-4-5-20250929` | No effort control, deprecated | No |

Each model exposes bare, `api/` and `anthropic/` aliases, plus supported
`-effort`, `-normal` and `-normal-effort` variants. Fast-capable rows also expose
`-fast` and `-fast-effort`. The bare `claude-sonnet-4-6` ID preserves its existing
Antigravity route; use `api/claude-sonnet-4-6` for Claude Code.
The dated Haiku 4.5, Opus 4.5 and Sonnet 4.5 IDs also have undated aliases.
Sonnet 4.5 is scheduled to retire November 30, 2026.

The unsuffixed `opus`, `sonnet`, `fable` and `haiku` aliases let the CLI choose
its latest model. Their effort/speed variants pin the reviewed current model.
When no effort is supplied, the gateway omits `--effort`, allowing each CLI
model's default. Currently Opus 5.5 and Sonnet 5.5 default to medium, Opus 4.7 to
xhigh, and the other effort-capable models to high. Haiku and Sonnet 4.5 have no
effort control. Opus 4.5 supports only low/medium/high. Unsupported efforts are
rejected rather than silently downgraded.

Claude normal requests set `fastMode: false`; fast requests set `fastMode: true`
through the CLI's `--settings` option and require Claude Code 2.1.205 or newer.
Only Opus 5.5, Opus 5 and Opus 4.8 accept fast requests. Other combinations return
HTTP 400, preventing the CLI from switching to an Opus model implicitly.
Account permissions and usage credits still apply. Claude may fall back to
standard execution during rate limits, exhausted credits or unavailable fast
access. Request `speed` describes the requested mode. Response `usage.speed`
is included only if the provider reports `fast` or `standard`; it is never
inferred from the request, and the gateway does not calculate a fast surcharge.
Restricted Mythos models are not advertised as Claude Code models: the reviewed
CLI inventory does not establish their execution controls or account access.

Example:

```json
{"model":"api/claude-opus-5-5","reasoning_effort":"max","speed":"fast","messages":[{"role":"user","content":"Hello"}]}
```

---

## Cursor picks (recommended)

| Use case | Model ID |
| --- | --- |
| Everyday Agent | `gpt-5.6-terra` |
| Hard tasks | `gpt-5.6-sol-high` |
| Fast Codex | `gpt-5.6-sol-fast` |
| Fast everyday Agent | `gpt-5.6-terra-fast` |
| Fast lightweight Agent | `gpt-5.6-luna-fast` |
| Fastest cheap turn | `gemini-3.1-flash-lite` |
| Gemini Pro (Antigravity) | `gemini-3.1-pro-high` |
| Claude Code Opus 5.5 | `api/claude-opus-5-5-high` |
| Claude Code Fable 5.1 | `api/claude-fable-5-1` |
| Claude Code Sonnet 5.5 | `api/claude-sonnet-5-5` |
| Claude Code Haiku | `api/claude-haiku-4-5` |
| Overflow / tiny context | `gpt-5.3-codex-spark` |
