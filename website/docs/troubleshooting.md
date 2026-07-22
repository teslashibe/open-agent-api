---
title: Troubleshooting
sidebar_label: Troubleshooting
---

# Troubleshooting

## Authentication failures

- `invalid_gateway_credentials` means `GATEWAY_BEARER_SECRET` is enabled and
  the request did not provide the matching bearer. No upstream call occurred.
- `upstream_authentication_failed` means Codex OAuth refresh or the ChatGPT
  WebSocket authentication failed. Pause judge/draft/outbox work with backoff;
  do not retry every minute. Check `/ready` and the client health metrics.
- The gateway refreshes an expiring JWT before dialing and retries one 401/403
  handshake after a forced refresh. A persistent failure usually means the
  refresh token was revoked, `auth_path` is wrong, or the runtime file is not
  writable. Run `codex login` through the approved operator flow, replace the
  seeded Secret and writable runtime copy, and retry without printing either.
- If one pool client fails authentication before output, the pool rotates once
  to another selectable client. Persistent rotation is a credential incident,
  not a reason to increase concurrency.

## Pool saturation or cooldown

`429 codex client pool saturated` means every healthy client is at
`CODEX_CLIENT_MAX_INFLIGHT`; it does not open another upstream request. Retry
with backoff, inspect active-stream and selection metrics, and raise limits only
after observing account and provider capacity.

Quota/rate-limit responses cool a client before its first output and can rotate
the request once. Check `codex_chat_api_pool_cooldowns_total` and
`codex_chat_api_pool_cooldown_skips_total`. A midstream failure does not rotate,
because switching accounts after partial content would corrupt the response.

## Readiness or drain failures

- `GET /health/live` should remain `200` while the process is running, even
  during an upstream outage.
- `GET /ready` (`/health/ready` compatibility alias) returns `503` while the
  local drain flag is set or when no
  Codex client is locally usable. Run `POST /drain/stop` from loopback for an
  abandoned drain; rotate a rejected credential for an `unavailable` response.
- A remote drain request returns `404` by design. Execute it inside the pod or
  host network namespace; `X-Forwarded-For` cannot bypass the restriction.

## Metrics failures

- A `404` from `/metrics` means `CODEX_METRICS_ENABLED=false` or the route is
  unavailable in the running version.
- A `401` means the gateway bearer secret also protects metrics. Configure the
  scrape from a mounted secret file rather than an inline token.
- Never add tenant IDs, prompts, request IDs, or credentials as metric labels.
  Client labels must remain safe operational aliases.

## Pipeline failures

- If image publication fails, fix that job first; the deploy job is correctly
  blocked by its dependency.
- If pin replacement says the image was not found, verify that the expected
  public image entry is still present in the target kustomization. Do not loosen
  the check to replace arbitrary image text.
- If the GitOps push is rejected, verify `GITOPS_TOKEN` scope and branch policy.
  Do not replace it with a personal credential in the workflow.
- If Pages assets return `404`, verify `url` is
  `https://teslashibe.github.io`, `baseUrl` is `/codex-chat-api/`, and Pages is
  configured to deploy with GitHub Actions.

Before sharing logs, remove hostnames, bearer headers, OAuth material, account
identifiers, request metadata, and prompt/response bodies.

For alert-by-alert diagnosis, credential rotation, and rollback, use the
[production-readiness runbook](./production-readiness.md#alert-response).
