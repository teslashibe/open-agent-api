# Codex Tool Events

This service maps Codex websocket tool/function-call events into the internal
tool-call representation used by the OpenAI chat response layer.

## Observed event shapes

Codex Responses websocket function calls are handled as a lifecycle:

- `response.output_item.added`: starts a function call. Relevant fields:
  `output_index`, `item.id`, `item.type`, `item.call_id`, `item.name`.
- `response.function_call_arguments.delta`: streams argument fragments. Relevant
  fields: `output_index`, `item_id`, `delta`.
- `response.function_call_arguments.done`: marks final arguments complete.
  Relevant fields: `output_index`, `item_id`, `arguments`.
- `response.output_item.done`: marks the output item complete. Relevant fields:
  `output_index`, `item.id`, `item.type`, `item.call_id`, `item.name`,
  `item.arguments`.

The parser also accepts compatibility shapes used by tests and captures:

- `response.tool_call.created`
- `response.tool_call.start`
- `response.function_call.started`
- `response.tool_call.arguments.delta`
- `response.tool_call.delta` with `tool_call_delta` or `tool_calls`
- `response.tool_call.completed`
- `response.tool_call.done`
- `response.function_call.completed`

## Internal mapping

Start events become `StreamEvent.ToolCallDelta` values with:

- `index`: Codex `output_index`
- `id`: Codex `item.call_id`, falling back to `item.id`
- `type`: `function`
- `function.name`: Codex `item.name` or `item.function.name`

Argument fragment events become `StreamEvent.ToolCallDelta` values with:

- `index`: Codex `output_index`
- `type`: `function`
- `function.arguments`: Codex `delta`, `arguments_delta`, or `arguments`

`response.function_call_arguments.done` updates the accumulated arguments for
the in-progress tool call. `response.output_item.done` and explicit
`tool_calls` arrays from compatibility frames are treated as full internal tool
calls.

The streaming OpenAI response layer uses a Cursor-compatible accumulator for
tool calls. Codex start/name/argument fragments are not forwarded as individual
OpenAI `delta.tool_calls` frames. Instead, the server tracks each tool call by
upstream output index, assigns a stable OpenAI index in first-seen order,
accumulates argument fragments, and emits one complete `delta.tool_calls` frame
per tool call immediately before the final `finish_reason:"tool_calls"` chunk.

Each emitted OpenAI tool-call delta includes:

- stable `index`
- non-empty `id` from Codex, or a deterministic stream-local fallback
- `type: "function"`
- `function.name`
- complete JSON `function.arguments`

Empty tool-call deltas with no ID, type, function name, or function arguments
are ignored. If the final accumulated arguments are not valid JSON, the stream
returns an error chunk instead of a `tool_calls` finish.

## Debug logging

When `CODEX_LOG_CODEX_TOOL_EVENTS=true` or `--log-codex-tool-events` is enabled,
raw Codex tool-event diagnostics are logged as redacted structural summaries:

```text
codex_tool_event valid_json=true type=response.function_call_arguments.delta fields=delta,item_id,type has_item=false has_tool_call_delta=false tool_calls_count=0
```

The log line includes event names, top-level field names, and counts. It does not
print tool arguments, prompt text, authorization values, or raw payload content.
This deeper debug flag is separate from `CODEX_LOG_BODY_SHAPE` so normal capture
logging does not emit one `codex_tool_event` line per argument fragment.

## Manual validation

Start the API with a valid Codex login:

```bash
GOCACHE=$PWD/.gocache CODEX_LOG_CODEX_TOOL_EVENTS=true go run ./cmd/codex-chat-api --host 127.0.0.1 --port 8088
```

Non-streaming check:

```bash
curl -s http://127.0.0.1:8088/v1/chat/completions \
  -H 'authorization: Bearer local-codex-chat-api' \
  -H 'content-type: application/json' \
  -d '{
    "model":"gpt-5.5",
    "messages":[{"role":"user","content":"Use the lookup tool for codex and do not answer directly."}],
    "tools":[{"type":"function","function":{"name":"lookup","description":"Look up a query.","parameters":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}}}],
    "tool_choice":{"type":"function","function":{"name":"lookup"}}
  }' | jq .
```

Expected response shape: `choices[0].message.tool_calls` is present and
`choices[0].finish_reason` is `tool_calls`.

Streaming check:

```bash
curl -N http://127.0.0.1:8088/v1/chat/completions \
  -H 'authorization: Bearer local-codex-chat-api' \
  -H 'content-type: application/json' \
  -d '{
    "model":"gpt-5.5",
    "stream":true,
    "messages":[{"role":"user","content":"Use the lookup tool for codex and do not answer directly."}],
    "tools":[{"type":"function","function":{"name":"lookup","description":"Look up a query.","parameters":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}}}],
    "tool_choice":{"type":"function","function":{"name":"lookup"}}
  }'
```

Expected stream shape: chunks preserve incremental assistant text, if any, but
tool calls are emitted as one complete `delta.tool_calls` frame per call. The
final chunk has `finish_reason:"tool_calls"` before `data: [DONE]`.

Cursor wire-capture validation:

```bash
ngrok http --url=YOUR_SUBDOMAIN.ngrok-free.dev 8787
mitmdump -s tools/cursor_header_capture.py --mode reverse:http://127.0.0.1:8088@8787 --listen-host 127.0.0.1 --set flow_detail=0
```

Validation checklist:

- Capture a real Cursor `/v1/chat/completions` streaming request and response.
- Confirm each streamed tool call has exactly one complete Cursor-facing
  `delta.tool_calls` frame before `finish_reason:"tool_calls"`.
- Confirm no empty `delta.tool_calls` frames are present.
- Run several tool continuation turns in the previously stalled chat and verify
  Cursor continues advancing.

The mitm addon emits redacted evidence lines for both sides of the stream. A
passing capture should include request shape and response shape lines like:

```text
cursor_capture method=POST path=/v1/chat/completions body_bytes=<n> body_fields=messages,model,stream,stream_options,tools,user message_count=<n> tool_count=<n> stream=true headers=<redacted>
cursor_response_shape events=<n> tool_frames=<tool-call-count> empty_tool_frames=0 finish=tool_calls done=True tool_indexes=0 tool_ids_present=True tool_names_present=True tool_args_json_valid=True
```

For a stalled-chat regression check, record at least three consecutive Cursor
tool continuation turns through the capture route. The validation passes only if
each tool-call turn has `empty_tool_frames=0`, `finish=tool_calls`, `done=True`,
`tool_ids_present=True`, `tool_names_present=True`, `tool_args_json_valid=True`,
and Cursor advances to the next turn without switching providers.

Record the live evidence in
[Issue 52 Validation](issue-52-validation.md) so the release or PR review can
verify the stalled-chat regression check directly.
