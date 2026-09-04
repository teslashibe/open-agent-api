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
| `GET` | `/v1/accounts/usage` | Codex account rate-limit windows and banked reset counts |
| `POST` | `/v1/accounts/:label/reset-credits/redeem` | Redeem a banked Codex reset |
| `POST` | `/v1/chat/completions` | Streaming and non-streaming chat |
| `POST` | `/v1/structured/inference` | Structured JSON extraction — **off by default** |

If you set `GATEWAY_BEARER_SECRET`, `/v1/*` needs `Authorization: Bearer …`. Health endpoints stay open for probes. Locally the secret is usually unset, so any non-empty bearer is fine.

The account endpoints are Codex-only and always sit behind the `/v1` bearer
gate. Redemption is an explicit mutation: send `confirm: true` and a
caller-generated UUID as `redeem_request_id`. Reusing that UUID makes retries
safe at the upstream redemption boundary.

```bash
curl -H "Authorization: Bearer $GATEWAY_BEARER_SECRET" \
  http://127.0.0.1:8088/v1/accounts/usage

curl -X POST \
  -H "Authorization: Bearer $GATEWAY_BEARER_SECRET" \
  -H "Content-Type: application/json" \
  -d '{"confirm":true,"redeem_request_id":"16fd2706-8baf-433b-82eb-8c7fada847da"}' \
  http://127.0.0.1:8088/v1/accounts/default/reset-credits/redeem
```

## Build provenance

`GET /health` and `GET /health/live` return the contract version and the provenance of the running binary alongside the usual `"status":"ok"`:

```json
{
  "status": "ok",
  "contract_version": "2.0.0",
  "build": {"version": "v0.1.0", "commit": "6fba3e4c8f1d09a2b5e3771c40a9d8e2f6b10c93", "build_date": "2026-07-26T20:04:11Z", "go_version": "go1.24.0", "modified": false}
}
```

`version` / `commit` / `build_date` are stamped at image build time from the `BUILD_VERSION`, `BUILD_COMMIT`, and `BUILD_DATE` build args.

**Images published by CI always carry real values.** `.github/workflows/docker.yml` passes the resolved tag, `github.sha`, and a UTC `BUILD_DATE` (`YYYY-MM-DDTHH:MM:SSZ`) into the build, and a `verify-provenance` job boots the pushed image by digest and fails the run unless `/health` reports that commit — so `"commit": "unknown"` or `"version": "devel"` on a ghcr image cannot ship. `commit` is the full 40-character SHA of the source the image was built from.

Local `docker compose` builds pass the same three args through from your environment (unset by default); with them unset the gateway falls back to the Go toolchain's VCS stamps, and finally to `"devel"` / `"unknown"`.

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
  "contract_version": "2.0.0",
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

Codes are additive. `contract_version` is `2.0.0`; treat an unrecognized `error.code` as its HTTP status class rather than failing to parse.

`2.0.0` removes the request field `max_output_tokens`. Codex Responses honours no output-token cap, so the field promised a cost ceiling it could never enforce; a body that still carries it is ignored, not rejected. Bound cost with `deadline_ms`, the model choice, and `STRUCTURED_MAX_ACTIVE` instead. No field you send is newly rejected, so a pre-`2.0.0` request body still works — the endpoint also ships disabled by default, so nothing in production depended on it.

### Supported schema subset

Schemas are validated at admission, so an unsupported construct is a clean `invalid_schema` rather than silently-unenforced output.

Supported: `type` (`object`, `array`, `string`, `number`, `integer`, `boolean`, `null`), `properties`, `required`, `additionalProperties`, `items`, `minItems`, `maxItems`, `enum`, `description`, `title`.

Rejected: `$ref`, `$defs`, `oneOf`, `anyOf`, `allOf`, `not`, `if`/`then`/`else`, `pattern`, `format`, `patternProperties`, `propertyNames`, `const`, numeric bounds, and union types (`"type": ["string","null"]`). Pre-flatten those before sending.

Two strict-mode rules apply to every object: `additionalProperties` must be present and `false`, and every declared property must appear in `required`.

### Limits

Every bound below is checked at admission, before any upstream call.

