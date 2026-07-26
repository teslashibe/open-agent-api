---
sidebar_position: 6
---

# Contributing

## Development setup

- **Go 1.24+** (Docker image builds with Go 1.24; see `go.mod`).
- Clone the repo and run from the module root.

## Validate locally

Run before opening a PR or tagging a release:

```bash
go test ./...
go vet ./...
go build ./...
```

If your Go build cache is outside a writable sandbox:

```bash
GOCACHE=$PWD/.gocache go test ./...
GOCACHE=$PWD/.gocache go vet ./...
GOCACHE=$PWD/.gocache go build ./...
```

Live smoke checks with the server on `127.0.0.1:8088`:

```bash
curl -s http://127.0.0.1:8088/health
curl -s http://127.0.0.1:8088/v1/models | jq .
```

## Docker workflow

```bash
docker compose up --build -d
```

Ngrok overlay for Cursor BYOK:

```bash
NGROK_AUTHTOKEN=... docker compose -f docker-compose.yml -f docker-compose.ngrok.yml up -d
```

Compose mounts auth paths and agent-lock volumes — keep `docker-compose.yml` and `docker-compose.ngrok.yml` in sync with documented env vars when you change behavior.

## CI and release

GitHub Actions workflow [`.github/workflows/docker.yml`](https://github.com/teslashibe/codex-chat-api/blob/main/.github/workflows/docker.yml):

1. **Build and push** `ghcr.io/teslashibe/codex-chat-api` on pushes to `main` and version tags `v*`.
2. **Pin-bump** [`teslashibe/k8s-control`](https://github.com/teslashibe/k8s-control): `main` → dev manifest `sha-<short>`; tag → prod manifest `vX.Y.Z`. Flux watches k8s-control and applies the change.

Run the full validation suite before tagging a release.

## Documentation

Keep docs in **`website/docs/`** aligned with:

- [`internal/openai/models.go`](https://github.com/teslashibe/codex-chat-api/blob/main/internal/openai/models.go) — model aliases and defaults
- [`README.md`](https://github.com/teslashibe/codex-chat-api/blob/main/README.md) — behavior and configuration
- `docker-compose*.yml` — mount paths, env defaults, ngrok overlay

When you add or change model aliases, update [Model catalog](./models/catalog) in the same PR.

## Secrets and safety

- **Never commit** secrets: `auth.json`, OAuth creds, `GATEWAY_BEARER_SECRET`, ngrok tokens, or k8s tokens.
- Deliver production secrets via env vars or mounted k8s Secrets, not committed compose/manifest files.
- Do not bind host `~/.claude.json` into containers unless you understand the security implications.

## Pull request notes

- Prefer **focused changes** — one logical fix or feature per PR.
- Update the **model catalog** when aliases, defaults, or `GATEWAY_PROVIDERS` behavior changes.
- Note Cursor/tunnel validation when touching Agent queue, tool conversion, or streaming.
- Follow existing Go style and conventions in `internal/` and `cmd/`.
