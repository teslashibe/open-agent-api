# Issue 45 Live Validation

This record ties the issue #45 live-traffic acceptance checks to repeatable
validation points.

## Automated live-proxy coverage

`TestCodexUpstreamGiantToolStreamForwardsIncrementally` exercises the API through
a real local HTTP listener and websocket upstream harness, using a
tool-capable streaming Chat Completions request shaped like Cursor BYOK Agent
traffic.

Run it with:

```bash
GOCACHE=$PWD/.gocache go test ./internal/server -run TestCodexUpstreamGiantToolStreamForwardsIncrementally -v
```

The test validates the three live-traffic regressions called out in issue #45:

| Check | Assertion |
| --- | --- |
| Non-zero tool arguments | Server logs must include `tool_arg_chars=8800` and `tool_deltas=2201`. |
| No empty tool-call regression | Every streamed `delta.tool_calls` frame is decoded and rejected if id, type, name, and arguments are all empty. |
| Lower default log volume | Default logs must not contain `codex_tool_event`, and the request must stay within the compact log-line budget. |

## Validation record

The automated live-proxy harness is the merge-gating validation for issue #45.
It runs the same HTTP streaming path and websocket upstream path used by Cursor
BYOK traffic, with a high-volume tool-argument stream that previously caused
log spam and empty-tool-call risk.

```text
Date: 2026-06-27
Validation command: GOCACHE=$PWD/.gocache go test ./...
Live-proxy test: TestCodexUpstreamGiantToolStreamForwardsIncrementally
Observed tool_arg_chars: 8800
Observed empty tool-call deltas: 0
Observed codex_tool_event lines: 0 by default
Observed request_timing: context_ms, queue_wait_ms, upstream_stream_ms, first_delta_ms, total_ms
Observed log volume: within compact default log-line budget
Result: PASS
```

Release-candidate Cursor BYOK validation should record the same evidence from a
real Cursor Agent session through the configured HTTPS tunnel when that client
environment is available:

- `tool_arg_chars` is greater than `0` on the Agent tool-call turn.
- No streamed `delta.tool_calls` frame is empty.
- Default logging has no per-fragment `codex_tool_event` lines.
- `request_timing` includes `context_ms`, `queue_wait_ms`,
  `upstream_stream_ms`, `first_delta_ms`, and `total_ms`.
