# Issue 122 Validation

This page records the hardening of the durable structured-inference idempotency
rollout guard and the waiter poll loop, following the adversarial review of
[issue-120-validation.md](./issue-120-validation.md).

Two things were silent before and are explicit now:

1. The startup guard reads a **declared** replica count. It cannot observe an
   HPA, a surge pod during a rolling update, or a stray process, so a
   memory-backend deployment could double-call upstream for one
   `idempotency_key` without anything in the logs saying why.
2. A duplicate waiting on a peer's reservation did a full
   create/write/`fsync`/`link`/unlink round trip every 10 ms. On a shared PVC
   that is a write amplifier proportional to the number of retrying clients.

Defaults do not move. `STRUCTURED_INFERENCE_ENABLED=false`,
`STRUCTURED_IDEMPOTENCY_BACKEND=memory`, `STRUCTURED_REPLICAS=1`: a gateway that
validates today still validates, and no configuration became newly rejected.

## Acceptance Criteria

- **AC1:** The deployment/runtime guard no longer silently assumes
  `STRUCTURED_REPLICAS` equals the actual concurrent process count. Either add
  peer detection, or explicitly document and warn about declared-count drift and
  `maxSurge`; Kubernetes guidance requires the file backend or
  `maxSurge=0`/Recreate for safe memory-backed enablement.
- **AC2:** Enabled+memory configuration emits an explicit startup warning
  covering rolling-update/multi-process single-flight limits, with a test.
- **AC3:** `FileBackend.Reserve` cheaply checks an existing non-lapsed lock
  before attempting temp-file creation/fsync.
- **AC4:** Duplicate waiter polling backs off from 10 ms toward a bounded
  250–500 ms interval.
- **AC5:** Tests demonstrate held-lock waiters avoid per-poll temp-file/fsync
  writes; cross-process single-flight, stale reservation, concurrency, full
  tests, vet, and race remain green.
- **AC6:** Validation/Kubernetes docs record the operational constraints without
  treating them as silently accepted risk.

## The rollout warning (AC1, AC2)

`Config.StructuredIdempotencyWarnings()` (`internal/config/config.go`) derives
the operator-facing limits from the config. It is pure and additive: the
fail-closed rejection in `validateStructured` is untouched, and warnings only
fire when `STRUCTURED_INFERENCE_ENABLED=true`.

- **Memory backend:** single-flight and replay are process-local. A rolling
  update runs the old and new pods at once (`maxSurge > 0`), so a duplicate
  `idempotency_key` can reach two processes **even at
  `STRUCTURED_REPLICAS=1`**. Both remedies are named in the line itself:
  `STRUCTURED_IDEMPOTENCY_BACKEND=file` with a shared
  `STRUCTURED_IDEMPOTENCY_DIR`, or `maxSurge=0` / `strategy: Recreate`.
- **File backend:** `STRUCTURED_REPLICAS` is a *declared* count, not a detected
  one, and the guard does not observe drift from an HPA, a surge pod, or a stray
  process.

`newStructuredIdempotencyStore` (`internal/server/server.go`) emits each warning
through the existing log seam, one line per warning, before the
`structured_idempotency_backend ...` line:

```
structured_idempotency_warning detail="structured idempotency backend=memory is process-local: ..."
structured_idempotency_backend backend=file dir=/var/lib/... ttl=10m0s replicas=2
```

The prefix is stable and greppable, so an alert can be built on it.

Peer detection (heartbeat files in the shared directory) is deliberately **out
of scope**: it only works for the file backend, which is already the safe
configuration. Warn-and-document is what closes AC1, and the residual is
recorded below.

## The held-lock fast path (AC3)

`FileBackend.Reserve` now probes before it writes:

```
stat(<key>.lock)
  ├── ErrNotExist ──────────► claimLock (temp file → write → fsync → link)
  ├── exists, not lapsed ───► return acquired=false        (no writes at all)
  └── exists, lapsed ───────► unlink, then claimLock
```

`lockHeld` is the read-only half; it reuses `lockLapsed` for the stealability
decision, so there is exactly one definition of "lapsed". The probe is
**advisory only** — `os.Link` inside `claimLock` remains the sole authority for
who owns a key. The added stat→link TOCTOU window is benign in both directions:
a lock that appears after the probe makes `link` fail with `ErrExist`, which is
a clean `acquired=false`; a lock that vanishes after the probe just means the
next attempt claims it.

