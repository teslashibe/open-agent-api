# Codex Chat API

A small **OpenAI-compatible** HTTP API that proxies chat to the Codex backend
(`wss://chatgpt.com/backend-api/codex/responses`) using the ChatGPT OAuth token
stored in `~/.codex/auth.json`. No `sk-` API key needed — it rides your existing
`codex login` session.

> Reverse-engineered from `codex_cli_rs` 0.142.0. See `../../codex-backend-api.md`
> for the underlying protocol. Use responsibly and within OpenAI's terms.

## Setup

```bash
cd codex-chat-api
python3 -m venv .venv && source .venv/bin/activate
pip install -r requirements.txt
```

You must be logged in via Codex (`codex login`) so `~/.codex/auth.json` exists.

## Run

```bash
uvicorn app:app --host 127.0.0.1 --port 8088
```

## Go development

The Go refactor scaffold currently exposes `GET /health` only. It does not
implement chat completions or websocket transport yet, and the Python prototype
remains in place.

```bash
go test ./...
go vet ./...
go build ./...
go run ./cmd/codex-chat-api --host 127.0.0.1 --port 8088
```

Health check:

```bash
curl -s http://127.0.0.1:8088/health
```

## Use

cURL (non-streaming):
```bash
curl -s http://127.0.0.1:8088/v1/chat/completions \
  -H 'content-type: application/json' \
  -d '{"model":"gpt-5.5","messages":[{"role":"user","content":"Say hi in 5 words"}]}' | jq .
```

Streaming (SSE):
```bash
curl -N http://127.0.0.1:8088/v1/chat/completions \
  -H 'content-type: application/json' \
  -d '{"model":"gpt-5.5","stream":true,"messages":[{"role":"user","content":"Count to 5"}]}'
```

With the OpenAI Python SDK:
```python
from openai import OpenAI
client = OpenAI(base_url="http://127.0.0.1:8088/v1", api_key="not-needed")
r = client.chat.completions.create(
    model="gpt-5.5",
    messages=[{"role": "user", "content": "Hello!"}],
)
print(r.choices[0].message.content)
```

## Fingerprint: codex-exact by default

By default (`faithful: true`) the API reproduces the real Codex CLI request at the
**application layer**, verified against a live capture:

- All 12 Codex app headers, in Codex order and with Codex values
  (`x-codex-beta-features`, `x-client-request-id`, `session-id`, `thread-id`,
  `x-codex-window-id`, `x-codex-turn-metadata`, Codex `user-agent`, etc.).
- The real ~21 KB `instructions` and 14 `tools`, plus `tool_choice`,
  `parallel_tool_calls`, `reasoning`, `include`, `text`, `client_metadata`.
- The developer/environment scaffold in `input` (cwd/date/timezone filled live).
- Prewarm-then-turn over two connections (prewarm carries `generate:false`; the
  turn omits `generate`), which reproduces Codex's prompt-cache hits.
- `session_id`/`thread_id`/`turn_id` use uuidv7 like Codex.

These come from `codex_profile.json` (instructions + tools) and
`codex_scaffold.json` (developer + environment_context), snapshotted from a real
session. Re-snapshot them if your Codex version/plugins change.

### What still differs (cannot be matched from pure Python)
- **TLS fingerprint (JA3/JA4):** this client is Python/OpenSSL; Codex is Rust/rustls.
- **WebSocket upgrade header order/casing:** `Host/Connection/Upgrade/Sec-WebSocket-*`
  are emitted by the `websockets` library, not in tungstenite's exact byte order.

Matching those needs a TLS-impersonating transport (e.g. `curl_cffi`) or a small
Rust sidecar. Set `"faithful": false` for the minimal plain-chat request instead.

## Notes / limitations

- Default model `gpt-5.5`. `gpt-5-codex` is rejected on ChatGPT accounts.
- Token is read fresh from `auth.json` on every request, so refreshes by the Codex
  app are picked up. If you get 401s, run `codex login` again.
- Extra knobs beyond the OpenAI schema: `reasoning_effort` (`low|medium|high`),
  `verbosity` (`low|medium|high`), `faithful` (default `true`), `prewarm` (default `true`).
- In faithful mode the model is told it's the Codex coding agent and is offered the
  Codex tools; for plain Q&A it still just answers, but it *may* occasionally emit a
  tool call (ignored). Use `faithful:false` for clean assistant-style chat.
- Subject to your ChatGPT plan's rate limits.
