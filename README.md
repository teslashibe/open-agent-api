# Codex Chat API

A small OpenAI-compatible Go HTTP API that proxies chat requests to the Codex
backend (`wss://chatgpt.com/backend-api/codex/responses`) using the ChatGPT OAuth
token stored by `codex login`. No `sk-` API key is needed.

> Reverse-engineered from `codex_cli_rs` 0.144.1. Use responsibly and within
> OpenAI's terms.

## Requirements

- Go 1.23 or newer.
- A Codex login session with `~/.codex/auth.json` present:

```bash
codex login
```

The old Python prototype has been removed. The Go server is the supported runtime
(current release: `v0.0.5`).

## Quick Start (Cursor)

1. Log in to Codex and start the API locally:

```bash
codex login
go run ./cmd/codex-chat-api --host 127.0.0.1 --port 8088
```

2. Expose it with ngrok (required for most Cursor BYOK paths):

```bash
NGROK_AUTHTOKEN=... docker compose -f docker-compose.yml -f docker-compose.ngrok.yml up -d
```

Default reserved domain: `icebound-melida-unstoppably.ngrok-free.dev` (override with `NGROK_DOMAIN`).

3. In **Cursor → Settings → Models**, enable the OpenAI API key override:

| Field | Value |
| --- | --- |
| OpenAI API Key | `local-codex-chat-api` (any non-empty string) |
| Override OpenAI Base URL | `https://icebound-melida-unstoppably.ngrok-free.dev/v1` |
| Model | `gpt-5.6-sol`, `gpt-5.6-terra`, `gpt-5.6-luna`, or an effort alias such as `gpt-5.6-sol-high` |

4. Open a **new** Agent chat and try:

```text
List the files in this repo.
```

Cursor Agent should emit tool calls, execute them locally, and return a final
answer based on real repo data.

