# Issue 130 Validation

Adversarial final-validation follow-up from [issue-128](./issue-128-validation.md).
Two things were true of the structured surface before this change and are not now:

1. **A caller could write its own audit records.** `request_id` and `operation`
   are caller-controlled, are validated only for emptiness and length, and were
   formatted straight into every structured log line. A `request_id` of
   `"req-1\nstructured_success request_id=forged operation=forged model=gpt-5.6-sol replay=false latency_ms=0"`
   produced a second line that grep, `wc -l`, and any line-oriented log pipeline
   read as a real `structured_success`. The same value also reached
   `agent_queue_*` lines, the distributed lock file, and the codex pool's own
   log lines, so one request could mint several forged records.
2. **The `schema` document had no bound of its own.** It was capped only by
   whatever the HTTP body limit happened to be, and it was decoded and compiled
   before anything looked at its size.

## What the fix is

One sanitizer, applied at every place a caller-controlled string can become part
of a log record:

```go
func sanitizeLogValue(value string, maxRunes int) string   // internal/server/structured.go
func sanitizeLogField(value string) string                 // = sanitizeLogValue(value, 128)
```

It maps `\n \r \t \v \f`, `U+0085`, `U+2028`, `U+2029`, and every other
`unicode.IsControl` rune to a space, trims, and truncates at the bound with the
existing `…` marker. `sanitizeProviderMessage` is now a one-line call into it, so
there is exactly one implementation rather than two that can drift.

The handler sanitizes **once**, immediately after `Validate()`, and then uses only
the sanitized values:

| Sink | Before | After |
| --- | --- | --- |
| `structured_shed`, `structured_admit`, `structured_conflict`, `structured_error`, `structured_success` | `req.RequestID`, `req.Operation` | `logRequestID`, `logOperation` |
| `structured_upstream_error` | took the whole `structured.Request` | takes `requestID, operation string` — the raw values are no longer reachable from the function |
| `agentQueue.acquire` → `agent_queue_acquire\|_full\|_wait\|_timeout\|_release\|_lock_*` and the lock file body | `req.RequestID` | `logRequestID` |
| `codex.Request.RequestID` → `internal/codex/pool.go` log lines | `req.RequestID` | `logRequestID` |

The response envelope still carries `req.RequestID` **verbatim**. It is
JSON-encoded there, so a newline is `\n` in the payload and forges nothing; a
client that correlates on the id it sent keeps working.

The schema bound is `structured.MaxSchemaBytes = 256 << 10`, checked at the very
top of `CompileSchema` **before** `json.Decoder.Decode`. Rejecting pre-decode is
the point: an over-cap schema costs a length comparison, not a parse and a
compile. `maxInputBytes` (1 MiB) and the Fiber body limit are untouched.

## Why a record is a line, not a substring

The tests assert on `strings.HasPrefix(line, …)` and on per-record-type line
counts, not on `strings.Contains(logs, …)`. That is deliberate: after the fix the
forged text still appears — collapsed to one line — as inert data inside the
`request_id=` field, exactly as an oversized-but-harmless value would. What must
not happen is that it *starts a line*, because a line is what grep, `wc -l`, and
log ingestion count as a record. `TestStructuredInferenceCannotForgeLogRecords`
therefore runs the same request twice — once with clean identifiers, once
poisoned — and requires the two runs to produce an identical map of leading-token
→ count and an identical line count.

## Criterion → proof

