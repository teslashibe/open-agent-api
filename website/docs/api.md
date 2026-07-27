---
sidebar_position: 5
title: API
description: Endpoints, auth, and response compression.
---

# API

The public surface is intentionally small — enough to look like OpenAI Chat Completions:

| Method | Path | Notes |
| --- | --- | --- |
| `GET` | `/health`, `/health/live` | Liveness — `{"status":"ok"}` when the process is up |
| `GET` | `/health/ready` | Readiness — `503` while draining |
| `GET` | `/v1/models` | Model list (filtered by `GATEWAY_PROVIDERS`) |
| `POST` | `/v1/chat/completions` | Streaming and non-streaming chat |
| `POST` | `/v1/structured/inference` | Structured JSON extraction — **off by default** |

If you set `GATEWAY_BEARER_SECRET`, `/v1/*` needs `Authorization: Bearer …`. Health endpoints stay open for probes. Locally the secret is usually unset, so any non-empty bearer is fine.

## Build provenance

`GET /health` and `GET /health/live` return the contract version and the provenance of the running binary alongside the usual `"status":"ok"`:

```json
{
  "status": "ok",
  "contract_version": "1.1.0",
  "build": {"version": "v0.1.0", "commit": "6fba3e4", "build_date": "2026-07-26T20:04:11Z", "go_version": "go1.24.0", "modified": false}
}
```

Stamp `version` / `commit` / `build_date` at image build time with the `BUILD_VERSION`, `BUILD_COMMIT`, and `BUILD_DATE` build args (`docker-compose.yml` passes them through from your environment). Without them the gateway falls back to the Go toolchain's VCS stamps.

## Structured inference

A non-streaming endpoint for callers that want machine-consumable JSON instead of chat prose. It is **not registered unless you enable it** — set `STRUCTURED_INFERENCE_ENABLED=true` (or `--structured-enabled`). While disabled the path 404s exactly like any other unknown route.

```bash
curl -s http://127.0.0.1:8088/v1/structured/inference \
  -H 'authorization: Bearer local-open-agent-api' \
  -H 'content-type: application/json' \
  -d '{
    "request_id": "req-1",
    "idempotency_key": "idem-1",
    "operation": "report_summary",
    "model": "gpt-5.6-sol",
    "input": "Revenue rose 12% in Q3, driven by enterprise renewals.",
    "schema_version": "v1",
    "max_output_tokens": 512,
    "deadline_ms": 60000,
    "schema": {
      "type": "object",
      "additionalProperties": false,
      "required": ["headline", "growth_pct"],
      "properties": {
        "headline": {"type": "string"},
        "growth_pct": {"type": "number"}
      }
    }
  }' | jq .
```

Success responses are guaranteed to have been validated against your schema before they are written:

```json
{
  "data": {"headline": "Enterprise renewals drove Q3", "growth_pct": 12},
  "model": "gpt-5.6-sol",
  "upstream_response_id": "resp_…",
  "usage": {"prompt_tokens": 118, "completion_tokens": 24, "total_tokens": 142},
  "latency_ms": 1840,
  "request_id": "req-1",
  "idempotency_key": "idem-1",
  "idempotent_replay": false,
  "operation": "report_summary",
  "schema_version": "v1",
  "model_policy_version": "3f1c…",
  "contract_version": "1.1.0",
  "build": {"…": "…"}
}
```

### Error codes

Branch on `error.code`, never on `error.message`.

| `code` | HTTP | Meaning |
| --- | --- | --- |
| `auth_error` | 401 | Inbound bearer rejected, or upstream auth failed |
| `unsupported_model` | 404 | Model is not on the structured allowlist (or its provider is disabled) |
| `invalid_schema` | 400 | Schema is outside the supported strict subset — see `error.details` |
| `invalid_request` | 400 | Malformed envelope (missing `request_id`, bad `verbosity`, …) |
| `rate_limited` | 429 | Admission control or upstream quota — honour `Retry-After` |
| `unavailable` | 503 | Gateway is draining — honour `Retry-After` |
| `timeout` | 504 | `deadline_ms` elapsed |
| `upstream_error` | 502 | Any other upstream failure |
| `output_validation_failed` | 422 | Model output was not parseable JSON, or did not satisfy your schema |
| `idempotency_conflict` | 409 | `idempotency_key` reused, within its TTL, with different inference parameters |

`output_validation_failed` is deliberately a 4xx: the gateway worked, the model did not comply. Retry with a fresh `idempotency_key`.

`idempotency_conflict` is also deterministic and makes **no upstream call**: the key is already bound to another request. Send the original parameters, or use a fresh key.

Codes are additive. `contract_version` is `1.1.0`; treat an unrecognized `error.code` as its HTTP status class rather than failing to parse.

### Supported schema subset

Schemas are validated at admission, so an unsupported construct is a clean `invalid_schema` rather than silently-unenforced output.

Supported: `type` (`object`, `array`, `string`, `number`, `integer`, `boolean`, `null`), `properties`, `required`, `additionalProperties`, `items`, `minItems`, `maxItems`, `enum`, `description`, `title`.

Rejected: `$ref`, `$defs`, `oneOf`, `anyOf`, `allOf`, `not`, `if`/`then`/`else`, `pattern`, `format`, `patternProperties`, `propertyNames`, `const`, numeric bounds, and union types (`"type": ["string","null"]`). Pre-flatten those before sending.

Two strict-mode rules apply to every object: `additionalProperties` must be present and `false`, and every declared property must appear in `required`.

### Idempotency

