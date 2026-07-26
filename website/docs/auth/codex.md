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

We re-read `auth.json` on every request. If you start seeing `401`s, run `codex login` again.

## Docker

Compose mounts `${HOME}/.codex` → `/home/codex/.codex` **read-only** and sets `CODEX_HOME=/home/codex/.codex`.

## Kubernetes

Secret key `CODEX_AUTH_JSON` is seeded by an init container into `/home/codex/.codex/auth.json` on a writable HOME volume.