| AC | Proof |
| --- | --- |
| 1 — one shared sanitizer that collapses CR/LF/tab/vertical whitespace, trims, and bounds length | `internal/server/structured.go`: `sanitizeLogValue(value, maxRunes)` + `maxLogFieldRunes = 128` + `sanitizeLogField`. `sanitizeProviderMessage` now delegates to it (`sanitizeLogValue(message, maxProviderMessageRunes)`), so there is one implementation. `TestSanitizeLogFieldCollapsesInjection` covers `\n`, `\r\n`, `\t`, `\v`, `\f`, `U+0085`, `U+2028`, `U+2029`, `NUL`, `ESC`, surrounding whitespace, and both sides of the 128-rune bound. `TestSanitizeProviderMessage` is unchanged and still passes, proving the provider-message behaviour did not drift. |
| 2 — sanitize `request_id` and `operation` at every structured inference log call site | `logRequestID`/`logOperation` computed once after `Validate()` and used in all five `logLine` calls plus `logStructuredUpstreamError`, whose signature changed from `(opts, req structured.Request, …)` to `(opts, requestID, operation string, …)` so the sink cannot be reintroduced. The two secondary sinks that carry the same strings are closed too: `structuredQueue.acquire(ctx, logRequestID, …)` and `codex.Request{RequestID: logRequestID}` (`runStructuredInference` takes it as a parameter). |
| 3 — regression tests proving embedded newlines cannot forge `structured_success` or any second audit record | `TestStructuredInferenceCannotForgeLogRecords` (success path and upstream-failure path): identical record-type counts and line counts vs the clean run, exactly 1 `structured_success` on success and 0 on failure, and no line starting with the forged record, a forged `structured_admit`, or a forged `agent_queue_acquire`. `TestStructuredInferenceQueueRecordsAreSingleLine` covers the shed path, where the queue writes the lines. `TestStructuredInferenceEchoesRawRequestIDVerbatim` keeps the response echo byte-exact. **Negative control:** with `sanitizeLogField` removed from the two assignments, all three tests fail; restored, all three pass. |
| 4 — explicit schema-size bound ≤ 256 KiB, documented, with boundary tests, request body cap preserved | `structured.MaxSchemaBytes = 256 << 10`, checked pre-decode in `CompileSchema`. `TestCompileSchemaBoundsSchemaSize`: at-cap compiles, cap+1 is `invalid_schema` with the size message, an over-cap *syntactically broken* document also reports the size message (proving it was never parsed), and the constant is asserted ≤ 256 KiB and below `maxInputBytes`. `TestStructuredInferenceRejectsOversizedSchema`: HTTP 400 + `invalid_schema` with **zero** upstream calls, an at-cap schema still returns 200, and a 1 MiB + 1 `input` is still `invalid_request` — the body/input caps are unchanged. Documented in `website/docs/api.md` → "Limits". |
| 5 — build, vet, formatting, full tests, race tests, CI | See **Commands run**. These are the exact steps in `.github/workflows/go-checks.yml`, which gates both `ci.yml` and the image build in `docker.yml`. |

## Commands run

| Command | Result |
| --- | --- |
| `gofmt -l $(go list -f '{{.Dir}}' ./...)` | pass (empty) |
| `go build ./...` | pass |
| `go vet ./...` | pass |
| `go test ./...` | pass |
| `go test -race ./...` | pass |
| `go test -count=1 -race ./internal/server ./internal/structured -run 'Structured\|Sanitize\|Schema'` | pass |
| negative control: `logRequestID := req.RequestID` (sanitizer removed) | `TestStructuredInferenceCannotForgeLogRecords`, `TestStructuredInferenceQueueRecordsAreSingleLine`, and `TestStructuredInferenceEchoesRawRequestIDVerbatim` all fail — the tests test the fix |

## Residual limits

- **Logged identifiers can now differ from the ones the client sent.** A
  `request_id` containing whitespace or control characters is space-collapsed in
  logs, and one over 128 runes is truncated with `…`, while the response body
  still echoes it verbatim. Log ↔ body correlation for such an id is by prefix,
  not by exact match, and two distinct long identifiers can collapse to the same
  logged value. Metrics are unaffected — they never carried the id.
- **This covers the structured surface only.** The OpenAI-compatible handlers and
  `requestLogger` in `internal/server/server.go` still log caller-influenced
  values without this sanitizer. Widening it is a separate change; it is stated
  here rather than done silently.
- **The 256 KiB schema bound is a new 400 for previously accepted payloads.**
  `ContractVersion` stays `2.0.0`: the endpoint ships dark behind
  `STRUCTURED_INFERENCE_ENABLED` and no released client sends a schema anywhere
  near this size, so nothing in production can break on it. By the strict reading
  of `internal/structured/contract.go` ("major for anything a consumer could
  break on") this is a judgment call, and it is flagged as one. It is documented
  as a limit in `website/docs/api.md`.
- **The bound is on the encoded byte length, not on schema complexity.** A 200 KiB
  schema that is deeply nested is still accepted and still compiled; the cap
  bounds the work, it does not eliminate it. Depth and property-count limits
  would be a separate bound.
- **Sanitization is not encoding.** Values are collapsed, not quoted, so a value
  containing ` key=value ` still parses as extra fields to a naive `key=value`
  splitter. What it can no longer do is create a record. A logfmt/JSON encoder
  for the whole log surface is the fuller fix and is out of scope here.
- **`go test -race` on a 2-core CI runner remains the known flake risk** noted in
  [issue-128-validation.md](./issue-128-validation.md). Nothing added here is
  concurrent — `TestStructuredInferenceQueueRecordsAreSingleLine` reuses the
  existing shed-path pattern from `TestStructuredInferenceQueueFullReturns429WithRetryAfter`.
- **Everything in issues 120–128 still holds** — see
  [issue-128-validation.md](./issue-128-validation.md).
