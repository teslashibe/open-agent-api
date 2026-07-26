# GPT-5.6 Sol / Terra / Luna — Model Inventory

Inventoried **2026-07-16** from:

1. OpenAI API model pages + [Using GPT-5.6](https://developers.openai.com/api/docs/guides/latest-model) + [launch post](https://openai.com/index/gpt-5-6/) (GA 2026-07-09)
2. Local Codex CLI **0.144.1** live catalog (`codex debug models` → `~/.codex/models_cache.json`, `client_version=0.144.1`)
3. This repo’s current Cursor-facing aliases (`internal/openai/models.go`)

## Naming model

| Concept | Meaning |
| --- | --- |
| Generation | `5.6` — training generation |
| Tier | **Sol** (flagship), **Terra** (balanced), **Luna** (fast/cheap) — durable capability tiers that can advance independently |
| Alias | `gpt-5.6` → routes to **`gpt-5.6-sol`** (API) |

Earlier GPT-5 families roughly map as: Sol ≈ unsuffixed flagship, Terra ≈ mini, Luna ≈ nano.

---

## Official API catalog (OpenAI developer platform)

| Display | Model ID | Alias | Role | Input / Output (per 1M tok) | Cached input | Context | Max out | Knowledge cutoff |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| GPT-5.6 Sol | `gpt-5.6-sol` | `gpt-5.6` | Frontier / complex professional + coding | $5 / $30 | $0.50 | **1.05M** | 128K | Feb 16, 2026 |
| GPT-5.6 Terra | `gpt-5.6-terra` | — | Balance intelligence vs cost (~old mini) | $2.50 / $15 | $0.25 | **1.05M** | 128K | Feb 16, 2026 |
| GPT-5.6 Luna | `gpt-5.6-luna` | — | Cost-sensitive / high-volume (~old nano) | $1 / $6 | $0.10 | **1.05M** | 128K | Feb 16, 2026 |

### API reasoning / modes

Supported `reasoning.effort` on all three (API docs):

`none` · `low` · `medium` · `high` · `xhigh` · `max`

Additional API controls (not separate model slugs):

| Control | How | Notes |
| --- | --- | --- |
| **Pro mode** | `reasoning.mode: "pro"` | More model work → single final answer; higher latency/cost. Keep same Sol/Terra/Luna ID. ChatGPT UI may label this “Sol Pro”. |
| **Ultra** | Product/Codex setting; API analogue is multi-agent beta | Coordinates parallel subagents (default ~4). Not an effort string on Luna in Codex CLI (see below). |
| **Verbosity** | `text.verbosity`: `low` / `medium` / `high` | Independent of effort |
| **Programmatic Tool Calling** | Responses API tool | ZDR-compatible in-memory tool orchestration |
| **Multi-agent** | Responses API beta | Ultra-like parallel subagents |
| **Prompt cache** | Implicit + explicit breakpoints | Writes billed **1.25×** uncached input; reads keep ~90% discount; 30m min cache life |
| **Long prompts** | >272K input | **2×** input + **1.5×** output for the full request |

Modalities (all three): text+image in, text out. Tools: functions, web search, file search, computer use, MCP, code interpreter, apply_patch, etc. (Responses API surface).

Endpoints listed for all three: Chat Completions + Responses (primary for reasoning/tools).

---

## Codex CLI live catalog (ChatGPT-auth path)

Source: `codex debug models` on **2026-07-16**, CLI **0.144.1**.

### Listed models (priority order)

| Priority | Slug | Visibility | Default effort | Efforts | Context (CLI) | Max context | In OpenAI API? | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | `gpt-5.6-sol` | list | **low** | low, medium, high, xhigh, **max**, **ultra** | 272K | 272K | yes | “Latest frontier agentic coding model.” `multi_agent_version=v2`, `tool_mode=code_mode_only`, `use_responses_lite=true`, speed tier `fast` / service tier `priority` |
| 2 | `gpt-5.6-terra` | list | medium | low…max, **ultra** | 272K | 272K | yes | “Balanced agentic coding model for everyday work.” Same multi-agent v2 / code_mode_only |
| 3 | `gpt-5.6-luna` | list | medium | low…**max** (**no ultra**) | 272K | 272K | yes | “Fast and affordable agentic coding model.” `multi_agent_version=v1` |
| 7 | `gpt-5.5` | list | medium | low…xhigh | 272K | 272K | yes | Previous flagship still listed |
| 16 | `gpt-5.4` | list | medium | low…xhigh | 272K | **1M** | yes | Everyday coding |
| 23 | `gpt-5.4-mini` | list | medium | low…xhigh | 272K | 272K | yes | Small/fast |
| 26 | `gpt-5.3-codex-spark` | list | **high** | low…xhigh | **128K** | 128K | **no** (`supported_in_api=false`) | Ultra-fast Codex-only; text-only input |
| 43 | `codex-auto-review` | **hide** | medium | low…xhigh | 272K | 1M | yes | Internal approval reviewer |

Bundled CLI catalog (no network) also ships Sol/Terra/Luna; it reported **372K** context for 5.6 tiers vs **272K** in the refreshed live cache. Treat **272K as the ChatGPT/Codex session window** for this account/CLI; API docs still advertise **1.05M** for API-key Responses usage.

Shared Codex defaults for Sol/Terra/Luna:

- `default_verbosity`: **low**
- `support_verbosity`: true
- `input_modalities`: text + image (Spark is text-only)
- `additional_speed_tiers`: `["fast"]`
- `service_tiers`: priority / “Fast” (1.5× speed, increased usage)
- `visibility`: list (picker-visible)

### Effort descriptions (Codex)

| Effort | Description |
| --- | --- |
| `low` | Fast responses with lighter reasoning |
| `medium` | Balances speed and reasoning depth for everyday tasks |
| `high` | Greater reasoning depth for complex problems |
| `xhigh` | Extra high reasoning depth for complex problems |
| `max` | Maximum reasoning depth for the hardest problems |
| `ultra` | Maximum reasoning with automatic task delegation (**Sol + Terra only** in CLI) |

### Plan / entitlement notes (product docs + field reports)

From OpenAI launch notes:

| Surface | Free / Go | Plus | Pro / Business / Enterprise |
| --- | --- | --- | --- |
| ChatGPT chat | Limited (Terra-oriented) | Sol via medium+ effort; Sol Pro on higher plans | Sol + Sol Pro |
| ChatGPT Work + Codex | Terra | Sol, Terra, Luna + effort; **max** toggleable; **ultra** for Plus+ in Codex | Same + Work ultra for Pro/Enterprise |

Field caveats (GitHub `openai/codex`, Jul 2026):

- Stale `~/.codex/models_cache.json` (old `client_version`) can hide Sol/Terra/Luna from `/model` even when `-m gpt-5.6-*` works. Fix: stop stale Codex processes, delete cache, run `codex debug models`.
- Some ChatGPT Plus users report `gpt-5.6-sol` rejected (“not supported when using Codex with a ChatGPT account”) while Terra/Luna work — treat Sol entitlement as **account-gated** until verified live.

---

## Per-tier deep dive

### Sol (`gpt-5.6-sol`)

- **Role:** Flagship agentic coding + professional work.
- **When to use:** Hard multi-file debugging, architecture, long tool chains, quality-first tasks.
- **API alias:** `gpt-5.6`
- **Codex default effort:** `low` (unlike Terra/Luna medium — intentional lean default).
- **Ultra:** Yes (CLI + Codex Plus+).
- **Pro:** API `reasoning.mode=pro` (not a separate slug).
- **Pricing:** $5 / $30 per 1M (same headline rate as GPT-5.5; better efficiency claimed).

### Terra (`gpt-5.6-terra`)

- **Role:** Everyday agentic coding; positioned competitive with GPT-5.5 at ~½ price.
- **When to use:** Default daily driver for features, tests, reviews, medium refactors.
- **Codex default effort:** `medium`
- **Ultra:** Yes in CLI catalog.
- **Pricing:** $2.50 / $15 per 1M.
- **Free/Go Codex path:** Terra is the accessible 5.6 tier.

### Luna (`gpt-5.6-luna`)

- **Role:** Fastest / cheapest 5.6 tier.
- **When to use:** Boilerplate, quick scripts, high-volume interactive turns, latency-sensitive drafts.
- **Codex default effort:** `medium`
- **Ultra:** **Not** in CLI `supported_reasoning_levels` (stops at `max`).
- **Pricing:** $1 / $6 per 1M.
- **Multi-agent version in CLI metadata:** `v1` (Sol/Terra are `v2`).

---

## Gap vs `open-chat-api` today

Current Cursor-facing Codex defaults in this repo:

| Exposed today | Missing for 5.6 |
| --- | --- |
| Default `gpt-5.5` + effort aliases (`-low`, `-high`, `-fast`, …) | `gpt-5.6-sol`, `gpt-5.6-terra`, `gpt-5.6-luna`, alias `gpt-5.6` |
| `gpt-5.3-codex-spark` (+ `-preview`) | Effort aliases for `max` / `ultra` |
| Effort: low / medium / high only in alias table | `xhigh`, `max`, `ultra` (and API `none`) |
| Verbosity: low / medium / high via aliases | Align defaults (Codex 5.6 defaults verbosity **low**) |
| No Sol/Terra/Luna in `/v1/models` | Cursor cannot pick them until aliases are added |

Passthrough already forwards unknown model IDs upstream with medium/medium defaults — so `gpt-5.6-sol` may work today if the ChatGPT account allows it, but it will **not** appear in Cursor’s model picker until listed in `modelAliases`.

---

## Suggested Cursor custom model IDs (for next implementation)

Public IDs for `/v1/models` → upstream + effort/verbosity:

```
gpt-5.6                 → gpt-5.6-sol     effort=medium  verbosity=low
gpt-5.6-sol             → gpt-5.6-sol     effort=low*    verbosity=low   (*Codex default)
gpt-5.6-sol-low|medium|high|xhigh|max|ultra
gpt-5.6-terra[-low|medium|high|xhigh|max|ultra]
gpt-5.6-luna[-low|medium|high|xhigh|max]   # no ultra
```

Optional friendly aliases: `codex-sol`, `codex-terra`, `codex-luna`.

Keep existing `gpt-5.5*` and `gpt-5.3-codex-spark*` for compatibility.

---

## How to re-inventory

```bash
codex --version                    # expect recent CLI (0.144.1+)
rm -f ~/.codex/models_cache.json   # if picker/cache looks stale
codex debug models > /tmp/codex-models.json
python3 -c "import json; d=json.load(open('/tmp/codex-models.json'));
print([(m['slug'], [e['effort'] for e in m['supported_reasoning_levels']]) for m in d['models']])"
```

Smoke entitlement (ChatGPT auth):

```bash
codex exec --ephemeral --skip-git-repo-check -m gpt-5.6-sol   'Reply: OK-sol'
codex exec --ephemeral --skip-git-repo-check -m gpt-5.6-terra 'Reply: OK-terra'
codex exec --ephemeral --skip-git-repo-check -m gpt-5.6-luna  'Reply: OK-luna'
```

---

## Sources

- https://openai.com/index/gpt-5-6/ (2026-07-09)
- https://developers.openai.com/api/docs/models
- https://developers.openai.com/api/docs/models/gpt-5.6-sol
- https://developers.openai.com/api/docs/models/gpt-5.6-terra
- https://developers.openai.com/api/docs/models/gpt-5.6-luna
- https://developers.openai.com/api/docs/guides/latest-model
- Local: `codex debug models` / `~/.codex/models_cache.json` (CLI 0.144.1, fetched 2026-07-16T17:51:38Z)
