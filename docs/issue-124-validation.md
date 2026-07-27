# Issue 124 Validation

Final-validation follow-up from [issue-122-validation.md](./issue-122-validation.md)
on `feature/rs-gpt-structured-inference`. Three things could fail silently
before and fail loudly now:

1. **Unusable idempotency storage.** A missing or unwritable
   `STRUCTURED_IDEMPOTENCY_DIR` degraded to a process-local store and logged
   `backend=file` anyway. On more than one replica that means duplicate
   `idempotency_key`s bypass single-flight and get billed twice, with nothing in
   the logs saying so.
2. **An unbound idempotency key.** The storage key scoped caller, operation,
   input, `schema_version`, and `model_policy_version` — but not `model`,
   `reasoning_effort`, `verbosity`, `max_output_tokens`, or the schema body. A
   caller could reuse one key with a different model and be handed a response
   produced by another one.
3. **A false schema-failure metric.** `normalizeStructuredValidation` fell back
   to `invalid`, so a typo or a label added later was counted as real model
   non-compliance on the one metric operators judge schema health by.

Defaults do not move. `STRUCTURED_INFERENCE_ENABLED=false`,
`STRUCTURED_IDEMPOTENCY_BACKEND=memory`, `STRUCTURED_REPLICAS=1`: a gateway that
starts today still starts, and no configuration became newly rejected unless it
was already pointing at storage that could not work.

## Acceptance Criteria

- **AC1:** File backend startup preflights directory creation/access and
  write+sync+rename/link semantics needed by the backend; enabled multi-replica
  startup fails closed with an actionable, secret-free error if the configured
  directory is unusable.
- **AC2:** Startup logging claims `backend=file` only after successful
  preflight; tests cover missing parent, read-only/unwritable path, and usable
  path.
- **AC3:** Persisted idempotency records bind the key to a canonical request
  fingerprint covering model, reasoning effort, verbosity, output limit, schema
  body/version, policy version, operation, caller and canonical input.
- **AC4:** Reuse of the same key/scope with a different fingerprint returns a
  deterministic conflict response and performs no upstream call. Identical
  retries continue replaying the stored result without another bill.
- **AC5:** Compatibility/versioning for pre-existing persisted records is
  explicit and tested.
- **AC6:** `normalizeStructuredValidation` maps unknown labels to `unknown`;
  existing valid/invalid/unparsable behavior remains intact and tests cover
  typo/new labels.
- **AC7:** Build, full tests, vet, and race tests for structured/server/config/
  metrics pass; validation docs are updated.

## The preflight (AC1, AC2)

`(*FileBackend).Preflight()` (`internal/structured/idempotency_file.go`) runs
exactly the syscalls the backend depends on, in a `.preflight` scratch directory
under the configured path:

```
MkdirAll(dir/.preflight)            → the volume exists and is traversable
CreateTemp(...)  → Write → Sync     → Store's durability path
Rename(tmp, probe.json)             → Store's atomic publish
Link(probe.json, probe.lock)        → claimLock's reservation primitive
Remove(both) + RemoveAll(.preflight)→ nothing is left behind
```

`os.Link` is the interesting one: it is what `claimLock` uses to make a
reservation atomic, and it is the semantic a weak network filesystem is most
likely to be missing. A directory that passes this can store a record and take a
reservation; one that fails cannot.

Each failure is wrapped as

```
structured idempotency preflight: <stage>: dir "<path>" is unusable;
point STRUCTURED_IDEMPOTENCY_DIR at a writable shared volume: <os error>
```

The only interpolated values are a constant stage name, the operator-supplied
path, and the OS error — no record body, no key, nothing derived from a
credential. It is safe to log at startup and safe to surface in a pod event.

**Where it fails closed.** `server.PreflightStructuredIdempotency(cfg)` is
called from `run()` in `cmd/open-agent-api/main.go` immediately after
`config.Load`, before any listener binds; `main` prints the error and exits 1.
It returns `nil` for every configuration that does not depend on the shared
volume (structured inference off, or the memory backend — which `Config.Validate`
already refuses for more than one replica).

This is **stricter than AC1's literal minimum**: startup fails whenever
structured inference is enabled on the file backend, not only at
`STRUCTURED_REPLICAS > 1`. That matches the issue title — "fail closed on
unusable idempotency storage" — and it converts a previously silent
degrade-to-memory into a visible `CrashLoopBackOff` for a single-replica
deployment with a bad mount. Recorded as a deliberate choice, not an oversight.

