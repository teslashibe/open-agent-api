---
sidebar_position: 4
title: Gemini / Antigravity
description: Sync Antigravity OAuth for Gemini 3.x and gateway models.
---

# Gemini / Antigravity auth

Prefer **Antigravity** OAuth for Gemini 3.x and Antigravity gateway model IDs. Plain Gemini CLI `~/.gemini/oauth_creds.json` still works for Gemini **2.5** when Antigravity creds are absent.

## Sync (macOS)

1. Log in with Antigravity / `agy` so the keyring item exists (`service=gemini`, `account=antigravity`).
2. From this repo:

```bash
scripts/sync-antigravity-auth.sh
```

Writes `~/.gemini/antigravity_oauth_creds.json` (mode `600`).

The script requires macOS `security` and a prior Antigravity login. Re-run when 3.x calls start failing auth.

## Configuration

| Setting | Env | Flag | Default |
| --- | --- | --- | --- |
| Auth path | `GEMINI_AUTH_PATH` | `--gemini-auth-path` | Antigravity file if present, else `~/.gemini/oauth_creds.json` |

## Docker

Compose default:

```text
GEMINI_AUTH_PATH=/home/codex/.gemini/antigravity_oauth_creds.json
```

Host `${HOME}/.gemini` is mounted writable so token refresh can update the file.

## Kubernetes

Secret key `GEMINI_OAUTH_JSON` is seeded to `/home/codex/.gemini/antigravity_oauth_creds.json` in an `emptyDir` HOME (writable for refresh; re-seed from Secret after pod restart if needed).

## Gateway model IDs

These public IDs look like Claude/GPT names but use the **Gemini / Antigravity** provider (not Claude Code):

- `claude-sonnet-4-6`
- `claude-opus-4-6-thinking`
- `gpt-oss-120b-medium`

They remain available when `GATEWAY_PROVIDERS=codex,gemini` (Claude Code disabled). Full tables: [Model catalog](../models/catalog).
