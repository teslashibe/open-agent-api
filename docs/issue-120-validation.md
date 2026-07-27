# Issue 120 Validation

This page records the evidence for durable structured-inference idempotency:
keys shared across gateway replicas and surviving restarts, so a Report Studio
retry never double-bills or returns a divergent body.

It closes the residual `rs-gpt-idempotency-process-local` finding from
[issue-116-validation.md](./issue-116-validation.md).

Defaults do not move. `STRUCTURED_INFERENCE_ENABLED=false`,
`STRUCTURED_IDEMPOTENCY_BACKEND=memory`, `STRUCTURED_REPLICAS=1`: a gateway that
validates today still validates, and its behavior is unchanged.

## Acceptance Criteria

- **AC1:** The store is not process-local-only. Replay works across pods that
  share the configured durable backend, and a multi-replica deployment that
  would be unsafe is rejected fail-closed.
- **AC2:** Restarting a gateway process does not lose in-TTL entries.
- **AC3:** The same idempotency key and scope returns the same stored body and
  status without a second upstream call.
- **AC4:** Tests cover cross-process and shared-store replay; `go vet` and
  `-race` stay clean for the concurrency packages.
- **AC5:** Docs update the issue 116 note: the residual risk is resolved, and
  what remains is bounded by the fail-closed guard.

## How it works

`IdempotencyStore` keeps its in-process single-flight layer and gains an
`IdempotencyBackend` seam (`internal/structured/idempotency.go`):

1. **Local hit** — a live in-process entry replays as before. A concurrent
   duplicate still waits on the in-flight call rather than issuing a second one.
2. **Store hit** — on a local miss the store asks the backend. A non-expired
   record is replayed verbatim with `idempotent_replay: true` and no upstream
   call. This is both the cross-pod and the post-restart path.
3. **Reservation** — on a backend miss the store takes a cross-process
   reservation. The winner calls upstream; the loser polls for the record on a
   10 ms tick, bounded by the caller's context, so a wedged peer degrades to
   `timeout` rather than hanging. After winning a reservation the store
   re-checks for a record, because the previous owner publishes its record just
   before dropping the reservation.
4. **Publish** — only a success is written. A failure releases the reservation
   without a record, so every replica can still retry it.

Backend faults never become a new error class: a failed load, reserve, or store
is counted as `backend_error` and degrades to running the request.

`FileBackend` (`internal/structured/idempotency_file.go`) is the durable
implementation, with no new module dependency:

- Records live at `<dir>/<key[0:2]>/<key>.json`, written to a temp file and
  `rename`d into place, so a crashed writer never leaves a half-read record.
- Reservations are `<key>.lock` files published with `link`, which is atomic
  with their content — a peer can never observe an empty lock and mistake a live
  reservation for an abandoned one. A lock whose stamp has passed is stolen, so
  a dead pod cannot wedge a key for longer than `STRUCTURED_MAX_DEADLINE`.
- Unparseable, truncated, wrong-version, and expired records are misses, and the
  bad file is unlinked.
- Directories are `0700` and records `0600`. A record holds the extracted
  payload, so the volume is a data-at-rest surface.
- Every write sweeps: expired records, lapsed locks, and orphaned temp files go,
  and the newest `maxEntries` records are kept.

## Configuration

| Knob | Default | Meaning |
| --- | --- | --- |
| `STRUCTURED_IDEMPOTENCY_BACKEND` | `memory` | `memory` (process-local) or `file` (durable, shared) |
| `STRUCTURED_IDEMPOTENCY_DIR` | *(empty)* | Shared directory for the file backend; required when it is selected |
| `STRUCTURED_REPLICAS` | `1` | Replicas sharing this configuration |
| `STRUCTURED_IDEMPOTENCY_TTL` | `10m` | How long a stored response can be replayed |

Flags mirror them: `--structured-idempotency-backend`,
`--structured-idempotency-dir`, `--structured-replicas`.

The fail-closed guard (`Config.validateStructured`) rejects, at startup:

