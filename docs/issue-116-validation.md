# Issue 116 Validation

This page records the automated evidence for the backward-compatible
structured-inference contract used by Report Studio
(`POST /v1/structured/inference`).

The endpoint ships **default-off**. `STRUCTURED_INFERENCE_ENABLED` is `false`
unless a deploy sets it, so the public Cursor BYOK surface is unchanged and this
ticket performs no production deployment or traffic cutover.

## Acceptance Criteria

- **AC1:** Authenticated non-streaming request with request/idempotency ID,
  model, input, strict JSON Schema, reasoning/verbosity, output limit, and
  deadline.
- **AC2:** Schema-valid success containing data, actual model, upstream response
  ID, usage, latency, and retry-safe identity.
- **AC3:** Distinct machine-readable auth, unsupported-model, invalid-schema,
  rate-limit, timeout, upstream, and validation errors.
- **AC4:** Extraction mode disables tools, coding scaffolding/profile overhead,
  and prewarm.
- **AC5:** Bounded admission; overload returns 429/503 with `Retry-After`.
- **AC6:** Idempotency scoped by caller, operation, input checksum, schema
  version, and model-policy version.
- **AC7:** Metrics for latency, usage, failures, validation, and saturation.
- **AC8:** Existing endpoints and authorized consumers remain behaviorally
  compatible.
- **AC9:** The image/source build provenance and contract version are
  documented and served by the binary.
- **AC10:** Unit/integration tests for all success and failure contracts.
- **AC11:** Compatibility tests for existing chat/inference endpoints.
- **AC12:** Concurrency tests at 1/2/4/8 and malformed-output tests.
- **AC13:** No production deployment or traffic cutover in this issue.

## Automated Evidence

Run from the repository root:

```bash
GOCACHE=$PWD/.gocache go build ./...
GOCACHE=$PWD/.gocache go vet ./...
GOCACHE=$PWD/.gocache go test -count=1 ./...
GOCACHE=$PWD/.gocache go test -race -count=1 ./internal/structured ./internal/server ./internal/codex ./internal/metrics
```

Result recorded on 2026-07-26: all packages passed the full and race test
suites, and `go vet` reported no findings.

| Criterion | Tests |
| --- | --- |
| AC1 | `TestStructuredInferenceReturnsSchemaValidSuccessEnvelope`, `TestStructuredInferenceClampsOutputTokensAndDeadline`, `TestRequestValidateRejectsMalformedEnvelopes`, `TestCompileSchemaAcceptsStrictSubset` |
| AC2 | `TestStructuredInferenceReturnsSchemaValidSuccessEnvelope`, `TestValidateAcceptsConformingOutput` |
| AC3 | `TestStructuredInferenceErrorContracts`, `TestStructuredInferenceAuthErrorUsesContractCode`, `TestStructuredInferenceDeadlineReturnsTimeout`, `TestStructuredInferenceRejectsMalformedJSONBody`, `TestPolicyRejectsUnknownAndDisabledModels`, `TestCompileSchemaRejectsUnsupportedConstructs` |
| AC4 | `TestStructuredInferenceUsesExtractionMode`, `TestBuildMinimalEmitsStrictJSONSchemaForExtraction`, `TestExtractionSkipsPrewarmAndTheFaithfulProfile`, `TestBuildMinimalNonExtractionIsUnchanged` |
| AC5 | `TestStructuredInferenceQueueFullReturns429WithRetryAfter`, `TestStructuredInferenceDrainingReturns503WithRetryAfter`, `TestStructuredInferenceUpstreamRateLimitCarriesRetryAfter` |
| AC6 | `TestStructuredInferenceReplaysIdempotentRequests`, `TestStructuredInferenceIdempotencyIsScoped`, `TestStructuredInferenceModelPolicyVersionScopesIdempotency`, `TestKeyIsScopedByEveryDimension`, `TestKeyIsNotVulnerableToComponentSmuggling`, `TestStoreDoesNotCacheFailures`, `TestStoreExpiresEntries`, `TestStoreIsBounded`, `TestPolicyVersionIsStableAndChangesWithTheAllowlist` |
| AC7 | `TestStructuredInferenceRecordsMetrics`, `TestStructuredMetricsExposeBoundedSurface`, `TestStructuredMetricsBoundModelAndCodeCardinality`, `TestDisabledStructuredMetricsAreNoop` |
| AC8 | `TestExistingEndpointsAreUnchangedByStructuredInference`, `TestStructuredInferenceDisabledByDefault`, `TestStructuredInferenceDefaultsAreDark`, the full pre-existing `internal/server` suite |
| AC9 | `TestHealth`, `TestStructuredInferenceReturnsSchemaValidSuccessEnvelope` (asserts `build` + `contract_version`) |
| AC10 | The whole `internal/structured` and `internal/server/structured_test.go` suites |
| AC11 | `TestExistingEndpointsAreUnchangedByStructuredInference` |
| AC12 | `TestStructuredInferenceConcurrency` (`distinct/1,2,4,8` and `duplicate/1,2,4,8`), `TestExtractJSONRejectsMalformedOutput`, `TestValidateRejectsNonConformingOutput`, the malformed-output cases in `TestStructuredInferenceErrorContracts` |
| AC13 | `TestStructuredInferenceDefaultsAreDark`, `TestStructuredInferenceDisabledByDefault` |

