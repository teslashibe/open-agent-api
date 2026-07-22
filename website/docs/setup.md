---
title: Setup and credentials
sidebar_label: Setup and credentials
---

# Setup and credentials

## Prerequisites

- Go 1.23 or newer for a local gateway build.
- The Codex CLI and an authorized operator account.
- `curl` and `jq` for smoke tests.
- Node.js 20 or newer only when building this guide locally.

## Create a local Codex session

Run the interactive login on the operator host:

```bash
codex login
test -f "${CODEX_HOME:-$HOME/.codex}/auth.json"
```

`auth.json` contains live credentials. Do not print it, copy it into this
repository, include it in a container image, or paste it into logs. The gateway
reads the file at request time and atomically persists OAuth token rotation.

Start a local gateway bound to loopback:

```bash
go run ./cmd/codex-chat-api --host 127.0.0.1 --port 8088
```

In a second terminal, confirm liveness:

```bash
curl -fsS http://127.0.0.1:8088/health/live | jq .
```

Expected result:

```json
{"status":"ok"}
```

## Deliver credentials in a deployment

Mount each Codex login Secret read-only as an input, then use an init container
to copy it with mode `0600` into a per-client writable `emptyDir`. Point
`auth_path` at that runtime copy. The gateway refreshes access tokens five
minutes before JWT expiry and must be able to atomically replace `auth.json`.
Keep credentials outside ConfigMaps, manifests, shell history, and image layers.

```text
/var/run/codex-auth/work-a/auth.json
/var/run/codex-auth/work-b/auth.json
```

Do not share the writable file with another pod or a concurrently running Codex
CLI. Refresh tokens may rotate and require one writer. Keep one gateway replica,
or provision an independent login and writable volume per replica.

Set `GATEWAY_BEARER_SECRET` from the deployment platform's secret store. Do not
put its value in an environment file committed to Git. When set, the same bearer
secret protects `/v1/models`, `/v1/chat/completions`, and `/metrics`; health
endpoints remain unauthenticated for probes.

Keep the process on loopback for local use. In a cluster, expose it only through
the intended internal Service or gateway and apply the normal network policy.
Use a hostname under `example.invalid` in shared examples rather than recording
a real cluster or tunnel hostname.

Next, configure the [multi-credential pool](./multi-credential-pool.md).