- an unknown backend value;
- `backend=file` with an empty directory;
- `STRUCTURED_INFERENCE_ENABLED=true` and `STRUCTURED_REPLICAS > 1` on the
  memory backend, naming both remedies ("set `STRUCTURED_IDEMPOTENCY_BACKEND=file`
  with a shared `STRUCTURED_IDEMPOTENCY_DIR`, or run a single replica").

## Automated Evidence

Run from the repository root:

```bash
GOCACHE=$PWD/.gocache go build ./...
GOCACHE=$PWD/.gocache go vet ./...
GOCACHE=$PWD/.gocache go test -count=1 ./...
GOCACHE=$PWD/.gocache go test -race -count=1 ./internal/structured ./internal/server ./internal/config ./internal/metrics
```

Result recorded on 2026-07-26: `go build` and `go vet` reported nothing, every
package passed `go test -count=1 ./...`, and the four concurrency packages
passed under `-race` (repeated with `-count=3` and, for
`./internal/structured`, `-count=10`).

| Criterion | Tests |
| --- | --- |
| AC1 | `TestFileBackendReplaysAcrossStores`, `TestStructuredInferenceReplaysAcrossRestartsAndReplicas`, `TestLoadRejectsMultiReplicaWithoutADurableIdempotencyStore`, `TestMultiReplicaGuardOnlyAppliesWhenStructuredIsEnabled`, `TestStructuredInferenceMemoryBackendStaysProcessLocal`, `TestFileBackendBoundsTheDirectory` |
| AC2 | `TestFileBackendSurvivesRestart`, `TestFileBackendExpiresRecords`, `TestStructuredInferenceReplaysAcrossRestartsAndReplicas` |
| AC3 | `TestStructuredInferenceReplaysAcrossRestartsAndReplicas` (identical JSON, HTTP 200, `idempotent_replay: true`, zero upstream calls on the second process), `TestFileBackendReplaysAcrossStores`, `TestStoreObservesStoreHits` |
| AC4 | `TestFileBackendReplaysAcrossProcesses` (re-execs the test binary), `TestFileBackendSingleFlightsAcrossStores`, `TestStructuredInferenceConcurrency` (`duplicate-file-backend/1,2,4,8`), `TestFileBackendStealsLapsedReservations`, `TestFileBackendTreatsCorruptRecordsAsMisses`, `TestFileBackendDoesNotPersistFailures`, `TestStoreDegradesWhenTheBackendFails` |
| AC5 | This page, the updated limitation bullet in `docs/issue-116-validation.md`, `website/docs/api.md`, `website/docs/install/kubernetes.md` |

Metrics: `codex_chat_api_structured_idempotency_total{result}` with the closed
label set `local_hit`, `store_hit`, `miss`, `backend_error` (`unknown` for
anything else), asserted by `TestStructuredMetricsExposeBoundedSurface`,
`TestStructuredMetricsBoundModelAndCodeCardinality`, and
`TestStructuredInferenceRecordsMetrics`.

## Compatibility

- `NewIdempotencyStore(ttl, maxEntries, now)` is unchanged and still
  memory-only; `NewIdempotencyStoreWithBackend` is the additive constructor.
- `ContractVersion` stays `1.0.0`. No envelope field was added: a replay is
  still a 200 with `idempotent_replay: true` and a freshly measured
  `latency_ms`.
- `go.mod` is unchanged — no Redis, no database driver.
- Only successes are stored; a failure is always retryable.

## Residual limits

- **A shared filesystem is not a transactional store.** Replay after a
  completed request is exact — the record is published atomically and read
  whole. Cross-process single-flight is best-effort: on a slow RWX/NFS volume
  two pods can both steal a reservation whose stamp has lapsed, and the loser of
  that race issues a second upstream call. The reservation window is tied to
  `STRUCTURED_MAX_DEADLINE`, so the exposure is bounded by one request's
  duration.
- **`rename` and `link` semantics vary on network filesystems.** A
  `ReadWriteMany` PVC with POSIX semantics is the supported substrate. Where
  that is unavailable, the fail-closed guard is the alternative: run a single
  replica.
- **Records hold response bodies at rest,** including extracted `data`. Modes
  are `0700`/`0600`, entries expire with the TTL, and the sweep runs on the
  write path. Treat the volume as sensitive.
- **A replay carries the storing pod's `build` and `model_policy_version`.**
  That is the point — the body is the stored one, byte for byte, except for the
  freshly measured `latency_ms` and `idempotent_replay: true`. During a mixed
  version rollout a replay can therefore report the older pod's build. The
  policy version is part of the key, so a policy change never replays across it.
- **The store still bounds itself by count, not bytes.** A workload with
  unusually large payloads should lower `STRUCTURED_IDEMPOTENCY_TTL` or size the
  volume accordingly.
- **`STRUCTURED_REPLICAS` is a declared count, not a detected one.** The guard
  cannot see an HPA scaling up, a surge pod during a rolling update, or a stray
  process on the same volume. Issue 122 turns this from a silent assumption into
  a startup `structured_idempotency_warning` and a deployment requirement
  (`file` backend, or `maxSurge=0` / `strategy: Recreate` on the memory
  backend). See [issue-122-validation.md](./issue-122-validation.md). Peer
  detection remains out of scope.
