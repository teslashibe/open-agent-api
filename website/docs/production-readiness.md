---
title: Production readiness runbook
sidebar_label: Production readiness
---

# Production readiness runbook

Use this checklist for every production candidate and incident response. The
repository owns the portable alert and response contract. Cluster-specific
`PrometheusRule`, probe, Alertmanager route, replica, and image-pin manifests
belong in `teslashibe/k8s-control`.

## Operating constraint: run one replica

Run **one codex-chat-api replica** in dev and production. Cooldowns, inflight
leases, auth health, and 24-hour soft pins are process-local. Replicas do not
share them, so multiple replicas can disagree about a credential and route the
same conversation differently. `CODEX_AGENT_QUEUE_LOCK_DIR` shares only Agent
queue-key exclusion; it does not share pool health, cooldowns, leases, or pins.

Keep `CODEX_AGENT_MAX_ACTIVE_PER_KEY=1`. Do not scale out during an incident or
a canary. A multi-replica design needs shared pool state and sticky routing and
is outside the current production contract.

## Preflight

From the candidate checkout, record the commit and run every local gate:

```bash
GOCACHE=$PWD/.gocache go test -count=1 ./...
GOCACHE=$PWD/.gocache go test -race -count=1 ./internal/codex ./internal/codextest ./internal/server
GOCACHE=$PWD/.gocache go vet ./...
GOCACHE=$PWD/.gocache go build ./...
docker build -t codex-chat-api:preflight .
(cd website && npm ci && npm run build)
promtool check rules examples/prometheus/codex-chat-api.rules.yml
promtool test rules examples/prometheus/codex-chat-api.rules.test.yml
```

Do not continue if any command fails. Confirm that the candidate changes no
credential material and that `CODEX_CLIENTS` uses only bounded, non-sensitive
labels. Verify in `k8s-control` that dev and production each request one replica,
use `/health/live` for liveness and `/ready` for readiness, seed every Codex
Secret into a writable per-client `emptyDir`, and expose
`/metrics` only to the approved scraper.

## Dev deploy and soak

1. Merge the candidate to `main` and wait for image publication.
2. Confirm the registry contains `sha-<short>` for that exact commit and record
   its digest.
3. Confirm only the dev image pin changed in `k8s-control` and wait for one dev
   replica to reconcile to the recorded digest.
4. Run the smoke checks in [Dev validation](./dev-validation.md).
5. Run a 30-minute soak through the approved dev access path:

```bash
API_URL=https://dev.example.invalid \
SOAK_LABEL=dev \
SOAK_DURATION=30m \
GATEWAY_BEARER_SECRET_FILE=/run/secrets/codex-chat-api-gateway \
./scripts/dev-soak.sh
```

The script checks liveness/readiness before starting, then checks readiness and
both non-streaming and streaming completions on every iteration. It prints only
aggregate counts. It never prints response bodies, the target URL, or bearer
material. Use `GATEWAY_BEARER_SECRET` only when an approved secret-file mount is
not available.

Record the commit, immutable image tag and digest, start/end timestamps, pass
counts, and alert state. Redact hosts and identifiers. The soak passes only with
zero failures unless an explicit, reviewed `SOAK_MAX_FAILURES` budget is set.

## Single-replica canary

There is no side-by-side multi-replica canary. The dev `sha-<short>` deployment
is the canary:

1. Keep one replica and the production concurrency settings.
2. Confirm `/health/live` and `/ready` return `200`.
3. Exercise models, non-streaming completion, streaming completion, and one
   tool-capable Agent turn.
4. Confirm request success, bounded queue wait, zero auth-unhealthy clients,
   and no unexpected cooldown/stream alerts during the soak.
5. Start a drain from loopback, confirm readiness becomes `503` and new work is
   rejected, stop the drain, and confirm readiness returns to `200`.

Do not tag a candidate that has not completed this canary and soak.

## Credential rotation

The gateway normally refreshes access-token JWTs five minutes before expiry,
persists rotated access/refresh/ID tokens atomically, and needs no restart. A
WebSocket 401/403 triggers one forced refresh and one redial. Each `auth.json`
must therefore be a writable runtime copy with a single writer, not the
read-only Secret mount itself.

For an active authentication incident, restore service in this order:

1. Run `codex login` through the approved operator flow and obtain a fresh
   `auth.json` without printing its contents.
2. Update the SOPS-managed `codex-chat-api-secrets` `CODEX_AUTH_JSON` value.
3. Reconcile the Secret and restart `deployment/codex-chat-api` in the `smore`
   namespace so the init container reseeds the writable runtime copy.
4. Verify `/health/live` and `/ready` are `200`, then send one Terra completion
   from `smore-api`. Confirm it succeeds before resuming Growth LLM stages.

Use the approved equivalents of the following operational commands; do not put
credential contents in shell arguments or transcripts:

```bash
kubectl -n smore rollout restart deployment/codex-chat-api
kubectl -n smore rollout status deployment/codex-chat-api
kubectl -n smore get pods -l app=codex-chat-api
```

Rotate one pool credential at a time so another client remains selectable:

1. Record the safe `client_label`; never record the account, token, auth path,
   or credential contents.
2. Obtain a new login through the approved operator flow. Validate its JSON and
   permissions outside the application transcript.
3. Update the source Secret, then atomically replace or restart to reseed the
   writable runtime `auth.json`. Do not edit a live credential in place or
   reuse a revision already rejected by this process.
