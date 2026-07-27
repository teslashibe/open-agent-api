# Issue 126 Validation

Final-validation follow-up from [issue-124-validation.md](./issue-124-validation.md)
on `feature/rs-gpt-structured-inference`. Two things could mislead before this
change and cannot now:

1. **A cost ceiling that was never enforced.** The structured request contract
   published `max_output_tokens`, the gateway clamped it to
   `STRUCTURED_MAX_OUTPUT_TOKENS`, and the builder put it on the extraction
   payload. Codex Responses supports no output-token cap, so the field promised
   a bill bound the upstream would never honour.
2. **A gate that could pass without ever touching the real upstream.** Every
   structured test drove a fake `codex.Service`. The endpoint could be broken
   against live Codex and the whole suite would still be green.

Defaults do not move. `STRUCTURED_INFERENCE_ENABLED=false`,
`STRUCTURED_IDEMPOTENCY_BACKEND=memory`, `STRUCTURED_REPLICAS=1`: a gateway that
starts today still starts. The one configuration that becomes invalid is
`--structured-max-output-tokens`, which the flag package now rejects loudly; the
`STRUCTURED_MAX_OUTPUT_TOKENS` environment variable is simply ignored, so a
stale deploy manifest does not block a rollout.

## Contract decision

Codex Responses has no output-token cap. Three options were on the table:

| Option | Why not |
| --- | --- |
| Keep sending the field | The upstream ignores it. The gateway would keep publishing a cost ceiling that does not exist. |
| Accept and ignore it | A useless public field that every consumer has to reason about, and every future reader has to re-derive as dead. |
| Reject it with `invalid_request` | Retains the field in the public vocabulary purely to say no, and breaks a caller that sends a harmless value. |

**Chosen: remove it.** The endpoint ships dark (`StructuredEnabled` defaults
false), so no released client is affected. A body that still carries
`max_output_tokens` is dropped by the JSON decoder — ignored, not rejected —
which is stated in the docs. Strict unknown-field rejection is a separate,
larger change that would apply to the whole envelope.

`ContractVersion` goes `1.1.0` → **`2.0.0`**. The file's own rule is "major for
anything a consumer could break on", and a documented public request field is
disappearing. Nothing a client *sends* is newly rejected, so a minor bump was
defensible; major was chosen because the published field list changed and the
dark endpoint makes the bump free.

Bound structured cost with `deadline_ms`, the model choice, and the structured
queue budget (`STRUCTURED_MAX_ACTIVE`, `STRUCTURED_MAX_ACTIVE_PER_KEY`) instead.

## What the record-version bump costs

`Fingerprint` no longer hashes the output limit, so **every version 2 record's
fingerprint is unreproducible by this build**. Without a version bump, the
identical retry of a live pre-upgrade key would compute a different fingerprint
and get `409 idempotency_conflict` — wedging that key for the whole
`STRUCTURED_IDEMPOTENCY_TTL` (default `10m`) with no way for the caller to
recover except a new key.

`IdempotencyRecordVersion` therefore moves **2 → 3**.
`decodeIdempotencyRecord` already treats any non-current version as a clean
miss and unlinks the record, so the cost is bounded at **one extra upstream call
per live key**, once, inside the TTL — exactly the trade already recorded for
the 1 → 2 bump in issue 124. Proven by
`TestFileBackendTreatsVersionTwoRecordsAsAMiss`: the v2 record is a miss, the
key republishes at v3, and the next retry replays (total upstream calls: 1).

## Redaction-safe provider failure logging

A structured client error stays deliberately generic (`upstream_error` /
`auth_error` / `rate_limited` with a fixed message), which leaves an operator
with nothing to diagnose. `structuredInference` now emits, alongside the
existing `structured_error` line:

```
structured_upstream_error request_id=… operation=… model=… provider=codex provider_kind=upstream provider_status=502 provider_message="codex stream failed"
```

Redaction discipline:

- Only `Kind`, `Status`, and `Message` from `*codex.Error` are logged. Every
  `codex.NewError` call site in `internal/codex/{client,events,builder}.go`
  passes a fixed in-repo string, never an upstream body or caller data.
- `err.Error()` is **never** logged: the wrapped chain can carry upstream detail.
- `sanitizeProviderMessage` collapses newlines/tabs and truncates at 200 runes,
  so a future non-constant message can neither forge a second log record nor
  flood the log.
