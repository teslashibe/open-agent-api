# Issue 86 Validation

This page records the automated evidence for the Prometheus queue, pool,
cooldown, quota, request, and active-stream metrics surface.

## Acceptance Criteria

- **AC1:** Given traffic through chat completions, when scraped, then counters
  for requests and at least one histogram for queue wait or request duration
  increase.
- **AC2:** Given a cooldown rotate event, when scraped, then a cooldown/rotate
  counter increments.
- **AC3:** Metrics labels do not include raw tenant ids, bearer tokens, or full
  model prompt hashes.
- **AC4:** When metrics are disabled via config, server behavior matches the
  pre-ticket surface; no Kubernetes sidecar is required.

## Automated Evidence

Run from the repository root:

```bash
GOCACHE=$PWD/.gocache go test -count=1 ./...
GOCACHE=$PWD/.gocache go test -race -count=1 ./internal/metrics ./internal/codex ./internal/server
GOCACHE=$PWD/.gocache go vet ./...
```

Result recorded on 2026-07-21: all packages passed the full and race test
suites, and `go vet` reported no findings.

The following regression tests map directly to the criteria:

| Criterion | Tests |
| --- | --- |
| AC1 | `TestMetricsEndpointRecordsRequestsAndQueueWait`, `TestMetricsExposeStableBoundedSurface` |
| AC2 | `TestPooledServiceRecordsCooldownRotationAndSkipMetrics` |
| AC3 | `TestMetricsNormalizeUntrustedLabels`, `TestMetricsEndpointRecordsRequestsAndQueueWait`, `TestPooledServiceRecordsCooldownRotationAndSkipMetrics` |
| AC4 | `TestMetricsEndpointDisabledReturnsNotFound`, `TestDisabledMetricsAreNoopAndNotFound`, `TestMetricsEndpointUsesGatewayBearerWhenConfigured`, `TestLoadMetricsEnvironmentAndFlag` |

The race suite covers concurrent pool cooldown state, queue observations, the
private Prometheus registry, and stream gauge cleanup. The full suite verifies
that existing OpenAI-compatible request behavior remains unchanged.