4. Poll `/ready` and `/metrics`. Readiness never opens a model WebSocket, but it
   may perform the same bounded OAuth refresh as a request. A locally
   auth-unhealthy client recovers when the pool observes a different valid
   credential revision; a restart also reloads all credentials.
5. Send a canary completion and confirm the client returns to
   `codex_chat_api_pool_client_usable{client_label="..."} 1` with aggregate
   `codex_chat_api_pool_usable_clients` increased by one and back at the
   expected configured total.
6. Observe a stable interval before rotating the next client. Revoke the old
   credential only after the replacement is verified.

If the new revision fails, restore the previously valid mounted secret revision
through the secret-management workflow or restart with the last known-good
secret set. Never solve rotation by raising concurrency or adding a replica.

## Alert response

Install the portable rules from
`examples/prometheus/codex-chat-api.rules.yml`. Keep selectors, the blackbox
probe for `GET /ready`, and notification routes in `k8s-control`.

### Capacity saturation

`CodexChatAPICapacitySaturation` uses bounded capacity-class `429` telemetry.
That class can include pool, provider, or queue capacity and is not proof of a
specific source. Check `queue_wait_seconds_count{result=~"full|timeout"}`,
active streams, and `codex_client_pool_saturated` logs. Back off callers and
restore upstream capacity. Raise limits only with soak evidence; do not scale
replicas.

### No usable clients or auth failure

`CodexChatAPINoUsableClients` and `CodexChatAPIClientAuthFailure` use the local
pool health gauges. Confirm `/ready` is `503`, stop callers that receive
`upstream_authentication_failed`, then follow the one-at-a-
time credential procedure. If every client is unhealthy, restore one known-good
revision first. Do not restart repeatedly with unchanged rejected credentials.

## Token-expiry drill and caller circuit break

Before promotion, seed a test login whose access-token JWT is expired while its
refresh token remains valid. One readiness or completion call must refresh and
persist the token, and the completion must succeed without a pod restart. Then
repeat with a rejected refresh token: `/ready` must return `503`, the client
must be removed from selection, and responses must carry
`upstream_authentication_failed`.

Growth judge, draft, and outbox workers must treat that code as a shared LLM
circuit-break signal: stop new LLM work, use exponential backoff, and resume
only after `/ready` and a bounded canary completion succeed. Discovery may
continue, but it must not enqueue or retry LLM-dependent work while the circuit
is open. This caller policy lives in `teslashibe/smore`; verify its expiry drill
before enabling the production schedule.

### All clients cooling

`CodexChatAPIAllClientsCoolingSuspected` compares, per scrape target, the number
of distinct client labels with recent bounded cooldown skips against that
target's usable-client count. Repeated skips from one client count once, and
clients from different instances or environments are not combined. Cooldown
metrics are event counters, not a current cooldown gauge, so the alert is
evidence rather than proof. Confirm cooldown and selection logs, honor upstream
reset hints, reduce load, and wait for the cooldown. Readiness can remain `200`
because cooling is not auth-unhealthy.

### Queue timeout or high wait

`CodexChatAPIQueueTimeouts` detects terminal queue timeouts;
`CodexChatAPIQueueWaitHigh` detects acquired-request p95 above 30 seconds. Check
provider-specific queue limits, active streams, and upstream latency. Preserve
`CODEX_AGENT_MAX_ACTIVE_PER_KEY=1` and avoid increasing concurrency until the
same workload passes a dev soak.

### Stream failure

`CodexChatAPIStreamFailures` detects bounded upstream errors at connect,
first-event, or mid-stream phases. Mid-stream failures never rotate credentials
because output has already reached the caller. Correlate provider health and the
candidate digest; roll back if failures began with the release.

### Readiness loss

`CodexChatAPIReadinessLost` expects an external blackbox probe whose job is
`codex-chat-api-readiness`. Check `/health/live` first. If live is `200`, inspect
the readiness body: `draining` is intentional only during a controlled rollout;
`unavailable` means no locally usable credential. Stop an abandoned drain from
loopback or recover a credential. Liveness must not depend on upstream status.

## Promotion gates

Create a new immutable `vX.Y.Z` tag only when all gates are true:

- the exact commit is on `main` and all preflight checks passed;
- `sha-<short>` exists and dev runs the matching digest with one replica;
- the 30-minute dev soak, drain check, and Agent canary passed;
- no unexplained capacity, cooling, auth, queue, stream, or readiness alert is
  firing;
- credential rotation is complete or explicitly deferred with valid clients;
- a previously verified immutable `v*` rollback target and its digest are
  recorded.

Follow [Tagged release](./tagged-release.md) to create and push the tag. A `v*`
tag is the only production promotion trigger. Never promote `main`, `latest`, a
mutable tag, or a commit that differs from the soaked dev digest.

## Rollback

Rollback is an image-pin operation in `k8s-control`, not a moved source tag:

1. Stop promotion and record the firing alerts, current `v*` tag, and digest.
2. Select the last verified immutable `v*` tag and confirm its recorded digest.
3. Through the reviewed `k8s-control` process, change only the production image
   pin to that tag. Never force-move or recreate a release tag.
4. Wait for the single replica to drain and reconcile. Verify the running digest
   exactly matches the rollback digest.
5. Repeat health, readiness, completion, streaming, Agent, and metrics checks.
6. Confirm release-correlated alerts clear and preserve redacted evidence for
   the incident review.

If the incident is credential-only, roll back the secret revision through the
credential workflow instead of changing the image. If both changed, restore one
dimension at a time so recovery has a clear cause.