**Where it degrades.** `newStructuredIdempotencyStore` (`internal/server/server.go`)
repeats the preflight and logs `backend=memory reason=…` if it fails, so
`backend=file` is only ever claimed after the check passes. `server.New` keeps
its signature (≈40 test call sites) and never panics; the hard failure lives in
`main`. This second call also covers a hand-built `config.Config` and a volume
that went away between `config.Load` and server construction.

Construction stays lazy — `NewFileBackend` does not preflight — so the memory
path and unit tests are unaffected.

## The request fingerprint (AC3, AC4)

`internal/structured/fingerprint.go`. `KeyParts.Key()` scopes *storage*: two
requests differing in caller, operation, input, `schema_version`, or
`model_policy_version` never share a slot. `Fingerprint` binds that slot to the
exact inference:

| Field | Source |
| --- | --- |
| `Caller` | tenant header, else hashed `Authorization` |
| `Operation` | `operation` |
| `Model` | `model` as sent |
| `ResolvedModel` | the alias the policy resolved it to |
| `ReasoningEffort` | effective value sent upstream (request override, else alias default) |
| `Verbosity` | effective value sent upstream |
| `MaxOutputTokens` | effective, already-clamped output limit |
| `SchemaVersion` | `schema_version` |
| `SchemaChecksum` | SHA-256 of the **canonicalized** schema body |
| `ModelPolicyVersion` | `Policy.Version()` at admission |
| `InputChecksum` | SHA-256 of `input` |

Components are length-prefixed exactly as in `KeyParts.Key()`, so no
concatenation of two components can be confused for another pair.

**Deliberately excluded**, because they do not change the inference:
`deadline_ms` (how long the caller will wait), `schema_name` (an upstream label
only — the schema *body* is what constrains output), and `request_id`
(per-attempt trace identity; a retry always brings a new one). A caller may vary
all three freely on a retry.

The overlap with `KeyParts` is intentional. A fingerprint that covered only the
*difference* would be unreadable and would silently stop binding if `KeyParts`
ever narrowed.

**Effective, not raw, values.** Effort, verbosity, and the output limit are
fingerprinted after resolution and clamping, so two requests that produce the
same upstream call are a replay rather than a conflict — e.g. two different
`max_output_tokens` values that both clamp to `STRUCTURED_MAX_OUTPUT_TOKENS`.

**Schema canonicalization is load-bearing.** Key order and whitespace are not
part of a schema's meaning. Without canonicalization, a client that
re-serializes its schema between retries would be told its own retry is a
conflict. `canonicalJSON` decodes with `UseNumber` and re-marshals
(`encoding/json` sorts `map[string]any` keys), so:

- key order and indentation are erased;
- array order is preserved (`enum` is ordered, and `required` order is cheap to
  keep);
- numeric literals keep their exact form, so `1` and `1.0` stay distinct and a
  53-bit-plus integer does not round through `float64`. Conservative in the safe
  direction: a changed literal conflicts rather than silently replaying.

## Where a conflict is detected (AC4)

`IdempotencyStore.Do(ctx, key, fingerprint, fn)`. Both layers check, and both
check **before** anything is reserved or called:

- **Local hit or in-flight entry.** `idempotencyEntry` carries the fingerprint
  it was created with (written once under `s.mu`, never mutated). A duplicate
  whose fingerprint differs returns `ErrIdempotencyConflict` *before* it waits
  on `entry.ready` — it must not join a single-flight and be handed the answer
  to a different question.
- **Durable record.** In `resolve`, both `s.load(key)` sites route through
  `resolveRecord`, which conflicts on `record.Fingerprint != fingerprint`
  instead of replaying. That happens before `backend.Reserve`, so no reservation
  is taken and none is left behind.

`ErrIdempotencyConflict` is a `*structured.Error` carrying
`CodeIdempotencyConflict`, so `structuredFailure` maps it with the same
`ErrorAs` branch every other contract error uses. Outcome label: `conflict`.
Nothing is stored, `fn` never runs, and the existing binding survives — the
matching retry still replays.

## On the wire (AC4)

