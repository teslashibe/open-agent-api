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
for `v0.0.1`.

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
- `POST /v1/chat/completions`

### Non-Streaming Chat

```bash
curl -s http://127.0.0.1:8088/v1/chat/completions \
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

Request body options beyond the core OpenAI chat schema:

| Field | Values | Default |
| --- | --- | --- |
| `reasoning_effort` | `low`, `medium`, `high` | `medium` |
| `verbosity` | `low`, `medium`, `high` | `medium` |
| `faithful` | `true`, `false` | `true` |
| `prewarm` | `true`, `false` | `true` |

## Fingerprint: codex-exact by default

By default (`faithful: true`) the API reproduces the real Codex CLI request at the
application layer, verified against a live capture:

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

## Release Validation Notes

For the `v0.0.1` PR, include these results:

```text
GOCACHE=$PWD/.gocache go test ./...  # pass
GOCACHE=$PWD/.gocache go vet ./...   # pass
GOCACHE=$PWD/.gocache go build ./... # pass
```

Live curl validation should be run with the Go server and a valid `codex login`:

```bash
curl -s http://127.0.0.1:8088/health

curl -s http://127.0.0.1:8088/v1/chat/completions \
  -H 'content-type: application/json' \
  -d '{"model":"gpt-5.5","messages":[{"role":"user","content":"Say hi in 5 words"}]}' | jq .

curl -N http://127.0.0.1:8088/v1/chat/completions \
  -H 'content-type: application/json' \
  -d '{"model":"gpt-5.5","stream":true,"messages":[{"role":"user","content":"Count to 5"}]}'
```

Document the observed `/health`, non-streaming chat, and streaming chat results in
the PR body.

## Notes

- Default model: `gpt-5.5`.
- Token credentials are read fresh from `auth.json` on every request, so refreshes
  by the Codex app are picked up. If you get 401s, run `codex login` again.
- In faithful mode the model is told it is the Codex coding agent and is offered
  Codex tools. For plain Q&A it usually answers normally, but it may emit a tool
  call that this API ignores. Use `faithful:false` for clean assistant-style chat.
- Requests are subject to your ChatGPT plan's rate limits.
