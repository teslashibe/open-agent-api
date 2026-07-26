---
sidebar_position: 1
title: Install
description: Run codex-chat-api locally or in Kubernetes.
---

# Install

Pick a path:

1. [Docker Compose](./docker) — local API on `127.0.0.1:8088`
2. [Kubernetes](./kubernetes) — in-cluster gateway for your apps (manifests in k8s-control)

You can also run without Docker:

```bash
go run ./cmd/codex-chat-api --host 127.0.0.1 --port 8088
```

Complete [auth](../auth/overview) before expecting upstream completions. For Cursor, continue with [BYOK + ngrok](../cursor/byok-ngrok).
