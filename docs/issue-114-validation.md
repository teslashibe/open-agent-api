# Issue 114 Validation

This page records the Codex OAuth expiry, readiness, and error-taxonomy evidence
for issue 114. Cluster probe and Secret seeding live in `teslashibe/k8s-control`;
the Growth caller circuit breaker lives in `teslashibe/smore`.

## Acceptance Criteria

- **AC1:** An expired access token with a valid refresh token is refreshed and
  persisted before the WebSocket dial; a 401/403 gets one forced refresh and
  one redial.
- **AC2:** A refresh failure removes the client from selection and makes
  `/ready` return `503` when no client remains usable.
- **AC3:** Inbound bearer failures return `invalid_gateway_credentials`;
  upstream Codex failures return `upstream_authentication_failed`.
- **AC4:** Once auth is unhealthy, repeated requests fail through the HTTP
  circuit before Agent queue admission or service invocation, with
  `upstream_authentication_failed` and `Retry-After: 300`. Growth must use that
  contract to pause judge/draft/outbox work with backoff.
- **AC5:** The production runbook documents login, SOPS Secret update, rollout,
  readiness, Terra canary, and caller-resume verification.

## Automated Evidence

Run from the repository root:

```bash
GOCACHE=$PWD/.gocache go test -count=1 ./...
GOCACHE=$PWD/.gocache go test -race -count=1 ./internal/auth ./internal/codex ./internal/codextest ./internal/server
GOCACHE=$PWD/.gocache go vet ./...
GOCACHE=$PWD/.gocache go build ./...
(cd website && npm ci && npm run build)
```

The focused regressions are:

| Criterion | Evidence |
| --- | --- |
| AC1 | `TestTokenSourceRefreshesExpiredTokenAndPersistsRotation`, `TestTokenSourceRefreshIsSingleFlight`, `TestOpenRefreshesExpiredAccessTokenBeforeDial`, `TestOpenRefreshesAndRedialsOnceAfterAuthHandshake` |
| AC2 | `TestOpenRefreshFailureReturnsAuthErrorWithoutRedial`, `TestPooledServiceReadinessFailsClosedAndSuppressesRepeatedUpstreamCalls`, `TestReadyFailsForZeroUsableClientsWithoutLeakingIdentity`, `TestDeploymentUsesAuthAwareReadinessAndWritableSeed`, `TestReleaseWorkflowInstallsRuntimeHardeningPatch` |
| AC3 | `TestBearerAuthRejectsWithoutUpstreamCall`, `TestChatCompletionsAuthErrorIsSanitized`, `TestChatCompletionsStreamingAuthErrorIncludesCircuitBreakCode` |
| AC4 | `TestPooledServiceReadinessFailsClosedAndSuppressesRepeatedUpstreamCalls`, `TestOpenCodexAuthCircuitFastFailsRepeatedGrowthWork`, `TestCodexAuthFailureOpensHTTPCircuitForSubsequentGrowthWork`; external Growth expiry drill in `teslashibe/smore` |
| AC5 | `website/docs/production-readiness.md` and successful docs build |

## Deployment Gate

The Kubernetes deployment must use `/health/live` for liveness and `/ready` for
readiness. Its init container must seed `CODEX_AUTH_JSON` from the read-only
Secret into a writable `emptyDir` with mode `0600`. Do not promote until a
Terra completion succeeds after an expiry simulation and the Growth circuit
breaker is verified in the caller repository.
