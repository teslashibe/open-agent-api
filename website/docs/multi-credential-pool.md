---
title: Multi-credential pool
sidebar_label: Multi-credential pool
---

# Multi-credential pool

Without `CODEX_CLIENTS`, the gateway uses one client from `CODEX_HOME` and
`CODEX_AUTH_PATH`. For multiple independent logins, pass a JSON array whose
paths point to mounted secrets:

```bash
export CODEX_CLIENTS='[
  {"label":"pool-a","codex_home":"/run/codex/pool-a","auth_path":"/run/secrets/codex-pool-a/auth.json"},
  {"label":"pool-b","codex_home":"/run/codex/pool-b","auth_path":"/run/secrets/codex-pool-b/auth.json"}
]'
export CODEX_CLIENT_MAX_INFLIGHT=2
export CODEX_CLIENT_POOL_UNAVAILABLE=fail
export CODEX_CLIENT_COOLDOWN_DEFAULT=5m
```

The labels are bounded metric and log dimensions, not account names. Use only
non-sensitive aliases made from letters, digits, `_`, `.`, or `-` (at most 64
characters). Never use an email address, username, token fragment, auth path, or
tenant identifier as a label.

## Balancing behavior

- The gateway hashes the Agent queue/conversation key for deterministic client
  affinity. Repeated turns stay on one client; selection is not random.
- Each client has an inflight lease capped by `CODEX_CLIENT_MAX_INFLIGHT`. If the
  preferred client is full, the pool checks the other clients in deterministic
  order. If all healthy clients are full, it returns HTTP `429` with
  `codex client pool saturated`.
- A quota or rate-limit failure before the first stream event cools the client
  using an upstream reset hint or `CODEX_CLIENT_COOLDOWN_DEFAULT`. The request is
  tried once on another healthy client.
- After a rotated request succeeds, the conversation is soft-pinned to that
  replacement. A failure after output starts never switches clients mid-stream.
- Cooldowns, inflight leases, and soft pins are process-local. Soft pins expire
  after 24 hours and use a bounded least-recently-used cache.

Use `CODEX_CLIENT_POOL_UNAVAILABLE=fail` for the narrow failure behavior.
`fallback_first` additionally retries the first configured client for other
startup failures from a non-primary client and should be enabled only for a
known compatibility requirement.

For multiple replicas, point `CODEX_AGENT_QUEUE_LOCK_DIR` at the same writable
shared volume and keep `CODEX_AGENT_MAX_ACTIVE_PER_KEY=1`. This prevents the
same conversation from streaming concurrently through separate processes.

Selection, rotation, cooldown, and saturation signals are visible through the
[metrics and dev checks](./dev-validation.md) without exposing credentials.
