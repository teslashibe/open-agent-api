# Spec: GPT-5.6 Sol / Terra / Luna Cursor Models

**Status:** Implemented  
**Date:** 2026-07-16  
**Repo:** `open-agent-api`  
**Depends on:** [`docs/gpt-5.6-sol-terra-luna.md`](../gpt-5.6-sol-terra-luna.md) (inventory)

**Resolved open questions:** default=`gpt-5.6-sol`; ship `codex-*` aliases in v1;
do **not** ship `-ultra` effort aliases (`ultra` is invalid for `reasoning.effort`);
1M API-key path remains follow-up.

## Goal

Expose the GPT-5.6 family (Sol, Terra, Luna) as Cursor-selectable custom models through this OpenAI-compatible proxy, with correct upstream slugs, reasoning-effort aliases, and honest context limits.

## Non-goals

- Implementing OpenAI Responses API (`/v1/responses`)
- Implementing Pro mode (`reasoning.mode: "pro"`) or Ultra multi-agent orchestration in this proxy
- Migrating auth from ChatGPT/Codex websocket → developer API keys (tracked as follow-up if 1M context is required)
- Removing legacy `gpt-5.5*` or Spark aliases in the first cut

---

## Can we use the 1M context?

### Verdict

**Not on the current ChatGPT/Codex path that powers this API.**  
Advertise **~272K tokens** for Sol/Terra/Luna in Cursor docs. Do **not** claim 1M until a verified path exists.

### Evidence

| Path | Sol / Terra / Luna context | Source |
| --- | --- | --- |
| OpenAI developer API | **1.05M** tokens (max out 128K) | API model pages |
| Codex CLI ChatGPT catalog (this machine, CLI 0.144.1) | **`context_window` = `max_context_window` = 272K** | `codex debug models` / `~/.codex/models_cache.json` |
| Same catalog, `gpt-5.4` only | `context_window` 272K, **`max_context_window` 1M** | Same cache — 1M is not wired for 5.6 here |
| This proxy defaults | Compacts Agent traffic at **~192 KiB** message bytes (`CODEX_CONTEXT_MAX_BYTES`) | `internal/config/config.go` |

Additional API pricing note (developer API only): prompts **>272K input** are billed at **2× input + 1.5× output** for the full request. Even if we later unlock ~1M via API keys, long context is expensive.

### Implications for this spec

1. Cursor-facing docs and `/v1/models` metadata (if we add any) must say **272K effective** for ChatGPT-backed 5.6.
2. Do **not** raise proxy context compaction to ~1M “because the API card says so” — that will hit upstream `context_length_exceeded` on the Codex websocket.
3. Optional follow-up epic: **API-key provider path** for true 1.05M Sol/Terra/Luna (separate auth, billing, and router). Out of scope for v1 of this change.
4. Soft target for Agent compaction on 5.6: raise toward a **token-aware ~200–250K** budget later if measured safe; keep Spark hard-cap at 96 KiB.

---

## User stories

1. As a Cursor user, I can add `gpt-5.6-sol`, `gpt-5.6-terra`, and `gpt-5.6-luna` as custom models and see them in the picker via `GET /v1/models`.
2. As a Cursor user, I can pick effort variants (e.g. `gpt-5.6-sol-high`, `gpt-5.6-terra-max`) without sending `reasoning_effort` myself.
3. As an operator, default upstream remains safe for my ChatGPT plan; unknown models still pass through.
4. As a Cursor user, Spark and `gpt-5.5*` aliases keep working after the upgrade.

---

## Acceptance criteria

### A. Catalog

- [ ] `GET /v1/models` includes at least:
  - `gpt-5.6` (alias → Sol)
  - `gpt-5.6-sol`, `gpt-5.6-terra`, `gpt-5.6-luna`
  - Effort variants listed in §Alias table
  - Existing `gpt-5.5*` and `gpt-5.3-codex-spark(-preview)` unchanged
- [ ] `DefaultModel` becomes `gpt-5.6-sol` **or** stays `gpt-5.5` behind an explicit config flag — decide in §Open questions before merge; document choice in README.
- [ ] `codex_profile.json` model field matches the chosen default.

### B. Resolution

- [ ] `ResolveModelAlias` maps each public ID → correct `UpstreamModel` + `ReasoningEffort` + `Verbosity`.
- [ ] Request-level `reasoning_effort` / `verbosity` continue to override alias defaults.
- [x] Effort aliases cover `low`…`max` only (no `ultra`; invalid enum upstream).
- [ ] Unknown models still pass through with medium/medium (existing behavior).

### C. Routing / runtime

- [ ] `ProviderForModel` still routes `gpt-5.6*` to Codex (no Gemini/Claude false match).
- [ ] Quota fallback remains Spark (`gpt-5.3-codex-spark`) unless config overrides.
- [ ] No `ContextHardMaxBytes` on Sol/Terra/Luna in v1 (rely on default context management). Spark hard-cap unchanged at 96 KiB.

### D. Docs / Cursor

- [ ] README Cursor section lists 5.6 custom model strings and notes **272K ChatGPT-context**, not 1.05M.
- [ ] Inventory doc linked from README Notes.
- [ ] Example Cursor config block updated.

### E. Validation

