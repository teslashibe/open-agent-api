---
sidebar_position: 6
---

# Contributing

Thanks for poking at the code. Here’s how we usually work.

## Setup

You’ll want **Go 1.24+** (that’s what the Docker image builds with — see `go.mod`). Clone the repo and work from the module root.

## Before you open a PR

```bash
go test ./...
go vet ./...
go build ./...
```

If your Go cache isn’t writable in the sandbox you’re in:

```bash
GOCACHE=$PWD/.gocache go test ./...
GOCACHE=$PWD/.gocache go vet ./...
GOCACHE=$PWD/.gocache go build ./...
```

With the server on `127.0.0.1:8088`, a quick smoke check:

```bash
curl -s http://127.0.0.1:8088/health
curl -s http://127.0.0.1:8088/v1/models | jq .
```

## Docker

```bash
docker compose up --build -d
```

Ngrok overlay for Cursor BYOK:

```bash
NGROK_AUTHTOKEN=... docker compose -f docker-compose.yml -f docker-compose.ngrok.yml up -d
```

Compose mounts auth paths and agent-lock volumes. If you change behavior, keep `docker-compose.yml` / `docker-compose.ngrok.yml` and the docs in sync.

## CI and release

[`.github/workflows/docker.yml`](https://github.com/teslashibe/open-agent-api/blob/main/.github/workflows/docker.yml) does two jobs:

1. **Build and push** `ghcr.io/teslashibe/open-agent-api` on `main` and on `v*` tags.
2. **Bump the pin** in [`teslashibe/k8s-control`](https://github.com/teslashibe/k8s-control): `main` → dev `sha-<short>`; tag → prod `vX.Y.Z`. Flux picks it up from there.

Run the full validation suite before you cut a release tag.

## Docs

Keep **`website/docs/`** honest against:

- [`internal/openai/models.go`](https://github.com/teslashibe/open-agent-api/blob/main/internal/openai/models.go) — aliases and defaults
- [`README.md`](https://github.com/teslashibe/open-agent-api/blob/main/README.md) — behavior people actually rely on
- `docker-compose*.yml` — mounts, env defaults, ngrok overlay

If you add or rename a model alias, update the [Model catalog](./models/catalog) in the same PR.

## Secrets

Don’t commit `auth.json`, OAuth creds, `GATEWAY_BEARER_SECRET`, ngrok tokens, or k8s tokens. Production secrets belong in env vars or mounted Secrets — not in compose files checked into git. And think twice before binding host `~/.claude.json` into a container.

## Pull requests

- Keep PRs focused — one logical change when you can.
- Touch the model catalog when aliases, defaults, or `GATEWAY_PROVIDERS` behavior changes.
- If you change Agent queue, tool conversion, or streaming, say how you validated it with Cursor / a tunnel.
- Match the Go style already in `internal/` and `cmd/`.
