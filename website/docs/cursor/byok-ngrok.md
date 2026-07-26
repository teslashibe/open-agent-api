---
sidebar_position: 1
title: Cursor BYOK + ngrok
description: Point Cursor at the API with a pinned free ngrok channel running in the background.
---

# Cursor BYOK + ngrok

Use this API as a custom OpenAI-compatible endpoint for Cursor Chat, Cmd+K, and **Agent**. Tab autocomplete does not use custom endpoints.

## Why a tunnel

Try localhost first if your Cursor build allows it:

```text
http://127.0.0.1:8088/v1
```

Many BYOK paths route through Cursor-managed servers and fail with:

```text
Access to private networks is forbidden
```

or show an OpenAI key error **with no request in local server logs**. Then use a public HTTPS tunnel.

## Preferred: pinned free ngrok domain

Default reserved domain in this repo:

```text
YOUR_SUBDOMAIN.ngrok-free.dev
```

Override with `NGROK_DOMAIN` if you use your own reserved domain.

### Background via Docker (recommended for always-on)

```bash
# Auth first (see Auth docs), then:
NGROK_AUTHTOKEN=... docker compose -f docker-compose.yml -f docker-compose.ngrok.yml up -d
```

- API + ngrok restart with `unless-stopped`
- Inspector: [http://127.0.0.1:4041](http://127.0.0.1:4041)
- Cursor base URL: `https://YOUR_SUBDOMAIN.ngrok-free.dev/v1`

### Host ngrok (also fine)

With the API already on `127.0.0.1:8088`:

```bash
ngrok http --url=YOUR_SUBDOMAIN.ngrok-free.dev 8088
```

## Cursor settings

<!-- SCREENSHOT: Cursor Settings → Models — OpenAI API Key + Override OpenAI Base URL filled, model gpt-5.6-terra selected -->

| Field | Value |
| --- | --- |
| OpenAI API Key | `local-codex-chat-api` (any non-empty string) |
| Override OpenAI Base URL | `https://YOUR_SUBDOMAIN.ngrok-free.dev/v1` |
| Model | Exact public ID — see [catalog](../models/catalog) |

Recommended slugs to add in Cursor:

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

Open a **new** Agent chat after changing base URL / models (old chats can keep poisoned history).

Agent tool calling (Chat Completions `tools` / `delta.tool_calls` / continuations) is documented in [Cursor tool conventions](./tool-conventions).

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
| `Unauthorized User Openai API key` and **no server log** | Cursor never reached you | Non-empty dummy key; confirm `/v1/models` via tunnel |
| `Access to private networks is forbidden` | Localhost blocked | Use ngrok HTTPS base URL |
| `[error: upstream error]` on first Agent turn | Missing `codex login` / bad auth | Re-login; check server `stream_error` logs |
| `[error: upstream error]` in an **existing** chat | Poisoned history | Start a **new** Agent chat |
| Several Agent chats stall | Queue / concurrency | Keep queue on, `CODEX_AGENT_MAX_ACTIVE_PER_KEY=1` |

## Alternatives

`cloudflared tunnel --url http://127.0.0.1:8088` or `tailscale funnel 8088` also work; this repo standardizes on the pinned ngrok domain for a stable Cursor base URL.