- `ContractVersion` `1.0.0` → **`1.1.0`**: additive error code, per the bump
  rule in `contract.go`.
- New code `idempotency_conflict` → **HTTP 409**, so a client that branches only
  on status still distinguishes "your key means something else" from a
  retryable failure.
- New log line, matching the `structured_shed` / `structured_error` shape:

  ```
  structured_conflict request_id=… operation=… model=…
  ```

  Its own greppable prefix keeps a caller mistake out of the gateway
  error-rate signal.

## Record compatibility (AC5)

`IdempotencyRecordVersion` `1` → **`2`** (adds `fingerprint`).
`decodeIdempotencyRecord` accepts only the current version — the rule the file
already followed — which makes the transition explicit in both directions:

| Direction | Behavior |
| --- | --- |
| **v1 record read by this build** | Miss. The record is unlinked, the call runs once, and the result is republished at v2 bound to a fingerprint. |
| **v2 record read by a rolled-back build** | Miss, by the same rule. The old pod re-runs and republishes at v1. |

A v1 record carries no fingerprint, so nothing proves which parameters produced
it. Replaying it is precisely what this issue forbids, and treating it as a
*conflict* would be worse still: it would wedge the key for its whole TTL.

**The cost is explicit:** the first request per live key after the rollout calls
upstream once more — one extra bill per live key, bounded by
`STRUCTURED_IDEMPOTENCY_TTL` (default `10m`). Records are not migrated in place
because a migration would have to invent a fingerprint, which is the same
unprovable binding by another name.

## The metric label fix (AC6)

`internal/metrics/metrics.go`:

```go
-return allow(value, "invalid", "valid", "invalid", "unparsable")
+return allow(value, "unknown", "valid", "invalid", "unparsable")
```

`allow`'s second argument is the *fallback*. With `invalid` there, a typo
(`"vaild"`), an unset value, or a label added by a later change was counted as a
real schema failure on
`codex_chat_api_structured_validation_total{result="invalid"}` — the metric an
operator uses to decide whether a model is complying with a schema.

Two other closed sets grew, so the new 409 path is observable rather than folded
into `unknown`: `normalizeStructuredCode` gains `idempotency_conflict`, and
`normalizeStructuredIdempotency` gains `conflict`. Both metric `Help` strings
now list their full label set.

## Criterion → test