- The caller appears only as `request_id` (the caller's own trace identity) and,
  on the admit line, as a hash.

## Live gate

```bash
STRUCTURED_LIVE_REQUIRED=1 go test -count=1 ./internal/server -run TestStructuredInferenceLiveUpstream -v
```

`TestStructuredInferenceLiveUpstream` sends the **verbatim copy-paste body from
`website/docs/api.md`** through `server.New` backed by a real `codex.NewClient`
against `wss://chatgpt.com/backend-api/codex/responses`, and asserts HTTP 200,
`data` that compiles-and-validates against the request schema,
`idempotent_replay == false`, a non-empty `upstream_response_id`,
`usage.total_tokens > 0`, `contract_version == 2.0.0`, a `structured_success`
log line, and that the logs carry none of the input, schema, output, or bearer
material.

Gate semantics:

- No credential (`~/.codex/auth.json`, or `CODEX_HOME` / `CODEX_AUTH_PATH`) →
  `t.Skip` with the reason, **unless** `STRUCTURED_LIVE_REQUIRED=1`, which turns
  the same condition into a failure. A QA gate sets it, so "never ran" cannot be
  mistaken for "passed".
- With a credential present the test **never skips**, so "credentials available
  but no successful structured response" is a hard failure.
- A `422 output_validation_failed` is reported with an explicit hint that the
  gateway worked and the model did not comply — a legitimate gate failure, not a
  gateway bug.
- `codex.NewClient` eagerly reads `codex_profile.json` / `codex_scaffold.json`,
  whose config defaults are repo-relative; the test resolves both from its own
  `runtime.Caller` path so it cannot fail for the wrong reason.

## Criterion → proof

| AC | Proof |
| --- | --- |
| 1 — payloads never send `max_output_tokens`; exact key set pinned | `internal/codex/extraction_test.go`: `assertPayloadKeys` pins the extraction payload to exactly `{type, model, instructions, input, stream, store, reasoning, text, prompt_cache_key}`; `TestBuildMinimalNonExtractionIsUnchanged` keeps the absence guard on the chat path. `internal/server/structured_test.go:TestStructuredInferenceSendsNoOutputCapAndIgnoresTheRemovedField` proves the `codex.Request` carries no cap. |
| 2 — field, knob, clamping, and fingerprint component removed | `structured.Request.MaxOutputTokens`, its validation branch, `Fingerprint.MaxOutputTokens`, `structuredOutputTokens`, `codex.Request.MaxOutputTokens`, the builder branch, `DefaultStructuredMaxOutputTokens`, the `Config` field, the env binding, the flag, the `Defaults()` entry, and the validation bound are all gone. `internal/config/config_test.go:TestLoadNoLongerRecognizesTheStructuredOutputTokenKnob` asserts the env var is ignored, the flag is rejected, and the `Config` field does not come back (via reflection, since a compile-time reference is impossible). |
| 3 — live-gated end-to-end 200 with schema-valid data | `internal/server/structured_live_test.go:TestStructuredInferenceLiveUpstream`, gated by `STRUCTURED_LIVE_REQUIRED`; see **Live gate** above. |
| 4 — redaction-safe provider logging, generic client body | `internal/server/structured_test.go:TestStructuredInferenceLogsRedactionSafeProviderFailure` uses distinctive sentinels for input, schema property, model output, and bearer token; asserts `provider_kind=upstream provider_status=502` in the logs, a generic `upstream_error` body, and that no sentinel appears. `TestSanitizeProviderMessage` covers newline collapsing and truncation. |
| 5 — `website/docs/api.md` copy-paste request succeeds | The curl body no longer carries `max_output_tokens`; the live test sends that exact body against real Codex. The fingerprint paragraph, knob list, and both `contract_version` occurrences are updated, plus a note that the removed field is ignored. |
| 6 — memory-backend release condition recorded | Release-condition admonitions in `website/docs/api.md` (Idempotency) and `website/docs/install/kubernetes.md` (Rolling updates): memory backend requires `maxSurge: 0` / `strategy: Recreate`, otherwise the file backend on `ReadWriteMany` storage. Do not enable structured inference in any other shape. |
| 7 — build, vet, tests, race, live | See **Commands run** below. |

## Release condition

> Do not enable structured inference on the memory backend unless the deployment
> sets `maxSurge: 0` (or `strategy: Recreate`). Otherwise use
> `STRUCTURED_IDEMPOTENCY_BACKEND=file` on `ReadWriteMany` storage. There is no
> third safe shape.

The startup guard reads `STRUCTURED_REPLICAS`, a *declared* count. Under a
default rolling update (`maxSurge: 25%`) the old and new pod run at once even at
`STRUCTURED_REPLICAS=1`, their memory stores are independent, and a duplicate
key landing on the new pod is billed twice with the declared count still `1`.
The gateway logs `structured_idempotency_warning` at startup on the memory
backend; treat it as a deployment requirement, not noise.

## Commands run

| Command | Result |
| --- | --- |
| `go build ./...` | pass |
| `go vet ./...` | pass |
| `go test ./...` | pass (all packages) |
| `go test -race ./...` | pass (all packages) |
| `STRUCTURED_LIVE_REQUIRED=1 go test -count=1 ./internal/server -run TestStructuredInferenceLiveUpstream -v` | pass — HTTP 200, schema-valid `data`, real `upstream_response_id` and token usage against live Codex |

The gate was also exercised in both negative directions: with
`CODEX_AUTH_PATH` pointing at a nonexistent file the test **skips** with the
reason, and the same run under `STRUCTURED_LIVE_REQUIRED=1` **fails**.

## Residual limits

- **The removed field is ignored, not rejected.** A caller still sending
  `max_output_tokens` gets no error. That is the recorded contract decision;
  strict unknown-field rejection for the whole envelope is a separate change.
- **The live test spends real quota** and depends on `~/.codex/auth.json`,
  network reachability of `wss://chatgpt.com/…`, and a model that complies with
  strict `json_schema`. Non-compliance surfaces as `422
  output_validation_failed` with a message saying which side failed.
- **`ContractVersion` is a major bump.** Any client switching exhaustively on
  the version string breaks. The endpoint ships dark, so this is free today and
  will not be later.
- **Provider messages are trusted to be constants today.** The sanitizer is the
  guard for the day one stops being one; it bounds shape and length, not
  semantics. A future call site that interpolates upstream text into `Message`
  would still be a leak, and code review is the control for that.
- **Everything in issues 120, 122, and 124 still holds:** a shared filesystem is
  not a transactional store, `rename`/`link` semantics vary on network
  filesystems, records hold response bodies at rest, the preflight is a
  point-in-time check, and `STRUCTURED_REPLICAS` remains a declared count.
