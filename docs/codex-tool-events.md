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

`response.function_call_arguments.done` and `response.output_item.done` are
recognized as tool lifecycle events but do not re-emit final arguments, because
the streaming OpenAI response has already emitted argument fragments. Explicit
`tool_calls` arrays from compatibility frames are forwarded as full internal
tool calls.

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

Expected stream shape: chunks include `delta.tool_calls` fragments and the final
chunk has `finish_reason:"tool_calls"` before `data: [DONE]`.