See [Cursor compatibility](#cursor-compatibility) for Ask vs Agent mode, tunnel
options, troubleshooting, and validation prompts.

## Run

```bash
go run ./cmd/codex-chat-api --host 127.0.0.1 --port 8088
```

Then verify the server is reachable:

```bash
curl -s http://127.0.0.1:8088/health
```

Expected response:

```json
{"status":"ok"}
```

## Docker Compose

Run the API:

```bash
docker compose up --build -d
```

Expose it for Cursor with ngrok (preferred over Cloudflare quick tunnels):

```bash
# Host ngrok (uses ~/.ngrok/ngrok.yml) — recommended
ngrok http --url=icebound-melida-unstoppably.ngrok-free.dev 8088

# Or docker ngrok overlay (requires NGROK_AUTHTOKEN)
NGROK_AUTHTOKEN=... docker compose -f docker-compose.yml -f docker-compose.ngrok.yml up -d
```

Cursor base URL: `https://icebound-melida-unstoppably.ngrok-free.dev/v1`

The Docker Compose service enables the Agent queue by default. Tool-capable
Cursor Agent requests use `CODEX_AGENT_QUEUE_KEY_MODE=cursor`
with `CODEX_AGENT_MAX_ACTIVE=2` and `CODEX_AGENT_MAX_ACTIVE_PER_KEY=1`, while
Ask/text-only requests bypass the queue.

## Validate

Run the full local validation suite before release:

```bash
go test ./...
go vet ./...
go build ./...
```

If your Go build cache is outside a writable sandbox, set it inside the repo:

```bash
GOCACHE=$PWD/.gocache go test ./...
GOCACHE=$PWD/.gocache go vet ./...
GOCACHE=$PWD/.gocache go build ./...
```

## API

The server implements:

- `GET /health` (live alias)
- `GET /health/live`
- `GET /health/ready`
- `POST /drain/start` (localhost-only)
- `POST /drain/stop` (localhost-only)
- `GET /v1/models`
- `POST /v1/chat/completions`

### Model Discovery

```bash
curl -s http://127.0.0.1:8088/v1/models | jq .
```

Expected response:

```json
{
  "object": "list",
  "data": [
    {
      "id": "gpt-5.5",
      "object": "model",
      "created": 0,
      "owned_by": "codex-chat-api"
    },
    {
      "id": "gpt-5.5-low",
      "object": "model",
      "created": 0,
      "owned_by": "codex-chat-api"
    },
    {
      "id": "gpt-5.5-high",
      "object": "model",
      "created": 0,
      "owned_by": "codex-chat-api"
    },
    {
      "id": "gpt-5.5-fast",
      "object": "model",
      "created": 0,
      "owned_by": "codex-chat-api"
    }
  ]
}
```

Model aliases are resolved server-side before the upstream Codex request.
GPT-5.6 ChatGPT/Codex context is ~272K tokens (not the API card's 1.05M).
See [`docs/gpt-5.6-sol-terra-luna.md`](docs/gpt-5.6-sol-terra-luna.md) and
[`docs/specs/gpt-5.6-cursor-models.md`](docs/specs/gpt-5.6-cursor-models.md).

| Public model ID | Upstream model | Reasoning effort | Verbosity |
| --- | --- | --- | --- |
| `gpt-5.6-sol` | `gpt-5.6-sol` | `low` | `low` |
| `gpt-5.6` | `gpt-5.6-sol` | `medium` | `low` |
| `gpt-5.6-sol-high` | `gpt-5.6-sol` | `high` | `low` |
| `gpt-5.6-terra` | `gpt-5.6-terra` | `medium` | `low` |
| `gpt-5.6-luna` / `gpt-5.6-luna-fast` | `gpt-5.6-luna` | `medium` / `low` | `low` |
| `codex-sol` / `codex-terra` / `codex-luna` | matching 5.6 tier | (tier default) | `low` |
| `gpt-5.5` | `gpt-5.5` | `medium` | `medium` |
| `gpt-5.5-high` | `gpt-5.5` | `high` | `medium` |
| `gemini-2.5-flash` / `flash-lite` / `pro` | same | `medium` | `medium` |
| `gemini-3.1-pro-low` | `gemini-3.1-pro-low` | `medium` | `medium` |
| `gemini-3.1-pro-high` | `gemini-pro-agent` | `medium` | `medium` |
| `gemini-3.5-flash-{low,medium,high}` | Antigravity wire IDs | `medium` | `medium` |
| `claude-sonnet-4-6` / `claude-opus-4-6-thinking` / `gpt-oss-120b-medium` | same (via Antigravity) | `medium` | `medium` |

All three tiers expose `-xhigh` and `-max` effort aliases. `ultra` is a Codex
product multi-agent mode (not a `reasoning.effort` value) and is not aliased.

`gpt-5.3-codex-spark` (+ `-preview`) is listed for overflow / Cursor aliases
(96 KiB hard context). Note: faithful Codex turns may inject `image_generation`,
which Spark rejects — use `faithful:false` or client `tools` for plain chat.

Antigravity Gemini 3.x requires Antigravity OAuth. Sync from the host keyring
before Docker reload:

```bash
scripts/sync-antigravity-auth.sh
```

That writes `~/.gemini/antigravity_oauth_creds.json` (compose sets
`GEMINI_AUTH_PATH` to the mounted copy).

### Non-Streaming Chat

```bash
curl -s http://127.0.0.1:8088/v1/chat/completions \
  -H 'authorization: Bearer local-codex-chat-api' \
  -H 'content-type: application/json' \
  -d '{
    "model": "gpt-5.5",
    "messages": [
      {"role": "user", "content": "Say hi in 5 words"}
    ]
  }' | jq .
```

### Streaming Chat

```bash
curl -N http://127.0.0.1:8088/v1/chat/completions \
  -H 'content-type: application/json' \
  -d '{
    "model": "gpt-5.5",
    "stream": true,
    "messages": [
      {"role": "user", "content": "Count to 5"}
    ]
  }'
```

Streaming responses use OpenAI-compatible SSE chunks and end with:

```text
data: [DONE]
```

## OpenAI SDK Usage

Point an OpenAI-compatible client at the local Go server and provide any dummy API
key value.

```python
from openai import OpenAI

client = OpenAI(base_url="http://127.0.0.1:8088/v1", api_key="not-needed")

response = client.chat.completions.create(
    model="gpt-5.5",
    messages=[{"role": "user", "content": "Hello!"}],
)

print(response.choices[0].message.content)
```

For streaming:

```python
from openai import OpenAI

client = OpenAI(base_url="http://127.0.0.1:8088/v1", api_key="not-needed")

for event in client.chat.completions.create(
    model="gpt-5.5",
    stream=True,
    messages=[{"role": "user", "content": "Count to 5"}],
):
    delta = event.choices[0].delta.content
    if delta:
        print(delta, end="", flush=True)
```

## Configuration

Configuration is loaded from `.env`, environment variables, and command-line flags.
Flags override environment values.

| Setting | Environment | Flag | Default |
| --- | --- | --- | --- |
| Bind host | `CODEX_CHAT_API_HOST` or `HOST` | `--host` | `127.0.0.1` |
| Bind port | `CODEX_CHAT_API_PORT` or `PORT` | `--port` | `8088` |
| Codex home directory | `CODEX_HOME` | `--codex-home` | `~/.codex` |
| Auth file path | `CODEX_AUTH_PATH` | `--auth-path` | `<codex-home>/auth.json` |
| Codex profile JSON | `CODEX_PROFILE_PATH` | `--codex-profile` | `codex_profile.json` |
| Codex scaffold JSON | `CODEX_SCAFFOLD_PATH` | `--codex-scaffold` | `codex_scaffold.json` |
| Codex websocket URL | `CODEX_WEBSOCKET_URL` | `--codex-websocket-url` | `wss://chatgpt.com/backend-api/codex/responses` |
| Codex request timeout | `CODEX_TIMEOUT` | `--codex-timeout` | `120s` |
| Codex client pool | `CODEX_CLIENTS` | `--codex-clients` | single client from `CODEX_HOME` / `CODEX_AUTH_PATH` |
| Codex pool unavailable policy | `CODEX_CLIENT_POOL_UNAVAILABLE` | `--codex-client-pool-unavailable` | `fail` |
| Codex client cooldown default | `CODEX_CLIENT_COOLDOWN_DEFAULT` | `--codex-client-cooldown-default` | `5m` |
| Redacted body-shape logging | `CODEX_LOG_BODY_SHAPE` | `--log-body-shape` | `false` |
| Redacted request identity logging | `CODEX_LOG_REQUEST_IDENTITY` | `--log-request-identity` | `false` |
| Redacted Codex tool-event logging | `CODEX_LOG_CODEX_TOOL_EVENTS` | `--log-codex-tool-events` | `false` |
| Agent queue enabled | `CODEX_AGENT_QUEUE_ENABLED` | `--agent-queue-enabled` | `true` |
| Agent max active requests | `CODEX_AGENT_MAX_ACTIVE` | `--agent-max-active` | `2` |
| Agent max active per key | `CODEX_AGENT_MAX_ACTIVE_PER_KEY` | `--agent-max-active-per-key` | `1` |
| Agent queue key mode | `CODEX_AGENT_QUEUE_KEY_MODE` | `--agent-queue-key-mode` | `cursor` |
| Agent queue waiting limit | `CODEX_AGENT_QUEUE_LIMIT` | `--agent-queue-limit` | `20` |
| Agent queue wait timeout | `CODEX_AGENT_QUEUE_TIMEOUT` | `--agent-queue-timeout` | `5m` |
| Agent queue shared lock directory | `CODEX_AGENT_QUEUE_LOCK_DIR` | `--agent-queue-lock-dir` | disabled; set explicitly for multi-replica or multi-client pools |
| Agent queue priority experiment | `CODEX_AGENT_QUEUE_PRIORITY_ENABLED` | `--agent-queue-priority-enabled` | `false` |
| Context management enabled | `CODEX_CONTEXT_MANAGEMENT_ENABLED` | `--context-management-enabled` | `true` |
| Context max bytes | `CODEX_CONTEXT_MAX_BYTES` | `--context-max-bytes` | `196608` |
| Context max messages | `CODEX_CONTEXT_MAX_MESSAGES` | `--context-max-messages` | `120` |
| Context recent messages kept | `CODEX_CONTEXT_RECENT_MESSAGES` | `--context-recent-messages` | `24` |
| Tool output max bytes | `CODEX_CONTEXT_TOOL_OUTPUT_MAX_BYTES` | `--context-tool-output-max-bytes` | `32768` |
| Compacted tool output max bytes | `CODEX_CONTEXT_COMPACTED_TOOL_OUTPUT_MAX_BYTES` | `--context-compacted-tool-output-max-bytes` | `512` |
| Gateway bearer secret | `GATEWAY_BEARER_SECRET` | `--gateway-bearer-secret` | empty (inbound auth disabled) |
| Gateway provider allowlist | `GATEWAY_PROVIDERS` | `--gateway-providers` | `codex,gemini,claude` |
| Gateway tenant header | `GATEWAY_TENANT_HEADER` | `--gateway-tenant-header` | `X-Smore-Tenant-ID` |

### Internal gateway / smore deployment

When this service runs as an internal free-tier backend (behind smore-api),
harden it with:

```bash
GATEWAY_BEARER_SECRET=<shared-secret>   # required bearer on /v1 routes
GATEWAY_PROVIDERS=codex,gemini          # drop claude from routing and /v1/models
```

- **Bearer auth.** When `GATEWAY_BEARER_SECRET` is set, `/v1/models` and
  `/v1/chat/completions` require `Authorization: Bearer <secret>`; anything
  else gets a `401` with an OpenAI-style `authentication_error` body and no
  upstream call is made. `/health` stays unauthenticated for k8s probes. When
  the secret is unset (the dev default), any Authorization value passes
  through, so the local Cursor workflow — including its BYOK model discovery
  against `/v1/models` — is unchanged.
- **Provider allowlist.** `GATEWAY_PROVIDERS` is a comma-separated subset of
  `codex,gemini,claude`; `codex` is mandatory (it is the router fallback).
  Disabled providers are removed from `/v1/models`, their models return
  `404 model not found` before any queueing, and their upstream clients
  (including the `claude` CLI) are never constructed.
- **Tenant queueing.** When a request carries the header named by
  `GATEWAY_TENANT_HEADER` (default `X-Smore-Tenant-ID`), agent queue affinity
  keys by that tenant id instead of the Cursor-session heuristics, so tenants
  share the upstream fairly. Callers behind smore should always set it.
- **Concurrency stays small on purpose.** The defaults
  (`CODEX_AGENT_MAX_ACTIVE=2`, `CODEX_AGENT_MAX_ACTIVE_PER_KEY=1`) protect the
  shared ChatGPT/Gemini operator accounts; raise them deliberately, not as
  part of enabling the gateway.
- Deliver the secret via a k8s Secret (env var or mounted file sourced into
  the env), not a committed compose/manifest file. The request logger only
  records `authorization_present=true/false`, never the header value.

### Health, readiness, and drain

Health is split so rollouts can drain a pod without killing it for upstream
outages. All health endpoints are unauthenticated so k8s probes reach them.

| Endpoint | Meaning |
| --- | --- |
| `GET /health` | Live alias — `200` whenever the process is up (unchanged body `{"status":"ok"}`). |
| `GET /health/live` | Liveness — `200` whenever the process is up. |
| `GET /health/ready` | Readiness — `200` when serving, `503 {"status":"draining"}` while draining. |
| `POST /drain/start` | Localhost-only. Begin draining; readiness flips to `503`. |
| `POST /drain/stop` | Localhost-only. Resume serving. |

- **Live never depends on upstream ChatGPT.** If OpenAI blips, `/health/live`
  (and `/health`) stay `200` so the pod is not restarted for an upstream
  outage. Readiness likewise never pings chatgpt.com; it only reflects the
  local drain flag.
- **Draining rejects new work, drains in-flight.** While draining, new
  `POST /v1/chat/completions` requests return `503` (`server draining`) *before*
  any upstream call, while requests already past that check finish normally.
- **Drain is localhost-only.** `/drain/start` and `/drain/stop` only act for a
  loopback connection remote address (`127.0.0.1`/`::1`); any other caller gets
  a `404` and the drain state is left untouched. The check uses the connection
  remote IP and does **not** honor `X-Forwarded-For`, so a fronting proxy
  cannot trigger a drain.
- **Probe mapping (compat).** Existing k8s probes hit `/health`, which maps to
  **live**, so no probe change is required for this release. Draining only
  gates Service routing once a manifest adopts a `/health/ready` readiness
  probe — that k8s-control manifest tweak is a follow-up and out of scope here.

Request body options beyond the core OpenAI chat schema:

| Field | Values | Default |
| --- | --- | --- |
| `reasoning_effort` | `low`, `medium`, `high` | selected model alias, otherwise `medium` |
| `verbosity` | `low`, `medium`, `high` | selected model alias, otherwise `medium` |
| `faithful` | `true`, `false` | `true` when the client does not send `tools`; otherwise `false` |
| `prewarm` | `true`, `false` | follows `faithful` |

When the client sends `tools` (Cursor Agent always does), the server automatically
uses **minimal mode**: no faithful CLI profile injection and no prewarm turn.
This avoids upstream Codex errors from conflicting tool definitions. Explicit
`reasoning_effort`, `verbosity`, `faithful`, and `prewarm` request fields still
override the defaults.

## Cursor Compatibility

Cursor can use this API as a custom OpenAI-compatible endpoint for **Chat**,
**Cmd+K**, and **Agent** mode. Cursor Tab autocomplete does not use custom
endpoints.

### What is supported

| Feature | Status |
| --- | --- |
| `GET /health` | Supported (live alias) |
| `GET /health/live`, `GET /health/ready` | Supported (liveness / readiness split for rollouts) |
| `GET /v1/models` | Supported (returns GPT-5.6 / 5.5 / Spark / Claude / Gemini aliases) |
| `POST /v1/chat/completions` (non-streaming) | Supported |
| `POST /v1/chat/completions` (streaming SSE) | Supported |
| Cursor Ask mode (text only) | Supported |
| Cursor Agent mode (tool calls) | Supported (`v0.0.4+`) |
| Tool-call streaming (`delta.tool_calls`) | Supported |
| Tool-result continuation (`role:"tool"`) | Supported |
| `POST /v1/responses` | Not implemented |
| Cursor Tab autocomplete | Not expected to work |

### Cursor settings

Cursor requires a non-empty OpenAI API key even though this API ignores it.
Upstream Codex authentication comes from `~/.codex/auth.json` (`codex login`).

```text
OpenAI API Key:        local-codex-chat-api
Override Base URL:     https://<tunnel-host>/v1
Model:                 gpt-5.6-sol-high
```

The model ID is exact and case-sensitive. Prefer `gpt-5.6-terra` for everyday
work, `gpt-5.6-sol` / `gpt-5.6-sol-high` for hard tasks, and `gpt-5.6-luna-fast`
for quick turns. Legacy `gpt-5.5*` aliases remain available.

### Localhost vs HTTPS tunnel

Try localhost first if your Cursor build routes BYOK requests directly:

```text
http://127.0.0.1:8088/v1
http://localhost:8088/v1
```

Many Cursor BYOK paths route through Cursor-managed servers. In that case
localhost fails with errors like:

```text
Access to private networks is forbidden
```

or Cursor reports an OpenAI API key error **without any request appearing in
these server logs**. When that happens, expose the local API with a public HTTPS
tunnel and point Cursor at the tunnel URL instead.

### HTTPS tunnel setup

Bind the API to `127.0.0.1:8088`, then run one of:

```bash
cloudflared tunnel --url http://127.0.0.1:8088
```

```bash
ngrok http 8088
```

```bash
tailscale funnel 8088
```

Use the tunnel host as the Cursor base URL:

```text
https://<tunnel-host>/v1
```

Example with Cloudflare's quick tunnel:

```bash
# terminal 1
go run ./cmd/codex-chat-api --host 127.0.0.1 --port 8088

# terminal 2
cloudflared tunnel --url http://127.0.0.1:8088
# → https://something-random.trycloudflare.com
```

Cursor base URL: `https://something-random.trycloudflare.com/v1`

Verify routing before testing in Cursor:

```bash
curl -s https://<tunnel-host>/health
curl -s https://<tunnel-host>/v1/models
```

Keep the API bound to `127.0.0.1` unless your tunnel tool requires otherwise.

### Ask vs Agent mode

**Ask mode** works for plain Q&A through the tunnel.

**Agent mode** always sends `tools` in the request body. The server:

1. Switches to minimal Codex mode automatically.
2. Converts Cursor's Chat Completions tool schema into Codex's Responses API
   tool shape.
3. Streams OpenAI-compatible `delta.tool_calls` when Codex requests a tool.
4. Accepts continuation requests with prior `tool_calls` and `role:"tool"`
   results, then returns a final assistant answer.

Agent mode should send `tools` in the first request, receive an assistant
`tool_calls` response, execute the tool locally, then send a continuation request
containing the prior assistant `tool_calls` and matching `role:"tool"` results.
The final response should be normal assistant text with `finish_reason:"stop"`.

By default, requests that include `tools` enter an Agent queue keyed by
`CODEX_AGENT_QUEUE_KEY_MODE=cursor`. This allows up to
`CODEX_AGENT_MAX_ACTIVE` concurrent Agent streams across different Cursor chats
while keeping one active stream per chat/session key. Requests without
`tools`, including Ask mode, bypass the queue.

Tune the queue with:

```bash
CODEX_AGENT_QUEUE_ENABLED=true
CODEX_AGENT_MAX_ACTIVE=2
CODEX_AGENT_MAX_ACTIVE_PER_KEY=1
CODEX_AGENT_QUEUE_KEY_MODE=cursor
CODEX_AGENT_QUEUE_LIMIT=20
CODEX_AGENT_QUEUE_TIMEOUT=5m
CODEX_AGENT_QUEUE_LOCK_DIR=/tmp/codex-chat-api-agent-locks
CODEX_AGENT_QUEUE_PRIORITY_ENABLED=false
```

Set `CODEX_AGENT_QUEUE_ENABLED=false` to disable the in-process wait queue and
global active-request limit, or raise `CODEX_AGENT_MAX_ACTIVE` after validating
that overlapping Agent chats are stable in your workspace. Tool-capable requests
still use the per-key shared lock when `CODEX_AGENT_QUEUE_LOCK_DIR` is set, so
the same derived conversation key is not streamed concurrently.

### Codex client pool

By default, the server builds one Codex client from `CODEX_HOME` and
`CODEX_AUTH_PATH`, preserving the historical single-client behavior. To shard
independent conversations across multiple Codex logins, set `CODEX_CLIENTS` to a
JSON array. Each client needs a non-sensitive `label`; omit `auth_path` to use
`<codex_home>/auth.json`, and omit profile/scaffold paths to inherit the global
`CODEX_PROFILE_PATH` and `CODEX_SCAFFOLD_PATH`.

```bash
CODEX_CLIENTS='[
  {"label":"work-a","codex_home":"/home/codex/.codex-a"},
  {"label":"work-b","auth_path":"/run/secrets/codex-b-auth.json"}
]'
CODEX_CLIENT_POOL_UNAVAILABLE=fail
CODEX_CLIENT_COOLDOWN_DEFAULT=5m
```

For each request, the server resolves the same key used by the Agent queue and
selects a client with deterministic affinity. Repeated turns with the same
queue key map to the same shard; different queue keys can use different shards.
Random per-request load balancing is unsafe and is not used.

If the selected account returns a quota or rate-limit failure before its first
stream event, the server cools that account until the upstream `Retry-After` or
reset hint. Without a valid hint it uses `CODEX_CLIENT_COOLDOWN_DEFAULT`. The
same request is retried once on another healthy account without changing its
model; model-level quota fallback runs only after that account rotation is
exhausted. New requests keep their sticky shard while it is healthy and skip it
while it is cooling. A quota failure after a content, reasoning, or tool delta
never switches accounts mid-stream.

Cooldowns are held in memory per process and expire automatically. Replicas do
not share cooldown state, so each replica may independently discover the same
limited account. When every account is cooling, ordinary requests retain the
existing OpenAI-compatible 429/model-overflow behavior.

When `CODEX_CLIENT_POOL_UNAVAILABLE=fail`, an unavailable selected client returns
the upstream/auth error. `fallback_first` retries the first configured client
when a non-primary shard fails before a stream starts, but it can break strict
conversation affinity after a conversation has already used that shard. Use
`fail` unless availability is more important than shard continuity.

Pool logs are redacted:

```text
codex_client_cooldown label=work-a until=2026-07-21T21:05:00Z
codex_client_select request_id=... key_mode=cursor:metadata key_hash=... shard=1 client_label=work-b fallback=rotate
```

Do not put credentials, account IDs, auth paths, hostnames with secrets, or user
names in client labels. Labels are intended only as safe operational aliases.
The Agent queue also creates one shared lock file per queue-key hash in
`CODEX_AGENT_QUEUE_LOCK_DIR`. Multiple API replicas must point this setting at
the same writable shared volume so the same chat cannot stream concurrently in
different processes. Keep `CODEX_AGENT_MAX_ACTIVE_PER_KEY=1`; sticky routing by
the same queue key is still recommended to reduce lock contention.
The supplied Docker Compose file mounts this path on a named volume by default
at `/var/lib/codex-chat-api/agent-locks`, so replicas started from that Compose
project share locks without extra volume wiring.

The queue classifies request shapes as `tool_generating`,
`tool_result_continuation`, `final_prose_continuation`, or `simple_no_tool` and
logs the class as `turn_class=...`. The optional priority experiment can reorder
eligible waiters across different conversation keys so a
`tool_result_continuation` can run before a lower-priority tool-generating turn.
It never bypasses `CODEX_AGENT_MAX_ACTIVE_PER_KEY`; no same-chat priority lane is
enabled because request shape alone does not prove concurrent upstream streams
for one conversation are safe.

Long Cursor Agent conversations can accumulate large historical tool outputs.
Context management is enabled by default for tool-capable minimal-mode requests
such as Cursor Agent traffic. It never rejects an oversized request, truncates
oversized individual tool outputs with an explicit marker, and compacts older
tool outputs once the configured byte or message threshold is exceeded. The most
recent `CODEX_CONTEXT_RECENT_MESSAGES` messages are left unchanged, and assistant
`tool_calls` plus matching `role:"tool"` / `tool_call_id` messages stay paired.

Tune it conservatively:

```bash
CODEX_CONTEXT_MANAGEMENT_ENABLED=true
CODEX_CONTEXT_MAX_BYTES=196608
CODEX_CONTEXT_MAX_MESSAGES=120
CODEX_CONTEXT_RECENT_MESSAGES=24
CODEX_CONTEXT_TOOL_OUTPUT_MAX_BYTES=32768
CODEX_CONTEXT_COMPACTED_TOOL_OUTPUT_MAX_BYTES=512
```

When context management changes a request, the server logs one redacted
`context_manage` line with before/after message counts, approximate bytes, tool
output counts, and truncation/compaction counts. It does not log prompt text,
tool arguments, or tool output content.

Every chat completion also logs one redacted `request_timing` line with
`context_ms`, `queue_wait_ms`, `upstream_stream_ms`, `first_delta_ms`, and
`total_ms`. Non-streaming requests use `first_delta_ms=-1`.

Queue key modes:

| Mode | Behavior |
| --- | --- |
| `cursor` | Prefer stable Cursor conversation identifiers from metadata/body, then stable Cursor/session headers, then a deterministic conversation fingerprint anchored on the earliest tool-call ID (or hashed first user message before tools appear), then `x-forwarded-for` or `remote_ip` as safe fallbacks. |
| `global` | Fallback when a header/body key is missing. All unmatched Agent traffic shares one key. |
| `auth_hash` | All requests using the same API key serialize. The raw key is never logged. |
| `header:<name>` | Queue by a selected request header, for example `header:x-cursor-session-id`, if real traffic shows it is stable. |
| `body:<field>` | Queue by a selected top-level JSON body field, for example `body:session_id`, if real traffic shows it is stable. |
| `request_fingerprint` | Queue by a redacted fingerprint of authorization hash, user agent hash, and remote IP. This is not a proven per-chat key. |

Configured header/body modes fall back to the global key when the selected value
is missing. The default `cursor` mode logs the source in `key_mode`, such as
`cursor:metadata`, `cursor:conversation_fingerprint`, `cursor:x-forwarded-for`,
`cursor:remote_ip`, and logs only `key_hash`, never raw identifiers,
prompt text, tool arguments, tool outputs, or request bodies.

### Tool-call protocol notes

Cursor sends OpenAI **Chat Completions** tools:

```json
{"type":"function","function":{"name":"list_dir","description":"...","parameters":{...}}}
```

Codex expects **Responses API** tools with function fields flattened:

```json
{"type":"function","name":"list_dir","description":"...","parameters":{...}}
```

The server converts between these formats automatically (`v0.0.4+`).

Codex also enforces a **64-character maximum** on `call_id`. Cursor can emit
longer `tool_call_id` values; the server hashes long IDs deterministically so
matching assistant `function_call` and tool `function_call_output` items stay
paired.

Tool-call websocket event shapes are documented in `docs/codex-tool-events.md`.

### Validation prompts

Use these in a **new** Agent chat after configuring the tunnel:

```text
List the files in this repo.
```

```text
Read go.mod and summarize the module name and direct dependencies.
```

```text
First list the files in this repo, then read go.mod, then summarize what you found.
```

A successful run should:

- Execute real local tools (for example `rg --files`, file reads).
- Return answers that reflect actual repo contents, not hallucinated paths.
- Show `tools_present=true` in server logs for Agent requests.

Validated in `v0.0.4`: Cursor Agent executed `rg --files` against a real repo
through a Cloudflare tunnel and returned actual top-level files.

### Diagnostics

Enable redacted body-shape logging:

```bash
CODEX_LOG_BODY_SHAPE=true go run ./cmd/codex-chat-api --host 127.0.0.1 --port 8088
```

To discover stable Cursor request identity signals, enable the request identity
diagnostic line:

```bash
CODEX_LOG_REQUEST_IDENTITY=true go run ./cmd/codex-chat-api --host 127.0.0.1 --port 8088
```

Successful Cursor probes should log:

- `GET /v1/models`
- `POST /v1/chat/completions` with `authorization_present=true`
- `request_identity request_id=... method=POST path=/v1/chat/completions ...`
- `chat_completion model=gpt-5.5-high stream=... tools_present=... turn_class=...`
- `agent_queue_acquire request_id=...` before queued Agent stream starts
- `agent_queue_release request_id=...` after the stream fully ends

Body-shape logs include field names, message count, message roles, and tool count.
Bearer tokens and message content are never printed. Codex tool websocket events
are logged as redacted structural summaries when body-shape logging is enabled.

Request identity logs include header names, selected Cursor/session/request
header hashes, top-level body fields, metadata field names, message role
sequence, stream flag, and tool count. They do not print Authorization values,
cookies, prompt content, message content, tool arguments, tool schemas, or full
request bodies.

When multiple Agent chats overlap, queue diagnostics show the lifecycle:

```text
agent_queue_wait request_id=... key_mode=cursor:conversation_fingerprint key_hash=... position=2
agent_queue_acquire request_id=... key_mode=cursor:conversation_fingerprint key_hash=... wait_ms=1234 active_global=2 active_key=1
agent_queue_lock_acquire request_id=... key_mode=cursor:conversation_fingerprint key_hash=... lock_wait_ms=1234
codex_client_select request_id=... key_mode=cursor:conversation_fingerprint key_hash=... shard=0 client_label=default fallback=none
stream_start id=...
stream_end id=... outcome=completed finish=tool_calls
agent_queue_lock_release request_id=... key_mode=cursor:conversation_fingerprint key_hash=...
agent_queue_release request_id=... key_mode=cursor:conversation_fingerprint key_hash=... run_ms=8123 active_global=1 active_key=0
agent_queue_wait request_id=... key_mode=cursor:conversation_fingerprint key_hash=... turn_class=tool_generating priority=0 position=2
agent_queue_acquire request_id=... key_mode=cursor:conversation_fingerprint key_hash=... turn_class=tool_generating priority=0 wait_ms=1234 active_global=2 active_key=1
agent_queue_release request_id=... key_mode=cursor:conversation_fingerprint key_hash=... turn_class=tool_generating priority=0 run_ms=8123 active_global=1 active_key=0
```

If the queue is full or a request waits longer than `CODEX_AGENT_QUEUE_TIMEOUT`,
the API returns an OpenAI-shaped `429` error and logs `agent_queue_full` or
`agent_queue_timeout`.

For live Cursor/ngrok validation, start two side-by-side Cursor Agent chats and
confirm their queue lines show different `key_hash` values with
`key_mode=cursor:metadata` or `key_mode=cursor:conversation_fingerprint`.
Repeated turns in the same chat should keep the same `key_hash`. Tool-call
streams should still include valid `delta.tool_calls` frames and finish with
`finish_reason:"tool_calls"`.

Upstream Codex errors are logged server-side as `stream_error` or
`complete_error` with the real payload. Clients still receive the sanitized
`[error: upstream error]` message.

#### Failure taxonomy (operators)

Every `stream_error` and `complete_error` line also carries a redacted
`failure_class` and `failure_phase` used by pool cooldown / rotation logic.
These fields are derived only from the error type, status code, and body
markers the server already knows — they never contain auth tokens, account
emails, or raw upstream bodies.

`failure_class` maps upstream failures deterministically:

| `failure_class` | Meaning | Mapped from |
| --- | --- | --- |
| `quota` | Account-scoped usage limit | `usage_limit_reached` (`ErrUsageLimitReached`) |
| `rate_limit` | Transient capacity throttle | non-usage-limit `429` upstream errors |
| `auth` | Credential/authorization failure | `auth`-kind Codex errors |
| `permanent` | Client-side, rotation cannot help | `context_length_exceeded`, other `client`-kind (`400`) errors |
| `transient` | Retryable/unknown upstream or transport failure | `5xx`, unavailable clients, and any unmapped error |

`failure_phase` records how far the request progressed when it failed:
`connect` (before any upstream event), `first_event` (the failure is the first
event, nothing sent to the client yet), or `mid_stream` (content already
streamed). Rotation logic refuses to switch accounts `mid_stream`, since that
would corrupt an in-flight Agent tool turn.

### Troubleshooting

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| `Unauthorized User Openai API key` and **no server log entry** | Cursor failed before reaching this API | Use a non-empty dummy key; confirm `GET /v1/models` works through the tunnel |
| `Access to private networks is forbidden` | Cursor cannot reach localhost | Use `cloudflared` / `ngrok` / `tailscale funnel` and a `https://.../v1` base URL |
| `[error: upstream error]` on first Agent turn | Old build before `v0.0.4`, or missing `codex login` | Upgrade to `v0.0.4+`, run `codex login`, check `stream_error` logs |
| `[error: upstream error]` in an **existing** chat | Poisoned history (failed tool turns, long `call_id`s) | Start a **new** Agent chat |
| Agent describes tools but does not run them | Ask mode, wrong model, or Cursor not using the custom endpoint | Use Agent mode, model `gpt-5.5` or a documented alias, confirm requests hit server logs |
| Agent chats stall when several run at once | Queue disabled, wrong key mode, or concurrency set too high for one workspace | Keep `CODEX_AGENT_QUEUE_ENABLED=true`, `CODEX_AGENT_QUEUE_KEY_MODE=cursor`, and `CODEX_AGENT_MAX_ACTIVE_PER_KEY=1`; confirm queue logs |
| `tools[0].name` missing (in logs) | Pre-`v0.0.4` tool-format bug | Upgrade to `v0.0.4+` |
| `call_id` string too long (in logs) | Long Cursor IDs in continuation history | Upgrade to `v0.0.4+` and start a fresh chat |

If direct localhost produces no request logs, the tunnel path is required for your
Cursor build.

### Compatibility limitations

- Cursor may probe `/v1/responses`; this service does not implement it.
- Cursor Tab autocomplete is not expected to use local/custom endpoints.
- Some Cursor modes may still use Cursor-managed routes regardless of settings.
- Faithful Codex fingerprint mode (`faithful:true`) is disabled automatically when
  the client sends `tools`. Use explicit `"faithful": true` only for non-Cursor
  plain chat requests without client tools.

## Fingerprint: codex-exact by default

By default (`faithful: true`) the API reproduces the real Codex CLI request at the
application layer for **plain chat without client tools**. Cursor Agent and other
clients that send their own `tools` automatically use minimal mode instead (see
[Cursor Compatibility](#cursor-compatibility)).

In faithful mode, verified against a live capture:

- All 12 Codex app headers, in Codex order and with Codex values
  (`x-codex-beta-features`, `x-client-request-id`, `session-id`, `thread-id`,
  `x-codex-window-id`, `x-codex-turn-metadata`, Codex `user-agent`, etc.).
- The real instructions and tools from `codex_profile.json`, plus `tool_choice`,
  `parallel_tool_calls`, `reasoning`, `include`, `text`, and `client_metadata`.
- The developer/environment scaffold from `codex_scaffold.json`, with cwd, date,
  and timezone filled live.
- Prewarm-then-turn over two websocket connections. Prewarm carries
  `generate:false`; the turn omits `generate`.
- `session_id`, `thread_id`, and `turn_id` use uuidv7 like Codex.

Re-snapshot `codex_profile.json` and `codex_scaffold.json` if your Codex
version, plugins, or captured profile change.

### Known Fingerprint Limitations

- TLS fingerprint (JA3/JA4): this client uses Go's standard TLS stack; Codex is
  Rust/rustls.
- WebSocket upgrade header order/casing: `Host`, `Connection`, `Upgrade`, and
  `Sec-WebSocket-*` are emitted by the Go websocket stack, not in tungstenite's
  exact byte order.

Matching those transport-layer details requires a TLS-impersonating transport or a
small Rust sidecar. Set `"faithful": false` for the minimal plain-chat request.

## Release Validation

Run before tagging a release:

```bash
GOCACHE=$PWD/.gocache go test ./...
GOCACHE=$PWD/.gocache go vet ./...
GOCACHE=$PWD/.gocache go build ./...
```

Live checks with `codex login` and the server running on `127.0.0.1:8088`:

```bash
curl -s http://127.0.0.1:8088/health
curl -s http://127.0.0.1:8088/v1/models | jq .
curl -s http://127.0.0.1:8088/v1/chat/completions \
  -H 'authorization: Bearer local-codex-chat-api' \
  -H 'content-type: application/json' \
  -d '{"model":"gpt-5.5-high","messages":[{"role":"user","content":"Say hi in 5 words"}]}' | jq .
curl -N http://127.0.0.1:8088/v1/chat/completions \
  -H 'content-type: application/json' \
  -d '{"model":"gpt-5.5-fast","stream":true,"messages":[{"role":"user","content":"Count to 5"}]}'
```

For Cursor compatibility releases (`v0.0.2+`), also validate through an HTTPS
tunnel with Agent mode using the prompts in
[Validation prompts](#validation-prompts).
For issue #45 latency/logging changes, record the live Cursor BYOK evidence in
[Issue 45 Live Validation](docs/issue-45-live-validation.md).

## Releases

| Version | Highlights |
| --- | --- |
| `v0.0.4` | Cursor Agent tool-format fix (Chat Completions → Responses API), long `call_id` normalization, upstream error logging |
| `v0.0.3` | Cursor Agent tool-call compatibility: OpenAI tool types, Codex tool events, tool-result continuation |
| `v0.0.2` | Cursor local-model setup: `/v1/models`, dummy API key, diagnostics, tunnel docs |
| `v0.0.1` | Initial Go refactor from Python prototype |

## Notes

- Default model: `gpt-5.6-sol`. Cursor-selectable GPT-5.6 aliases
  (`gpt-5.6-terra`, `gpt-5.6-luna`, effort suffixes, `codex-*`) resolve to the
  matching upstream tier with server-side reasoning effort and verbosity.
  Legacy `gpt-5.5*` aliases still map to `gpt-5.5`.
- Token credentials are read fresh from `auth.json` on every request, so refreshes
  by the Codex app are picked up. If you get 401s, run `codex login` again.
- In faithful mode the model is told it is the Codex coding agent and is offered
  Codex tools. For plain Q&A it usually answers normally, but it may emit a tool
  call that this API ignores. Use `faithful:false` for clean assistant-style chat,
  or let the server auto-switch when the client sends `tools`.
- Requests are subject to your ChatGPT plan's rate limits.


### Cursor Custom Model

For Cursor's OpenAI-compatible custom provider, use:

```text
Base URL: http://127.0.0.1:8088/v1
API Key: any non-empty value
Model: gpt-5.6-terra
```

Recommended Cursor custom models to add: `gpt-5.6-sol`, `gpt-5.6-sol-high`,
`gpt-5.6-terra`, `gpt-5.6-luna`, `gpt-5.6-luna-fast`, `gemini-3.1-pro-low`,
`gemini-3.1-pro-high`, `gemini-3.5-flash-medium`, `gemini-2.5-flash`.

`gpt-5.3-codex-spark` (+ `-preview`) is also listed for overflow turns (96 KiB hard context).