The common case on a shared PVC — many duplicates polling one in-flight call —
now costs one `stat` per poll instead of a create, a write, an `fsync`, a `link`
attempt, and an unlink.

`claimAttempts` is an unexported `atomic.Uint64` incremented at the top of
`claimLock`. It exists so a test can assert the write path was not entered. It
is **not a metric** and nothing outside the package reads it.

## Bounded waiter backoff (AC4)

`internal/structured/idempotency.go`:

| Constant | Value | Role |
| --- | --- | --- |
| `idempotencyPollInterval` | `10ms` | First delay — a fast peer is still noticed promptly |
| `idempotencyMaxPollInterval` | `250ms` | Ceiling |

`nextPollInterval(current)` doubles and clamps: `10 → 20 → 40 → 80 → 160 → 250
→ 250 …`. `resolve` holds `wait` local to the call, so every waiter starts at
the floor; the caller's `ctx` still bounds the whole wait, so a wedged peer
degrades to a timeout rather than a hang. Worst-case added latency for a
duplicate is one poll interval (≤ 250 ms), far below `STRUCTURED_MAX_DEADLINE`.

## Criterion → test

| Criterion | Tests |
| --- | --- |
| AC1 | `TestStructuredIdempotencyWarningsCoverRollingUpdates` (file-backend declared-drift note; warnings do not change what validates), `TestLoadRejectsMultiReplicaWithoutADurableIdempotencyStore`, `TestMultiReplicaGuardOnlyAppliesWhenStructuredIsEnabled` |
| AC2 | `TestStructuredEnabledMemoryBackendWarnsAtStartup` (present when enabled, absent when dark), `TestStructuredIdempotencyWarningsCoverRollingUpdates` |
| AC3 | `TestFileBackendReserveAvoidsWritesWhileALockIsHeld` — 32 blocked `Reserve` calls leave `claimAttempts` unchanged and the shard directories free of `.tmp-*` entries; the companion assertion proves a *lapsed* lock still takes the write path |
| AC4 | `TestWaiterPollIntervalBacksOffToABound` — deterministic, no sleeps: the 250–500 ms band, the doubling table, monotonicity, and convergence to the ceiling |
| AC5 | The AC3/AC4 tests above plus the untouched regressions `TestFileBackendSingleFlightsAcrossStores`, `TestFileBackendStealsLapsedReservations`, `TestFileBackendReplaysAcrossProcesses`, `TestStructuredInferenceConcurrency` |
| AC6 | This page, the residual-limits update in `docs/issue-120-validation.md`, `website/docs/install/kubernetes.md`, `website/docs/api.md` |

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

- `IdempotencyBackend`, `NewIdempotencyStore`, `NewIdempotencyStoreWithBackend`,
  `ContractVersion` (`1.0.0`), and `IdempotencyRecordVersion` (`1`) are
  unchanged.
- The metric label set is unchanged: `local_hit`, `store_hit`, `miss`,
  `backend_error`.
- The on-disk record and `.lock` formats are unchanged, so a mixed-version
  rollout interoperates on one PVC — an old pod's lock is honored by a new pod's
  probe and vice versa.
- No configuration became newly rejected; AC1 is satisfied by warn + document,
  not by a new startup failure.
- `go.mod` is unchanged.

## Residual limits

- **No peer detection.** The guard still cannot count the processes actually
  sharing a key space. That is now an explicit, warned-about constraint with a
  named remedy per backend, not a silent assumption — but an operator who
  ignores the warning and runs an HPA over the memory backend can still
  double-call upstream.
- **The stat→link window.** The fast path adds a TOCTOU gap between the probe
  and the claim. Correctness rests on `os.Link` staying the only authority; a
  lost race is a clean `acquired=false`, never a double claim. Any future change
  that moves an ownership decision out of `claimLock` breaks this argument.
- **Backoff costs latency.** A duplicate that arrives just after a poll waits up
  to 250 ms longer than it would have on the old fixed tick. This is visible in
  p99 replay latency and is the deliberate trade for bounded filesystem load.
- **Everything in issue 120 still holds:** a shared filesystem is not a
  transactional store, `rename`/`link` semantics vary on network filesystems,
  records hold response bodies at rest, and the store bounds itself by count
  rather than bytes.
