# Issue 52 Validation

This page records validation evidence for the Cursor BYOK streamed tool-call
compatibility hardening.

## Automated Verification

Run from the repository root:

```bash
GOCACHE=$PWD/.gocache go test ./internal/codex ./internal/openai ./internal/server
```

Result recorded on 2026-06-27:

```text
ok  	github.com/teslashibe/open-agent-api/internal/codex	(cached)
ok  	github.com/teslashibe/open-agent-api/internal/openai	(cached)
ok  	github.com/teslashibe/open-agent-api/internal/server	(cached)
```

The server tests assert exact Cursor-facing streamed SSE shape for fragmented
tool-call events, empty-delta suppression, fallback tool-call IDs, invalid JSON
rejection, and parallel tool-call ordering.

## Live Cursor Validation Evidence

AC 14 requires a live Cursor desktop regression check against a previously
stalled chat over several tool continuation turns. This cannot be reproduced by
unit tests alone because the failure mode depends on Cursor's BYOK stream
consumer.

Use this route while the API is listening on `127.0.0.1:8088`:

```bash
ngrok http --url=YOUR_SUBDOMAIN.ngrok-free.dev 8787
mitmdump -s tools/cursor_header_capture.py --mode reverse:http://127.0.0.1:8088@8787 --listen-host 127.0.0.1 --set flow_detail=0
```

Cursor base URL:

```text
https://YOUR_SUBDOMAIN.ngrok-free.dev/v1
```

For the issue to be fully validated, paste at least three consecutive redacted
`cursor_response_shape` lines from the previously stalled Cursor chat below.
Each line must have:

- `empty_tool_frames=0`
- `finish=tool_calls`
- `done=True`
- `tool_ids_present=True`
- `tool_names_present=True`
- `tool_args_json_valid=True`

Record whether Cursor advanced to the next turn without switching providers.

```text
turn_1: <paste cursor_response_shape ...> cursor_advanced=<true|false>
turn_2: <paste cursor_response_shape ...> cursor_advanced=<true|false>
turn_3: <paste cursor_response_shape ...> cursor_advanced=<true|false>
```

Do not mark AC 14 complete until all three turns have `cursor_advanced=true`.
