# Cursor Priority Lanes

The Agent queue classifies Cursor-style chat completion requests before they
enter the queue:

- `tool_generating`: request includes `tools` and does not end with a matched
  tool result or final assistant prose.
- `tool_result_continuation`: request includes `tools` and ends with a
  `role:"tool"` message that matches a prior assistant `tool_calls` id.
- `final_prose_continuation`: request includes `tools` and ends with assistant
  prose without tool calls. This is a low-confidence structural signal because
  the server cannot know the next upstream turn result before starting it.
- `simple_no_tool`: request does not include `tools`; these already bypass the
  Agent queue.

The safe priority decision is cross-conversation only. When
`CODEX_AGENT_QUEUE_PRIORITY_ENABLED=true`, eligible waiters with higher priority
can be selected before lower-priority waiters for other conversation keys. The
queue still checks `activeKey[key] < CODEX_AGENT_MAX_ACTIVE_PER_KEY` before any
waiter starts, so the experiment cannot run two active upstream streams for the
same conversation key.

No same-conversation priority lane is enabled. Captured request shape can show
that a turn is likely a tool-result continuation, but it does not prove that the
upstream model will only produce final prose or that concurrent streams for the
same chat are safe. Keeping `CODEX_AGENT_MAX_ACTIVE_PER_KEY=1` remains the guard
against same-chat concurrency and empty tool-call regressions.