| Criterion | Tests |
| --- | --- |
| AC1 | `TestFileBackendPreflightAcceptsAUsableDirectory` (creates a nested dir, leaves nothing behind, is repeatable), `TestFileBackendPreflightFailsClosedOnAnUnusableDirectory` (missing parent + read-only dir; asserts the dir and `STRUCTURED_IDEMPOTENCY_DIR` are named and no record/credential material leaks), `TestPreflightStructuredIdempotencyFailsClosed` (enabled, `STRUCTURED_REPLICAS=3`, unusable dir → error; usable dir, structured-off, and memory backend → nil) |
| AC2 | `TestStructuredIdempotencyBackendLogClaimsFileOnlyAfterPreflight` — `backend=file` on a usable `t.TempDir()`; `backend=memory reason=…` with the setting name and **no** `backend=file` on an unusable one |
| AC3 | `TestFingerprintCoversEveryMaterialParameter` (all 11 fields), `TestFingerprintIsNotVulnerableToComponentSmuggling`, `TestSchemaChecksumIgnoresFormattingAndKeyOrder`, `TestSchemaChecksumPreservesArrayOrder`, `TestSchemaChecksumKeepsIntegerPrecision`, `TestSchemaChecksumFallsBackToTheRawBytes` |
| AC4 | `TestStructuredInferenceConflictsOnChangedParameters` (model / effort / verbosity / output limit / schema body → 409 + `idempotency_conflict`, zero upstream calls, and the original key still replays), `TestStructuredInferenceConflictsAcrossReplicas` (pod B rejects a divergent reuse of pod A's key, then replays the matching retry), `TestStructuredInferenceReplaysReformattedSchemas`, `TestStoreConflictsOnADivergentFingerprint`, `TestStoreConflictsWithAnInFlightDivergentCall`, `TestStoreWithoutKeyNeverConflicts`, `TestFileBackendConflictsOnADivergentFingerprint` (also asserts no reservation is left behind) |
| AC5 | `TestFileBackendTreatsVersionOneRecordsAsAMiss` — a hand-written v1 record is a clean miss, is unlinked, and the re-run republishes at v2 bound to a fingerprint |
| AC6 | `TestStructuredValidationUnknownLabelsDoNotForgeSchemaFailures` (7 typo/new/empty/case/whitespace labels all land on `unknown`, and `valid`/`invalid`/`unparsable` counters stay at zero, then still count themselves), `TestStructuredConflictLabelsAreCounted`, `TestStructuredMetricsBoundModelAndCodeCardinality` (updated) |
| AC7 | The untouched regressions `TestStructuredInferenceReplaysIdempotentRequests`, `TestStructuredInferenceReplaysAcrossRestartsAndReplicas`, `TestStructuredInferenceConcurrency` (including every `duplicate-file-backend/N` subtest), `TestFileBackendSingleFlightsAcrossStores`, `TestFileBackendReplaysAcrossProcesses`, plus this page and the website updates |

The two permission-based tests skip under `os.Geteuid() == 0`, because root
bypasses the mode bits they rely on and the assertions would otherwise pass
without testing anything.

## Automated Evidence

Run from the repository root:

```bash
GOCACHE=$PWD/.gocache go build ./...
GOCACHE=$PWD/.gocache go vet ./...
GOCACHE=$PWD/.gocache go test -count=1 ./...
GOCACHE=$PWD/.gocache go test -race -count=1 ./internal/structured ./internal/server ./internal/config ./internal/metrics
GOCACHE=$PWD/.gocache go test -race -count=10 ./internal/structured
```

Result recorded on 2026-07-26: `go build` and `go vet` reported nothing, every
package passed `go test -count=1 ./...`, the four concurrency packages passed
under `-race`, and `./internal/structured` passed `-race -count=10`.

## Compatibility

- **Breaking for in-flight persisted state:** `IdempotencyRecordVersion` `1` →
  `2`. Every record written by the current build is invalidated. See the table
  above for the exact cost.
- **Additive on the wire:** `ContractVersion` `1.0.0` → `1.1.0`, new error code
  `idempotency_conflict` (409). A client that switches exhaustively on
  `error.code` and has no default branch can break; 409 is the mitigation for
  clients that branch on status only.
- **Source-breaking in-package:** `IdempotencyStore.Do` takes a `fingerprint`
  argument. `internal/structured` is not a public API; the single production
  caller is `internal/server/structured.go`.
- **`server.New` is unchanged** — same signature, still never panics.
- **Newly rejected configuration:** `STRUCTURED_INFERENCE_ENABLED=true` plus the
  file backend plus an unusable directory now fails startup. Nothing that was
  actually working starts failing.
- Metric label sets grew (`unknown` on validation, `conflict` on idempotency,
  `idempotency_conflict` on code); nothing was removed or renamed.
- `go.mod` is unchanged.

## Residual limits

- **The fingerprint covers parameters, not the world.** Anything that changes
  model behavior without changing a request field — an upstream model revision
  behind a stable alias, a provider-side default change — is still replayable
  inside the TTL. `model_policy_version` catches allowlist changes; it cannot
  catch a silent upstream one.
- **A conflict is symmetric.** Two divergent requests arriving concurrently on
  one fresh key have no durable record to arbitrate: whoever creates the local
  entry first defines the binding, and the other gets a 409. That is the correct
  outcome for both orderings, but which one wins is a race.
- **A narrow spurious-conflict window.** A request that creates a local entry and
  *then* discovers a divergent durable record leaves that entry visible for one
  backend `Load`. A matching request arriving inside that window sees the
  divergent in-flight fingerprint and gets a 409 instead of a replay. It errs
  toward conflict — never toward a wrong replay or an extra bill — and the
  retry succeeds.
- **The preflight is a point-in-time check.** A volume that goes read-only or
  unmounts *after* startup is not re-detected; that path still degrades through
  `backend_error` and the store's existing fault handling.
- **Preflight is stricter than AC1's minimum,** as recorded above: a
  single-replica deployment with a bad mount now crash-loops instead of serving
  from memory.
- **Everything in issues 120 and 122 still holds:** a shared filesystem is not a
  transactional store, `rename`/`link` semantics vary on network filesystems,
  records hold response bodies at rest, the store bounds itself by count rather
  than bytes, and `STRUCTURED_REPLICAS` remains a declared count.
