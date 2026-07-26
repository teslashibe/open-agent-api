---
sidebar_position: 3
title: Install on Kubernetes
description: Deploy open-agent-api as an in-cluster OpenAI-compatible gateway for your apps.
---

# Install on Kubernetes

This app repo does **not** ship Helm charts. Production manifests live in **[teslashibe/k8s-control](https://github.com/teslashibe/k8s-control)**.

> **Rename note:** GitOps paths/DNS still use the legacy name `codex-chat-api` (`manifests/base/codex-chat-api`, Service `codex-chat-api.smore.svc`). The container image is `ghcr.io/teslashibe/open-agent-api` (CI also publishes temporary `open-chat-api` + `codex-chat-api` aliases). A follow-up in k8s-control can rename resources to match.

The gateway is **internal only**: ClusterIP Service, no Ingress/Certificate, plus a NetworkPolicy that admits traffic only from allow-listed callers.

## What you get

| Resource | Role |
| --- | --- |
| Deployment `codex-chat-api` (legacy name) | Single replica gateway (namespace `smore`) |
| Service ClusterIP `:8088` | In-cluster DNS |
| NetworkPolicy | Ingress only from `smore-api` and Verum `api`/`scheduler` |
| Secret `codex-chat-api-secrets` | OAuth + gateway bearer (SOPS) |

**In-cluster base URL for apps (current GitOps name):**

```text
http://codex-chat-api.smore.svc.cluster.local:8088/v1
```

## Image and GitOps

- Image: `ghcr.io/teslashibe/open-agent-api` (plus legacy aliases `open-chat-api` and `codex-chat-api`)
- CI in this repo (`.github/workflows/docker.yml`) builds/pushes, then pin-bumps k8s-control:
  - push to `main` → `manifests/dev` tag `sha-<short>`
  - tag `v*` → `manifests/prod` tag `vX.Y.Z`
- Flux applies the pin from k8s-control

## Secrets

Secret name: `codex-chat-api-secrets` (namespace `smore`).

Real secrets are SOPS-encrypted in k8s-control:

`infrastructure/secrets/{dev,prod}/codex-chat-api-secrets.yaml`

Shape is documented in `manifests/base/codex-chat-api/secret.example.yaml`:

| Key | Contents |
| --- | --- |
| `CODEX_AUTH_JSON` | Contents of `~/.codex/auth.json` |
| `GEMINI_OAUTH_JSON` | Contents of `~/.gemini/antigravity_oauth_creds.json` |
| `CLAUDE_CODE_OAUTH_TOKEN` | Claude Code OAuth token (same as local compose `.env`) |
| `GATEWAY_BEARER_TOKEN` | `openssl rand -hex 32`; shared with callers |

An init container seeds Codex/Gemini files into a writable `emptyDir` HOME so OAuth refresh can rewrite creds. Claude auth is env-injected (not a file mount).

## Hardening env (deployment)

- `GATEWAY_BEARER_SECRET` — required bearer on `/v1/*`; `/health` stays open for probes
- `GATEWAY_PROVIDERS=codex,gemini,claude`
- `GEMINI_AUTH_PATH=/home/codex/.gemini/antigravity_oauth_creds.json`
- Single replica on purpose: agent queues protect shared upstream accounts; more replicas multiply concurrency against the same OAuth pools

## App integration

Callers should send:

```http
Authorization: Bearer <GATEWAY_BEARER_TOKEN>
```

to `.../v1/models` and `.../v1/chat/completions`.

In the smore stack:

- `CODEX_GATEWAY_URL` → `http://codex-chat-api.smore.svc.cluster.local:8088/v1`
- `CODEX_GATEWAY_BEARER` → same value as `GATEWAY_BEARER_TOKEN`

Optional tenant fairness: set header `X-Smore-Tenant-ID` so the agent queue keys by tenant.

## Verify

```bash
kubectl -n smore get deploy,svc,networkpolicy codex-chat-api

kubectl -n smore exec deploy/smore-api -- \
  curl -sf http://codex-chat-api.smore.svc.cluster.local:8088/health
# → {"status":"ok"}

kubectl -n smore exec deploy/smore-api -- sh -c \
  'curl -sf -H "Authorization: Bearer $CODEX_GATEWAY_BEARER" \
     http://codex-chat-api.smore.svc.cluster.local:8088/v1/models'
```

## Rotate credentials

```bash
codex login
# in open-agent-api checkout:
scripts/sync-antigravity-auth.sh
# refresh CLAUDE_CODE_OAUTH_TOKEN from a fresh claude login / .env

# in k8s-control:
sops infrastructure/secrets/<env>/codex-chat-api-secrets.yaml
git commit && git push
flux reconcile kustomization flux-system --with-source
kubectl -n smore rollout restart deploy/codex-chat-api
```

## Generic clusters

Copy/adapt `manifests/base/codex-chat-api` from k8s-control (legacy path): keep bearer auth, secret seeding, ClusterIP (or your own Ingress if you accept exposing it), and keep concurrency low against shared provider accounts.
