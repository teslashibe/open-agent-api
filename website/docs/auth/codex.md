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

This writes `~/.codex/auth.json`.

## Configuration

| Setting | Env | Flag | Default |
| --- | --- | --- | --- |
| Codex home | `CODEX_HOME` | `--codex-home` | `~/.codex` |
| Auth file | `CODEX_AUTH_PATH` | `--auth-path` | `<codex-home>/auth.json` |

Tokens are read fresh from `auth.json` on every request. On `401`, run `codex login` again.

## Docker

Compose mounts `${HOME}/.codex` → `/home/codex/.codex` **read-only** and sets `CODEX_HOME=/home/codex/.codex`.

## Kubernetes

Secret key `CODEX_AUTH_JSON` is seeded by an init container to `/home/codex/.codex/auth.json` inside a writable HOME volume.