## Contract version and build provenance (AC9)

`ContractVersion` lives in `internal/structured/contract.go` and is currently
`1.0.0`. Bump the minor for additive fields and the major for anything a
consumer can break on.

Every structured success and error response carries `contract_version` plus a
`build` object, and `GET /health` and `GET /health/live` serve the same two
fields additively next to the unchanged `"status":"ok"`:

```json
{
  "status": "ok",
  "contract_version": "1.0.0",
  "build": {
    "version": "v0.1.0",
    "commit": "6fba3e4…",
    "build_date": "2026-07-26T20:04:11Z",
    "go_version": "go1.24.0",
    "modified": false
  }
}
```

`internal/buildinfo` prefers `-ldflags` values stamped by the Dockerfile
(`BUILD_VERSION`, `BUILD_COMMIT`, `BUILD_DATE` build args, wired through
`docker-compose.yml`), falls back to the Go toolchain's `vcs.revision` /
`vcs.time` stamps, and finally to `devel`/`unknown`.

## Documented limitations

These are deliberate and must be read before any GitOps rollout:

- **Idempotency is process-local — resolved in issue 120.** As shipped in this
  issue the store lived only in each pod's memory, so two pods could both call
  upstream for one idempotency key. Issue 120 adds the durable file backend
  (`STRUCTURED_IDEMPOTENCY_BACKEND=file` plus a shared
  `STRUCTURED_IDEMPOTENCY_DIR`), which makes replay survive restarts and work
  across replicas, and a fail-closed guard: `STRUCTURED_REPLICAS > 1` with the
  memory backend is now rejected at startup. See
  [issue-120-validation.md](./issue-120-validation.md). The default is still
  `memory` with one replica, which is exactly the behavior recorded here.
- **The JSON Schema subset is narrow by design.** `$ref`, `$defs`, `oneOf`,
  `anyOf`, `allOf`, `not`, `if`/`then`/`else`, `pattern`, `format`,
  `patternProperties`, `propertyNames`, `const`, numeric bounds, and union
  types are rejected with `invalid_schema` at admission. Objects must set
  `additionalProperties: false` and list every property in `required`. That is
  the safe failure direction — a rejected schema is a clear 400, not silently
  unenforced output.
- **The model allowlist is Codex-only.** Gemini and Claude Code aliases do not
  honour `text.format` json_schema, so they resolve to `unsupported_model`
  rather than producing unpredictable `output_validation_failed`s.
- **Non-conforming model output is a 422, not a 5xx.** The gateway worked; the
  model did not comply. Callers should retry with a fresh idempotency key.