| Field | Limit | Rejection |
| --- | --- | --- |
| `schema` | 256 KiB | `invalid_schema` 400 — `"schema exceeds the maximum size"` |
| `input` | 1 MiB | `invalid_request` 400 |
| `request_id` | 128 characters | `invalid_request` 400 |
| `operation` | 128 characters | `invalid_request` 400 |
| `idempotency_key` | 200 characters | `invalid_request` 400 |
| `schema_version` | 64 characters | `invalid_request` 400 |

The `schema` bound is checked **before the document is parsed**, so an over-cap schema costs a length comparison rather than a JSON decode and a compile. 256 KiB is far above any real strict-subset schema; the `input` cap is unchanged and independent.

Caller-controlled identifiers are **echoed verbatim in the response** — `request_id` comes back exactly as you sent it — but are **sanitized in log records**: line breaks, tabs, and control characters collapse to a space and the value is bounded at 128 characters. A log record is always exactly one line per real event, so an embedded newline cannot forge a second `structured_success` (or any other) audit line. If you send an identifier containing whitespace or control characters, correlating your logs with the gateway's is by prefix rather than by exact match.

### Idempotency

A response is replayed (`"idempotent_replay": true`) only when the caller, `operation`, input checksum, `schema_version`, `model_policy_version`, **and** `idempotency_key` all match. Change any of them and the inference re-runs. Concurrent duplicates single-flight — one upstream call, everyone gets the same answer. Failures are never stored, so a retry after an error really retries.

A stored response is also **bound to the request that produced it**. The key carries a fingerprint over `model` (as sent and as resolved), the effective `reasoning_effort` and `verbosity`, the schema body, `schema_version`, `model_policy_version`, `operation`, the caller, and the input. Reuse the same key with any of those changed and you get `409 idempotency_conflict` — no upstream call, no bill, and never a response produced by another model. `deadline_ms`, `schema_name`, and `request_id` are deliberately **not** part of the fingerprint: they do not change the inference, so a retry may vary them freely. The schema body is canonicalized before hashing, so re-serializing an identical schema (different key order or whitespace) is a replay, not a conflict.

The store is memory-only and process-local: replay lives in the gateway process and is lost on restart. `STRUCTURED_REPLICAS` must be exactly `1`; startup rejects any other value. Kubernetes must also use `maxSurge: 0` or `strategy: Recreate`, because a nominally single-replica rolling update can run the old and new process simultaneously.

A completed response is stored only when upstream succeeds; failures remain retryable. The bounded store removes expired records opportunistically. It never evicts an unexpired or in-flight binding: if all capacity is live, a new unique key gets `503 unavailable` with `Retry-After`, while existing keys continue to replay or join their in-flight request.

:::warning Release condition

Enable structured inference only on one replica with `maxSurge: 0` (or `strategy: Recreate`) so no two processes ever serve the same key at once.

:::

### Admission and models

Structured traffic has its own queue budget (`STRUCTURED_MAX_ACTIVE`, `STRUCTURED_MAX_ACTIVE_PER_KEY`, `STRUCTURED_QUEUE_LIMIT`, `STRUCTURED_QUEUE_TIMEOUT`) so it can never starve Cursor/agent traffic. The rest of the knobs: `STRUCTURED_MAX_DEADLINE`, `STRUCTURED_IDEMPOTENCY_TTL`, `STRUCTURED_REPLICAS`, and `STRUCTURED_MODELS` (each also a `--structured-*` flag). Extraction requests carry no tools, skip the captured Codex CLI profile and scaffold, and never prewarm a connection.

The allowlist is Codex-only by default (`gpt-5.6-sol`, `gpt-5.6-terra`, `gpt-5.6-luna` and their effort variants); override it with `STRUCTURED_MODELS`. Gemini and Claude Code aliases return `unsupported_model` because they do not honour strict `json_schema` output.

### Metrics

`codex_chat_api_structured_latency_seconds`, `codex_chat_api_structured_tokens_total`, `codex_chat_api_structured_failures_total`, `codex_chat_api_structured_validation_total` (`result` is `valid`, `invalid`, `unparsable`, or `unknown` — `unknown` is a label the gateway did not recognize, never a real schema failure), `codex_chat_api_structured_idempotency_total` (`result` is `local_hit`, `miss`, `conflict`, or `capacity`), and `codex_chat_api_structured_inflight`, plus queue waits under `provider="structured"`.

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
