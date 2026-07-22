---
title: Dev validation
sidebar_label: Dev validation
---

# Dev validation

Run these checks against the dev deployment after `main` updates its
`sha-<short>` pin. The examples use loopback; when validating remotely, set
`API_URL` to an approved endpoint and keep the real hostname out of transcripts.

```bash
export API_URL=http://127.0.0.1:8088
curl -fsS "$API_URL/health/live" | jq .
curl -fsS "$API_URL/health/ready" | jq .
```

Both endpoints return `200` during normal service. Liveness never calls an
upstream provider. Readiness reflects the local drain flag and locally known
Codex credential health; it returns `503` when draining or when no configured
Codex client is usable. It never probes the upstream provider.

## Models and a completion

For an unprotected local gateway:

```bash
curl -fsS "$API_URL/v1/models" | jq '.data[].id'
curl -fsS "$API_URL/v1/chat/completions" \
  -H 'content-type: application/json' \
  -d '{
    "model":"gpt-5.5",
    "messages":[{"role":"user","content":"Reply with: dev smoke test passed"}]
  }' | jq .
```

For a protected deployment, obtain the bearer value from the approved secret
manager without printing it, then add this header to `/v1` and `/metrics`
requests:

```bash
-H "authorization: Bearer ${GATEWAY_BEARER_SECRET:?not set}"
```

## Readiness and drain

Drain controls accept only a connection whose remote address is loopback. They
ignore forwarded-client headers. Run them inside the pod or host network
namespace, not through a public gateway:

```bash
curl -fsS -X POST http://127.0.0.1:8088/drain/start | jq .
curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8088/health/ready
curl -fsS -X POST http://127.0.0.1:8088/drain/stop | jq .
curl -fsS http://127.0.0.1:8088/health/ready | jq .
```

The status sequence is `200`, `503`, `200`, `200`. While draining, new chat
requests receive `503 server draining`; already admitted requests can finish.

## Metrics

Metrics are enabled by default. For an unprotected local gateway:

```bash
curl -fsS "$API_URL/metrics" | grep '^codex_chat_api_'
```

Check these bounded operational signals:

| Metric | What to validate |
| --- | --- |
| `codex_chat_api_requests_total` | Final request results by provider. |
| `codex_chat_api_rate_limit_responses_total` | Final `429` classes. |
| `codex_chat_api_pool_selections_total` | Normal, rotated, fallback, and pinned selections. |
| `codex_chat_api_pool_cooldowns_total` | Cooldowns by safe client label and failure class. |
| `codex_chat_api_pool_cooldown_skips_total` | Attempts that skipped cooling clients. |
| `codex_chat_api_queue_wait_seconds` | Queue admission latency and terminal result. |
| `codex_chat_api_active_streams` | Current downstream streams by provider. |

Metrics must not contain bearer values, tenant IDs, request IDs, prompts, auth
paths, or raw conversation keys. Set `CODEX_METRICS_ENABLED=false` only when the
deployment deliberately removes the route.

Record the deployed image digest, `sha-<short>` pin, check timestamps, and
pass/fail results. Redact hosts, headers, identifiers, and response content
before attaching evidence to an issue.

After these smoke checks, run the documented 30-minute soak and single-replica
canary in the [production-readiness runbook](./production-readiness.md#dev-deploy-and-soak).
