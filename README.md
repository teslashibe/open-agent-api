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
    }
  ]
}
```

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
| `faithful` | `true`, `false` | `true` |
| `prewarm` | `true`, `false` | `true` |

## Cursor Local-Model Setup

Cursor can be pointed at this API the same way it is pointed at local
OpenAI-compatible servers such as Ollama, LM Studio, or LiteLLM. The local HTTP
API ignores the client `Authorization` value, but Cursor generally requires a
non-empty OpenAI API key field.

Direct localhost settings to try first:

```text
OpenAI API Key: local-codex-chat-api
Override OpenAI Base URL: http://127.0.0.1:8088/v1
Model: gpt-5.5
```

Also test this base URL if Cursor does not hit the API:

```text
http://localhost:8088/v1
```

The model ID is exact and case-sensitive: use `gpt-5.5`. Confirm Cursor reaches
the local API by watching server logs. Successful probes should show entries for
`GET /v1/models` and `POST /v1/chat/completions`, with
`authorization_present=true` when Cursor sends the dummy key. Chat request logs
also include the selected model, `stream` flag, and whether `tools` were present.
The bearer token value and message content are never printed.

For extra diagnostics, enable redacted body-shape logging:

```bash
CODEX_LOG_BODY_SHAPE=true go run ./cmd/codex-chat-api --host 127.0.0.1 --port 8088
```

This prints field names, message count, message roles, and tool count, but not
message content or authorization values.

Tool-call event parsing and manual tool-call validation examples are documented
in `docs/codex-tool-events.md`. With body-shape logging enabled, Codex
tool/function-call websocket events are logged as redacted structural summaries
without argument content.

### Cursor Through HTTPS Tunnel

Some Cursor paths may construct requests through Cursor-managed servers. In that
case `localhost` and `127.0.0.1` are not reachable from Cursor's route, and a
public HTTPS tunnel is required. Bind the API locally, then expose port `8088`
with one of these:

```bash
ngrok http 8088
```

```bash
cloudflared tunnel --url http://127.0.0.1:8088
```

```bash
tailscale funnel 8088
```

Use the tunnel host as the Cursor base URL:

```text
OpenAI API Key: local-codex-chat-api
Override OpenAI Base URL: https://<tunnel-host>/v1
Model: gpt-5.5
```

Keep the API bound to `127.0.0.1` unless your tunnel tool requires otherwise.
The dummy Cursor key is not used for upstream Codex authentication; upstream
credentials still come from `~/.codex/auth.json`.

### Cursor Agent Validation

Use Cursor Agent mode with the custom OpenAI-compatible model configured above.
Agent mode should send `tools` in the first request, receive an assistant
`tool_calls` response, execute the tool locally, then send a continuation request
containing the prior assistant `tool_calls` and matching `role:"tool"` results.
The final response should be normal assistant text with `finish_reason:"stop"`.

Known-good validation prompts:

```text
List the files in this repo.
```

```text
Read go.mod and summarize the module name and direct dependencies.
```

```text
First list the files in this repo, then read go.mod, then summarize what you found.
```

With `CODEX_LOG_BODY_SHAPE=true`, expected evidence includes at least one
`POST /v1/chat/completions` log with `tools_present=true`, followed by another
chat completion log whose `message_roles` include `assistant,tool`. For tunnel
validation, record the exact tunnel command, the Cursor base URL, and whether
Cursor also probed `GET /v1/models` or any unsupported endpoint.

### Cursor Compatibility Notes

- Cursor Chat, Cmd+K, and Agent mode are the expected local/custom endpoint
  surfaces when Cursor routes them to the configured OpenAI-compatible endpoint.
- Cursor Tab autocomplete is not expected to use local/custom endpoints.
- Some Cursor modes or BYOK paths may still use Cursor-managed routes and may
  require HTTPS tunneling.
- Cursor may probe `GET /v1/models`, `POST /v1/chat/completions`, or other
  endpoints such as `/v1/responses`. This service currently implements
  `/v1/models` and `/v1/chat/completions`; `/v1/responses` is not implemented.
- If Cursor reports an OpenAI API key authorization error and no request appears
  in these server logs, the failure occurred before reaching this API.

### Issue 16 Validation Results

Recorded on 2026-06-26 in the issue #16 worktree.

Automated validation:

```text
GOCACHE=$PWD/.gocache go test ./...
?   	github.com/teslashibe/codex-chat-api/cmd/codex-chat-api	[no test files]
ok  	github.com/teslashibe/codex-chat-api/internal/auth	0.874s
ok  	github.com/teslashibe/codex-chat-api/internal/codex	0.354s
ok  	github.com/teslashibe/codex-chat-api/internal/config	0.656s
ok  	github.com/teslashibe/codex-chat-api/internal/openai	0.507s
ok  	github.com/teslashibe/codex-chat-api/internal/server	1.156s
ok  	github.com/teslashibe/codex-chat-api/internal/sse	0.890s

GOCACHE=$PWD/.gocache go vet ./...
pass, no output

