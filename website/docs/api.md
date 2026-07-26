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
  "contract_version": "1.0.0",
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
  "contract_version": "1.0.0",
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

`output_validation_failed` is deliberately a 4xx: the gateway worked, the model did not comply. Retry with a fresh `idempotency_key`.

### Supported schema subset

Schemas are validated at admission, so an unsupported construct is a clean `invalid_schema` rather than silently-unenforced output.

Supported: `type` (`object`, `array`, `string`, `number`, `integer`, `boolean`, `null`), `properties`, `required`, `additionalProperties`, `items`, `minItems`, `maxItems`, `enum`, `description`, `title`.

Rejected: `$ref`, `$defs`, `oneOf`, `anyOf`, `allOf`, `not`, `if`/`then`/`else`, `pattern`, `format`, `patternProperties`, `propertyNames`, `const`, numeric bounds, and union types (`"type": ["string","null"]`). Pre-flatten those before sending.

Two strict-mode rules apply to every object: `additionalProperties` must be present and `false`, and every declared property must appear in `required`.

### Idempotency

A response is replayed (`"idempotent_replay": true`) only when the caller, `operation`, input checksum, `schema_version`, `model_policy_version`, **and** `idempotency_key` all match. Change any of them and the inference re-runs. Concurrent duplicates single-flight — one upstream call, everyone gets the same answer. Failures are never stored, so a retry after an error really retries.

The store is **process-local**: a multi-replica deployment replays per pod, not cluster-wide.

### Admission and models

Structured traffic has its own queue budget (`STRUCTURED_MAX_ACTIVE`, `STRUCTURED_MAX_ACTIVE_PER_KEY`, `STRUCTURED_QUEUE_LIMIT`, `STRUCTURED_QUEUE_TIMEOUT`) so it can never starve Cursor/agent traffic. Extraction requests carry no tools, skip the captured Codex CLI profile and scaffold, and never prewarm a connection.

The allowlist is Codex-only by default (`gpt-5.6-sol`, `gpt-5.6-terra`, `gpt-5.6-luna` and their effort variants); override it with `STRUCTURED_MODELS`. Gemini and Claude Code aliases return `unsupported_model` because they do not honour strict `json_schema` output.

### Metrics

`codex_chat_api_structured_latency_seconds`, `codex_chat_api_structured_tokens_total`, `codex_chat_api_structured_failures_total`, `codex_chat_api_structured_validation_total`, and `codex_chat_api_structured_inflight`, plus queue waits under `provider="structured"`.

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
