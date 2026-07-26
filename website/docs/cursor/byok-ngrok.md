---
sidebar_position: 1
title: Cursor BYOK + ngrok
description: Point Cursor at the API over a public HTTPS tunnel — localhost won’t work.
---

# Cursor BYOK + ngrok

Use this API as a custom OpenAI-compatible endpoint for Cursor Chat, Cmd+K, and **Agent**. Tab autocomplete ignores custom endpoints.

## Localhost doesn’t work

Cursor BYOK goes through Cursor’s cloud. Private addresses get rejected:

```text
Access to private networks is forbidden
```

Don’t set the base URL to `http://127.0.0.1:8088/v1` or `http://localhost:8088/v1`. You’ll often see an OpenAI key error **and nothing in your local server logs** — Cursor never reached you.

You need a public **HTTPS** tunnel. This repo’s Compose overlay assumes a free ngrok domain you reserve yourself.

## Prerequisites

1. [Clone, auth, and start the API](../install/) (Compose or `go run`).
2. An [ngrok authtoken](https://dashboard.ngrok.com/get-started/your-authtoken).

Confirm the API locally first (curl only):

```bash
curl -s http://127.0.0.1:8088/health
```

## Use your own free ngrok domain

Reserve a free domain in the [ngrok dashboard](https://dashboard.ngrok.com/), then set `NGROK_DOMAIN` to that hostname — for example:

```text
YOUR_SUBDOMAIN.ngrok-free.dev
```

Please don’t commit a personal reserved domain into this repo.

### Background via Docker (recommended)

```bash
NGROK_AUTHTOKEN=... docker compose -f docker-compose.yml -f docker-compose.ngrok.yml up -d
```

- API + ngrok restart with `unless-stopped`
- Inspector: [http://127.0.0.1:4041](http://127.0.0.1:4041)
- Cursor base URL: `https://YOUR_SUBDOMAIN.ngrok-free.dev/v1`

### Host ngrok

If the API is already on `127.0.0.1:8088`:

```bash
ngrok http --url=YOUR_SUBDOMAIN.ngrok-free.dev 8088
```

## Cursor settings

<!-- SCREENSHOT: Cursor Settings → Models — OpenAI API Key + Override OpenAI Base URL filled, model gpt-5.6-terra selected -->

| Field | Value |
| --- | --- |
| OpenAI API Key | `local-open-agent-api` (any non-empty string) |
| Override OpenAI Base URL | `https://YOUR_SUBDOMAIN.ngrok-free.dev/v1` |
| Model | Exact public ID — see [catalog](../models/catalog) |

Again: no `http://127.0.0.1…` or `http://localhost…` base URL.

Handy slugs to add in Cursor:

```text
gpt-5.6-terra
gpt-5.6-sol-high
gpt-5.6-luna-fast
gemini-3.1-flash-lite
gemini-3.1-pro-high
claude-sonnet-4-6
haiku
sonnet
opus
fable
gpt-5.3-codex-spark
```

| Use case | Model ID |
| --- | --- |
| Everyday Agent | `gpt-5.6-terra` |
| Hard tasks | `gpt-5.6-sol-high` |
| Fast Codex | `gpt-5.6-luna-fast` |
| Fastest cheap turn | `gemini-3.1-flash-lite` |
| Claude Code fast | `haiku` |

After changing the base URL or models, open a **new** Agent chat — old ones can keep poisoned history.

How Agent tool calling works over the wire: [Cursor tool conventions](./tool-conventions).

## Validate the tunnel

```bash
curl -s https://YOUR_SUBDOMAIN.ngrok-free.dev/health
curl -s https://YOUR_SUBDOMAIN.ngrok-free.dev/v1/models | jq '.data[].id'
```

In a new Agent chat:

```text
List the files in this repo.
```

You should see real tool execution and matching `POST /v1/chat/completions` lines in `docker compose logs -f api`.

## Troubleshooting

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| `Access to private networks is forbidden` | Localhost / private base URL | Use the ngrok `https://…/v1` URL |
| `Unauthorized User Openai API key` and **no server log** | Cursor never reached you | Non-empty dummy key; confirm `/v1/models` via the tunnel |
| `[error: upstream error]` on first Agent turn | Missing `codex login` / bad auth | Re-login; check server `stream_error` logs |
| `[error: upstream error]` in an **existing** chat | Poisoned history | Start a **new** Agent chat |
| Several Agent chats stall | Queue / concurrency | Keep the queue on, `CODEX_AGENT_MAX_ACTIVE_PER_KEY=1` |

## Alternatives

`cloudflared tunnel --url http://127.0.0.1:8088` or `tailscale funnel 8088` also work if they give you public HTTPS. We document ngrok because a reserved free domain keeps the Cursor base URL stable.
