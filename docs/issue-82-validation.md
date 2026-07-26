# Issue 82 Validation

This page records the acceptance criteria and automated evidence for Codex pool
cooldown and account rotation before model overflow.

## Acceptance Criteria

- **AC1:** Given two pooled clients and client A returns a rotate-eligible
  429/quota at connect, when the request is retried, then client B is selected
  and the request can succeed without changing the client model id.
- **AC2:** Given client A is cooling, when a new request's sticky hash would
  pick A, then selection skips A until `cooldown_until`.
- **AC3:** Given a stream already emitted a content/tool delta, when a later
  upstream 429 occurs, then the pool does not switch accounts for that request.
- **AC4:** Single-client pools and `CODEX_CLIENT_POOL_UNAVAILABLE=fail` still
  behave as documented when no alternate member exists.

## Automated Evidence

Run from the repository root:

```bash
GOCACHE=$PWD/.gocache go test -count=1 ./internal/codex ./internal/config ./internal/server
GOCACHE=$PWD/.gocache go test -race -count=1 ./internal/codex ./internal/server
```

Result recorded on 2026-07-21:

```text
ok  github.com/teslashibe/open-chat-api/internal/codex
ok  github.com/teslashibe/open-chat-api/internal/config
ok  github.com/teslashibe/open-chat-api/internal/server
```

The following regression tests map directly to the criteria:

| Criterion | Tests |
| --- | --- |
| AC1 | `TestPooledServiceRotatesConnectQuotaWithoutChangingModel`, `TestPooledServiceRotatesFirstEventQuota`, `TestApplyQuotaFallbackUsesAccountRotationBeforeModelOverflow` |
| AC2 | `TestPooledServiceCoolingStickyClientIsSkippedUntilExpiry`, `TestPooledServiceCooldownAndLeaseAcquisitionAreAtomic`, `TestPooledServiceHonorsRetryHint` |
| AC3 | `TestPooledServiceDoesNotRotateAfterContentOrToolDelta`, `TestPooledServiceBoundsRotationToOneAlternate` |
| AC4 | `TestPooledServiceSingleClientAndAllCoolingCompatibility`, `TestPooledServiceAllCoolingPreservesStickyClientFailureClass`, `TestApplyQuotaFallbackRunsOverflowAfterPoolExhaustion`, `TestStreamingConnectQuotaRunsModelFallback`, `TestAllCoolingRateLimitDoesNotRunModelFallback` |

The race suite covers the new mutex-protected cooldown state and concurrent
server diagnostics. The full functional suite also verifies config parsing,
retry/reset hint parsing, redacted diagnostics, and existing OpenAI-compatible
quota fallback behavior.