GOCACHE=$PWD/.gocache go build ./...
pass, no output
```

Continuation coverage added for issue #16 verifies:

- OpenAI request parsing preserves multiple assistant `tool_calls` and matching
  `role:"tool"` messages with `tool_call_id`.
- Codex request building emits `function_call` and `function_call_output` input
  items for tool-result continuation turns, including two sequential call/result
  pairs.
- Non-streaming continuation requests return final assistant text with
  `finish_reason:"stop"`.
- Streaming continuation requests return final assistant text deltas and a final
  `finish_reason:"stop"` without emitting another tool-call finish.
- Server-level continuation coverage verifies a two-step tool sequence
  (`list_dir` result followed by `read_file` result) returns final assistant text
  containing the real tool outputs instead of stalling or ending with another
  tool-call finish.

Manual Cursor Agent tunnel validation could not be completed in this automated
worktree because the sandbox rejects binding a local listener, which is required
before starting `cloudflared` or connecting Cursor:

```text
GOCACHE=$PWD/.gocache CODEX_LOG_BODY_SHAPE=true go run ./cmd/codex-chat-api --host 127.0.0.1 --port 18088
codex-chat-api: failed to listen: listen tcp4 127.0.0.1:18088: bind: operation not permitted
```

Run the Cursor Agent validation prompts above in an interactive developer
environment with port binding enabled and record the observed tool activity,
final assistant answers, tunnel command, and Cursor base URL in the PR notes.

### Issue 11 Validation Results

Recorded on 2026-06-26 in the issue #11 worktree.

Automated validation:

```text
GOCACHE=$PWD/.gocache go test ./...
?   	github.com/teslashibe/codex-chat-api/cmd/codex-chat-api	[no test files]
ok  	github.com/teslashibe/codex-chat-api/internal/auth
ok  	github.com/teslashibe/codex-chat-api/internal/codex
ok  	github.com/teslashibe/codex-chat-api/internal/config
?   	github.com/teslashibe/codex-chat-api/internal/openai	[no test files]
ok  	github.com/teslashibe/codex-chat-api/internal/server
ok  	github.com/teslashibe/codex-chat-api/internal/sse

GOCACHE=$PWD/.gocache go vet ./...
pass, no output

GOCACHE=$PWD/.gocache go build ./...
pass, no output
```

Handler-level coverage added in `internal/server/server_test.go` verifies:

- `GET /v1/models` returns a model list containing `gpt-5.5`.
- `POST /v1/chat/completions` accepts an arbitrary
  `Authorization: Bearer local-codex-chat-api` header and reaches the Codex
  service layer.
- Request diagnostics include method, path, status, 404 visibility, auth
  presence, chat model, stream mode, and tool presence without logging bearer
  values or message content.

Live curl validation could not be completed in the automated sandbox because the
environment rejects listening on the local test port:

```text
GOCACHE=$PWD/.gocache go run ./cmd/codex-chat-api --host 127.0.0.1 --port 8088
codex-chat-api: failed to listen: listen tcp4 127.0.0.1:8088: bind: operation not permitted
```

Run these live checks in a developer environment with port binding enabled and a
valid `codex login`:

```bash
curl -s http://127.0.0.1:8088/v1/models | jq .

curl -s http://127.0.0.1:8088/v1/chat/completions \
  -H 'authorization: Bearer local-codex-chat-api' \
  -H 'content-type: application/json' \
  -d '{"model":"gpt-5.5","messages":[{"role":"user","content":"Say hi"}]}' | jq .
```

Cursor validation status for this automated run:

| Cursor base URL | Result | Observed server probes |
| --- | --- | --- |
| `http://127.0.0.1:8088/v1` | Not runnable in sandbox because the server could not bind to `127.0.0.1:8088`. | Not observed in sandbox. |
| `http://localhost:8088/v1` | Not runnable in sandbox because the server could not bind to `127.0.0.1:8088`. | Not observed in sandbox. |
| `https://<tunnel-host>/v1` | Not runnable in sandbox because a local listener is required before starting a tunnel. | Not observed in sandbox. |

For manual Cursor sign-off, record whether Cursor hits `GET /v1/models`,
`POST /v1/chat/completions`, and any unexpected endpoint such as
`/v1/responses`. If direct localhost produces no request logs, expose the same
local process with one tunnel command from the tunnel section above and record
the exact command and Cursor base URL that worked.

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

## Release Validation

Run and record these checks in the PR body before merging a release-readiness
change. Issue #1 can be closed only after the local checks and live curl checks
below have passing results in the PR notes.

Local validation:

```bash
GOCACHE=$PWD/.gocache go test ./...
GOCACHE=$PWD/.gocache go vet ./...
GOCACHE=$PWD/.gocache go build ./...
```

Live curl validation must be run with the Go server and a valid `codex login`.
Start the server:

```bash
GOCACHE=$PWD/.gocache go run ./cmd/codex-chat-api --host 127.0.0.1 --port 8088
```

Then run and record these probes:

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

The PR notes should include:

- `go test ./...`: passing output.
- `go vet ./...`: passing output or "pass, no output".
- `go build ./...`: passing output or "pass, no output".
- `/health`: observed `{"status":"ok"}` response.
- `/v1/models`: observed OpenAI-compatible model list containing `gpt-5.5`.
- Non-streaming chat: observed `chat.completion` response with assistant content.
- Streaming chat: observed SSE chunks ending in `data: [DONE]`.

## Notes

- Default model: `gpt-5.5`.
- Token credentials are read fresh from `auth.json` on every request, so refreshes
  by the Codex app are picked up. If you get 401s, run `codex login` again.
- In faithful mode the model is told it is the Codex coding agent and is offered
  Codex tools. For plain Q&A it usually answers normally, but it may emit a tool
  call that this API ignores. Use `faithful:false` for clean assistant-style chat.
- Requests are subject to your ChatGPT plan's rate limits.