- [ ] Unit tests for alias list + resolution updated (existing test files that assert IDs).
- [ ] Manual smoke (ChatGPT auth):
  - `POST /v1/chat/completions` with `gpt-5.6-sol`, `-terra`, `-luna` (stream + non-stream)
  - One Agent-shaped request with tools on Terra
  - Confirm Sol entitlement on the operator account (Plus may reject Sol — fail soft with clear upstream error)

---

## Alias table (v1)

| Public Cursor ID | Upstream | Effort | Verbosity | Notes |
| --- | --- | --- | --- | --- |
| `gpt-5.6` | `gpt-5.6-sol` | medium | low | Convenience alias |
| `gpt-5.6-sol` | `gpt-5.6-sol` | **low** | low | Matches Codex CLI default |
| `gpt-5.6-sol-low` | `gpt-5.6-sol` | low | low | |
| `gpt-5.6-sol-medium` | `gpt-5.6-sol` | medium | low | |
| `gpt-5.6-sol-high` | `gpt-5.6-sol` | high | low | |
| `gpt-5.6-sol-xhigh` | `gpt-5.6-sol` | xhigh | low | |
| `gpt-5.6-sol-max` | `gpt-5.6-sol` | max | low | |
| `gpt-5.6-terra` | `gpt-5.6-terra` | medium | low | Everyday default recommendation |
| `gpt-5.6-terra-low` … `-max` | `gpt-5.6-terra` | * | low | Same ladder as Sol |
| `gpt-5.6-luna` | `gpt-5.6-luna` | medium | low | |
| `gpt-5.6-luna-low` … `-max` | `gpt-5.6-luna` | * | low | |
| `gpt-5.6-luna-fast` | `gpt-5.6-luna` | low | low | Interactive shortcut |

Optional (nice-to-have, same PR or follow-up):

| Public ID | Maps to |
| --- | --- |
| `codex-sol` / `codex-terra` / `codex-luna` | Same as bare tier IDs |

Keep all existing `gpt-5.5*` and Spark rows.

---

## Implementation plan

1. **`internal/openai/openai.go`** — set / document `DefaultModel`.
2. **`internal/openai/models.go`** — add alias rows; keep Spark hard-cap.
3. **`codex_profile.json`** — align default model string.
4. **Tests** — `openai_test.go`, `server_test.go` ID lists + resolve cases for `max`/`ultra`/Luna-no-ultra.
5. **`README.md`** — Cursor custom models + context honesty note.
6. **Smoke** against running local/tunnel instance with ChatGPT auth.

No router changes expected. No new env vars required for v1 (optional later: `CODEX_DEFAULT_MODEL`).

### Effort support check

Today the proxy forwards `reasoning_effort` into the Codex websocket payload. Confirm builder accepts `xhigh` / `max` / `ultra` strings without clamping. If it clamps to `{low,medium,high}`, extend the allowlist before shipping those aliases.

---

## Context policy (v1)

| Model family | Proxy behavior | Documented limit |
| --- | --- | --- |
| `gpt-5.6-*` | Default context management only | **~272K tokens** ChatGPT/Codex |
| `gpt-5.5-*` | Unchanged | ~272K |
| `gpt-5.3-codex-spark*` | `ContextHardMaxBytes = 96KiB` | ~96 KiB hard |

**1M follow-up (separate ticket):**

- Add optional OpenAI API-key provider for `gpt-5.6-*` when `OPENAI_API_KEY` is set.
- Only then advertise 1.05M, with warning about >272K surcharge.
- Keep ChatGPT path as default for Cursor BYOK-to-local-proxy users who rely on Codex login.

---

## Risks

| Risk | Mitigation |
| --- | --- |
| ChatGPT Plus rejects Sol | Document; smoke on operator account; Terra as recommended daily driver |
| `ultra`/`max` rejected upstream | Feature-detect via CLI catalog; surface upstream error; drop aliases if needed |
| Users expect 1M from API cards | Explicit README + this spec verdict |
| Stale local Codex model cache hides 5.6 | Operator note: refresh via `codex debug models` |
| Default cutover to Sol burns quota | Prefer default `gpt-5.6-terra` or keep `gpt-5.5` until confirmed |

---

## Open questions (resolve before coding)

1. **Default model:** `gpt-5.6-sol` vs `gpt-5.6-terra` vs keep `gpt-5.5`?
2. **Friendly `codex-*` aliases** in v1 or later?
3. **Ship `ultra` aliases now**, or wait until Sol entitlement verified on the operator account?
4. Is a **1M API-key path** wanted as a follow-up epic, or permanently out of scope for this proxy?

---

## Test plan

```bash
# catalog
curl -s "$BASE/v1/models" | jq '.data[].id' | grep 5.6

# completions
for m in gpt-5.6-sol gpt-5.6-terra gpt-5.6-luna gpt-5.6-sol-high gpt-5.6-luna-fast; do
  curl -s "$BASE/v1/chat/completions" -H 'Content-Type: application/json' \
    -d "{\"model\":\"$m\",\"messages\":[{\"role\":\"user\",\"content\":\"Reply: OK-$m\"}]}"
done

# unit
go test ./internal/openai/ ./internal/server/ -count=1
```

Cursor: add custom models with Base URL `…/v1`, any API key, model IDs from §Alias table; confirm Agent mode tool calls on Terra.

---

## Done when

All acceptance criteria checked, smoke green on at least Terra + Luna, Sol either green or documented as account-gated, README states **272K not 1M** for the ChatGPT path, and open questions 1–3 answered in the PR description.