A response is replayed (`"idempotent_replay": true`) only when the caller, `operation`, input checksum, `schema_version`, `model_policy_version`, **and** `idempotency_key` all match. Change any of them and the inference re-runs. Concurrent duplicates single-flight — one upstream call, everyone gets the same answer. Failures are never stored, so a retry after an error really retries.

A stored response is also **bound to the request that produced it**. The key carries a fingerprint over `model` (as sent and as resolved), the effective `reasoning_effort` and `verbosity`, the effective `max_output_tokens`, the schema body, `schema_version`, `model_policy_version`, `operation`, the caller, and the input. Reuse the same key with any of those changed and you get `409 idempotency_conflict` — no upstream call, no bill, and never a response produced by another model. `deadline_ms`, `schema_name`, and `request_id` are deliberately **not** part of the fingerprint: they do not change the inference, so a retry may vary them freely. The schema body is canonicalized before hashing, so re-serializing an identical schema (different key order or whitespace) is a replay, not a conflict.

By default the store is **process-local** (`STRUCTURED_IDEMPOTENCY_BACKEND=memory`): replay lives in one pod's memory and is lost on restart. That's fine for the single-replica default.

Single-flight on the memory backend is per **process**, not per replica count, and a rolling update runs the old and new pod at once — so even at `STRUCTURED_REPLICAS=1` a duplicate key can reach two processes during a deploy unless you use the file backend or `maxSurge: 0` / `strategy: Recreate` (see [Kubernetes](./install/kubernetes#structured-inference-across-replicas)). The gateway logs a `structured_idempotency_warning` line at startup whenever structured inference is enabled on the memory backend.

For anything bigger, point every replica at one shared directory:

```bash
STRUCTURED_IDEMPOTENCY_BACKEND=file
STRUCTURED_IDEMPOTENCY_DIR=/var/lib/open-agent-api/structured-idempotency
STRUCTURED_REPLICAS=2
```

Then a stored response replays across pods and survives a restart for the whole `STRUCTURED_IDEMPOTENCY_TTL` — one upstream call, one bill, one body. The gateway **fails closed**: `STRUCTURED_REPLICAS > 1` with the memory backend is refused at startup rather than silently double-calling upstream.

The shared volume must have POSIX `rename`/`link` semantics (a `ReadWriteMany` PVC). Replay after a completed request is exact; concurrent single-flight across pods is best-effort — see [issue-120-validation.md](https://github.com/teslashibe/open-agent-api/blob/main/docs/issue-120-validation.md) for the exact bound.

The gateway **preflights that directory at startup** and refuses to start if it is absent or unwritable: it creates the directory, then writes, `fsync`s, renames, and hard-links a scratch file, which is exactly what storing a record and taking a reservation need. The error names the directory and `STRUCTURED_IDEMPOTENCY_DIR`. Previously a bad mount degraded silently to a process-local store, which on more than one replica means duplicate keys bypass single-flight and get billed twice. `backend=file` is logged only after the preflight passes.

Bumping the durable record format invalidates records written by an older build: the first request per live key after such an upgrade calls upstream once more, bounded by `STRUCTURED_IDEMPOTENCY_TTL`. That is deliberate — a record from before this release carries no fingerprint, so replaying it could not be proven safe.

### Admission and models

Structured traffic has its own queue budget (`STRUCTURED_MAX_ACTIVE`, `STRUCTURED_MAX_ACTIVE_PER_KEY`, `STRUCTURED_QUEUE_LIMIT`, `STRUCTURED_QUEUE_TIMEOUT`) so it can never starve Cursor/agent traffic. The rest of the knobs: `STRUCTURED_MAX_DEADLINE`, `STRUCTURED_MAX_OUTPUT_TOKENS`, `STRUCTURED_IDEMPOTENCY_TTL`, `STRUCTURED_IDEMPOTENCY_BACKEND`, `STRUCTURED_IDEMPOTENCY_DIR`, `STRUCTURED_REPLICAS`, and `STRUCTURED_MODELS` (each also a `--structured-*` flag). Extraction requests carry no tools, skip the captured Codex CLI profile and scaffold, and never prewarm a connection.

The allowlist is Codex-only by default (`gpt-5.6-sol`, `gpt-5.6-terra`, `gpt-5.6-luna` and their effort variants); override it with `STRUCTURED_MODELS`. Gemini and Claude Code aliases return `unsupported_model` because they do not honour strict `json_schema` output.

### Metrics

`codex_chat_api_structured_latency_seconds`, `codex_chat_api_structured_tokens_total`, `codex_chat_api_structured_failures_total`, `codex_chat_api_structured_validation_total` (`result` is `valid`, `invalid`, `unparsable`, or `unknown` — `unknown` is a label the gateway did not recognize, never a real schema failure), `codex_chat_api_structured_idempotency_total` (`result` is `local_hit`, `store_hit`, `miss`, `backend_error`, or `conflict`), and `codex_chat_api_structured_inflight`, plus queue waits under `provider="structured"`.

## Response compression

Ask for it with `Accept-Encoding` and you’ll often get **gzip**, **brotli**, or **deflate** back (Fiber’s compress middleware), with `Content-Encoding` set to match.

```bash
curl -s -H 'Accept-Encoding: gzip' -H 'authorization: Bearer local-open-agent-api' \
  --compressed http://127.0.0.1:8088/v1/models | jq '.data | length'
```

`--compressed` tells curl to decode the body for you.

**Streaming chat completions skip compression.** Agent / SSE traffic (`"stream": true`) stays raw so flushes aren’t held until a whole blob is ready. That’s deliberate for Cursor BYOK.

## See also

- [Install](./install/) — from clone to first completion
- [Cursor Agent tool calling](./cursor/tool-conventions) — tools / `delta.tool_calls`
- [Model catalog](./models/catalog) — public IDs
