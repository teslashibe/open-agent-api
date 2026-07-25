# Report Studio Structured Inference

Contract version: `report-studio.structured-inference.v1`

This endpoint is an additive, non-streaming extraction API for Report Studio.
It does not change `/v1/models` or `/v1/chat/completions`.

## Request

`POST /v1/report-studio/inference` requires the configured gateway bearer
secret and `Content-Type: application/json`.

```json
{
  "contract_version": "report-studio.structured-inference.v1",
  "request_id": "report-run-123",
  "idempotency_key": "report-run-123-section-summary",
  "model": "gpt-5.6-sol",
  "input": {"source": "Quarterly revenue was 42."},
  "schema": {
    "name": "report_summary",
    "version": "report-summary.v1",
    "strict": true,
    "json_schema": {
      "type": "object",
      "properties": {"revenue": {"type": "number"}},
      "required": ["revenue"],
      "additionalProperties": false
    }
  },
  "reasoning": {"effort": "low"},
  "verbosity": "low",
  "max_output_tokens": 1024,
  "deadline_ms": 30000
}
```

Unknown request fields are rejected. The deadline is a duration from request
acceptance and includes authentication, parsing, schema compilation, admission,
and upstream work; a request whose deadline expires during preprocessing never
starts upstream inference.

Schemas use the strict structured-output subset of JSON Schema draft 2020-12.
Schema names must contain 1-64 letters, digits, underscores, or hyphens.
Schemas are size/depth bounded, must have an object root, must require every
declared object property, must set `additionalProperties: false` on every
object, and may use only locally validated structured-output keywords. Contract
v1 allows at most five nesting levels and 100 total object properties. Remote
references and unsupported constructs such as `oneOf`, `allOf`, conditionals,
and string/numeric constraint keywords are rejected before the upstream call.
Enums are limited to 1,000 values across the schema. A single string enum with
more than 250 values may contain at most 15,000 aggregate characters.

Extraction mode always disables tools, the captured Codex coding
profile/scaffold, and prewarm. The model policy is an exact allowlist configured
with `STRUCTURED_INFERENCE_MODELS`; arbitrary upstream model IDs are rejected.
Contract v1 accepts Codex-backed aliases only, because other configured
providers do not preserve the same strict JSON Schema dialect.

## Success

Success contains the schema-validated `data`, actual upstream model, upstream
response ID, token usage, upstream latency, provenance, and a retry-safe
identity. `identity.response_id` is derived from the caller, operation, input
checksum, schema version, canonical schema checksum, model policy version,
model, and idempotency key. `identity.replayed` identifies a cached response.
Cached data is revalidated against the current request schema before replay.

Idempotency is caller-scoped using a one-way hash of the authenticated bearer.
Client-supplied tenant or forwarding headers never select an idempotency
namespace. The default cache is bounded and process-local with in-flight
coalescing. The store API is isolated in `internal/reportstudio` so shared
storage or independently authenticated caller credentials can be wired in a
later deployment issue.

## Errors and Admission

Every error uses the versioned envelope:

```json
{
  "contract_version": "report-studio.structured-inference.v1",
  "error": {
    "code": "validation_error",
    "message": "model output failed schema validation",
    "request_id": "report-run-123",
    "retryable": true
  }
}
```

Machine-readable codes are `authentication_error`, `invalid_request`,
`unsupported_model`, `invalid_schema`, `idempotency_conflict`, `rate_limit`,
`timeout`, `upstream_error`, `validation_error`, and `overloaded`. Admission
has independent active and waiting bounds. Saturation returns 429 or 503 with
`Retry-After`; upstream 429 responses also preserve a bounded retry hint.

## Metrics

The private Prometheus registry includes structured inference latency, prompt /
completion / total token counts, bounded failure and validation counters,
saturation counters, and active/queued gauges. Caller IDs, request IDs,
idempotency keys, inputs, schemas, and checksums are never metric labels.

## Build Provenance

The binary exposes build-injected `source_revision` and `image_version` in each
success response. The Docker image also carries:

- `org.opencontainers.image.revision`
- `org.opencontainers.image.version`
- `io.teslashibe.report-studio.contract`

The Docker workflow supplies the source SHA and ref name as build arguments.
This issue adds build metadata only; it performs no production deployment,
manifest update, traffic routing, or cutover.

## Validation

Run:

```bash
GOCACHE=$PWD/.gocache go test -count=1 ./...
GOCACHE=$PWD/.gocache go test -race -count=1 ./internal/reportstudio ./internal/server ./internal/codex ./internal/metrics
GOCACHE=$PWD/.gocache go vet ./...
GOCACHE=$PWD/.gocache go build ./...
```
