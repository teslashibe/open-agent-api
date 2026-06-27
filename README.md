# Codex Chat API

A small OpenAI-compatible Go HTTP API that proxies chat requests to the Codex
backend (`wss://chatgpt.com/backend-api/codex/responses`) using the ChatGPT OAuth
token stored by `codex login`. No `sk-` API key is needed.

> Reverse-engineered from `codex_cli_rs` 0.142.0. Use responsibly and within
> OpenAI's terms.

## Requirements

- Go 1.23 or newer.
- A Codex login session with `~/.codex/auth.json` present:

```bash
codex login
```

The old Python prototype has been removed. The Go server is the supported runtime
(current release: `v0.0.4`).

## Quick Start (Cursor)

1. Log in to Codex and start the API locally:

```bash
codex login
go run ./cmd/codex-chat-api --host 127.0.0.1 --port 8088
```

2. Expose it with a public HTTPS tunnel (required for most Cursor BYOK paths):

```bash
cloudflared tunnel --url http://127.0.0.1:8088
```

Copy the `https://<random>.trycloudflare.com` URL from the tunnel output.

3. In **Cursor → Settings → Models**, enable the OpenAI API key override:

| Field | Value |
| --- | --- |
| OpenAI API Key | `local-codex-chat-api` (any non-empty string) |
| Override OpenAI Base URL | `https://<tunnel-host>/v1` |
| Model | `gpt-5.5` or an alias such as `gpt-5.5-high` |

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

- `GET /health`
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

The alias models are Cursor-selectable convenience IDs. They all send upstream
model `gpt-5.5` while applying server-side defaults:

| Public model ID | Upstream model | Reasoning effort | Verbosity |
| --- | --- | --- | --- |
| `gpt-5.5` | `gpt-5.5` | `medium` | `medium` |
| `gpt-5.5-low` | `gpt-5.5` | `low` | `medium` |
| `gpt-5.5-high` | `gpt-5.5` | `high` | `medium` |
| `gpt-5.5-fast` | `gpt-5.5` | `low` | `low` |

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
| Redacted body-shape logging | `CODEX_LOG_BODY_SHAPE` | `--log-body-shape` | `false` |

Request body options beyond the core OpenAI chat schema:

| Field | Values | Default |
| --- | --- | --- |
| `reasoning_effort` | `low`, `medium`, `high` | `medium` |
| `verbosity` | `low`, `medium`, `high` | `medium` |
| `faithful` | `true`, `false` | `true` when the client does not send `tools`; otherwise `false` |
| `prewarm` | `true`, `false` | follows `faithful` |

When the selected model is an alias, its reasoning and verbosity values become
the defaults for that request. Explicit `reasoning_effort` and `verbosity`
fields still override alias defaults for clients that send them.

When the client sends `tools` (Cursor Agent always does), the server automatically
uses **minimal mode**: no faithful CLI profile injection and no prewarm turn.
This avoids upstream Codex errors from conflicting tool definitions. Explicit
`faithful` / `prewarm` request fields still override the default.

## Cursor Compatibility

Cursor can use this API as a custom OpenAI-compatible endpoint for **Chat**,
**Cmd+K**, and **Agent** mode. Cursor Tab autocomplete does not use custom
endpoints.

### What is supported

| Feature | Status |
| --- | --- |
| `GET /health` | Supported |
| `GET /v1/models` | Supported (returns `gpt-5.5` and alias models) |
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
Model:                 gpt-5.5-high
```

The model ID is exact and case-sensitive. Cursor may display model names with
different capitalization in the UI, but the API model strings are `gpt-5.5`,
`gpt-5.5-low`, `gpt-5.5-high`, and `gpt-5.5-fast`.

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

Enable redacted request logging:

```bash
CODEX_LOG_BODY_SHAPE=true go run ./cmd/codex-chat-api --host 127.0.0.1 --port 8088
```

Successful Cursor probes should log:

- `GET /v1/models`
- `POST /v1/chat/completions` with `authorization_present=true`
- `chat_completion model=gpt-5.5-high stream=... tools_present=...` or the
  selected alias model

Body-shape logs include field names, message count, message roles, and tool count.
Bearer tokens and message content are never printed. Codex tool websocket events
are logged as redacted structural summaries when body-shape logging is enabled.

Upstream Codex errors are logged server-side as `stream_error` or
`complete_error` with the real payload. Clients still receive the sanitized
`[error: upstream error]` message.

### Troubleshooting

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| `Unauthorized User Openai API key` and **no server log entry** | Cursor failed before reaching this API | Use a non-empty dummy key; confirm `GET /v1/models` works through the tunnel |
| `Access to private networks is forbidden` | Cursor cannot reach localhost | Use `cloudflared` / `ngrok` / `tailscale funnel` and a `https://.../v1` base URL |
| `[error: upstream error]` on first Agent turn | Old build before `v0.0.4`, or missing `codex login` | Upgrade to `v0.0.4+`, run `codex login`, check `stream_error` logs |
| `[error: upstream error]` in an **existing** chat | Poisoned history (failed tool turns, long `call_id`s) | Start a **new** Agent chat |
| Agent describes tools but does not run them | Ask mode, wrong model, or Cursor not using the custom endpoint | Use Agent mode, model `gpt-5.5` or a documented alias, confirm requests hit server logs |
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
  -d '{"model":"gpt-5.5","messages":[{"role":"user","content":"Say hi in 5 words"}]}' | jq .
curl -N http://127.0.0.1:8088/v1/chat/completions \
  -H 'content-type: application/json' \
  -d '{"model":"gpt-5.5","stream":true,"messages":[{"role":"user","content":"Count to 5"}]}'
```

For Cursor compatibility releases (`v0.0.2+`), also validate through an HTTPS
tunnel with Agent mode using the prompts in
[Validation prompts](#validation-prompts).

## Releases

| Version | Highlights |
| --- | --- |
| `v0.0.4` | Cursor Agent tool-format fix (Chat Completions → Responses API), long `call_id` normalization, upstream error logging |
| `v0.0.3` | Cursor Agent tool-call compatibility: OpenAI tool types, Codex tool events, tool-result continuation |
| `v0.0.2` | Cursor local-model setup: `/v1/models`, dummy API key, diagnostics, tunnel docs |
| `v0.0.1` | Initial Go refactor from Python prototype |

## Notes

- Default model: `gpt-5.5`.
- Token credentials are read fresh from `auth.json` on every request, so refreshes
  by the Codex app are picked up. If you get 401s, run `codex login` again.
- In faithful mode the model is told it is the Codex coding agent and is offered
  Codex tools. For plain Q&A it usually answers normally, but it may emit a tool
  call that this API ignores. Use `faithful:false` for clean assistant-style chat,
  or let the server auto-switch when the client sends `tools`.
- Requests are subject to your ChatGPT plan's rate limits.
