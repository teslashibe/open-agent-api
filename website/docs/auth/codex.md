---
sidebar_position: 2
title: Codex / ChatGPT
description: Authenticate the Codex upstream with codex login.
---

# Codex / ChatGPT auth

## Setup

```bash
codex login
```

That writes `~/.codex/auth.json`.

## Configuration

| Setting | Env | Flag | Default |
| --- | --- | --- | --- |
| Codex home | `CODEX_HOME` | `--codex-home` | `~/.codex` |
| Auth file | `CODEX_AUTH_PATH` | `--auth-path` | `<codex-home>/auth.json` |

On each request we load `auth.json` through a token source that:

1. Uses the access token while it is still valid (with a short expiry slack).
2. Silently refreshes via `https://auth.openai.com/oauth/token` when near expiry (or after a ChatGPT websocket `401`/`403`).
3. Persists the rotated `access_token` / `refresh_token` / `expires_at` / `last_refresh` back to `auth.json`.

If refresh fails with `invalid_grant` / revoked refresh token, run `codex login` again and re-seed the gateway secret.

## Docker

Compose mounts `${HOME}/.codex` → `/home/codex/.codex` **read-only** and sets `CODEX_HOME=/home/codex/.codex`. Prefer a writable mount in long-lived deployments so refresh can persist.

## Kubernetes

Secret key `CODEX_AUTH_JSON` is seeded by an init container into `/home/codex/.codex/auth.json` on a writable HOME volume (PVC in prod so rotated refresh tokens survive pod restarts). The init copies the secret when the live file is missing or the secret's `last_refresh` is newer.
