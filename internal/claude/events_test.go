package claude

import (
	"testing"

	"github.com/teslashibe/codex-chat-api/internal/codex"
)

func TestParseJSONLEventTextDelta(t *testing.T) {
	event, ok, err := parseJSONLEvent([]byte(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello"}}}`))
	if err != nil {
		t.Fatalf("parseJSONLEvent: %v", err)
	}
	if !ok || event.Delta != "Hello" {
		t.Fatalf("event ok=%t delta=%q", ok, event.Delta)
	}
}

func TestParseJSONLEventUsageAndDone(t *testing.T) {
	event, ok, err := parseJSONLEvent([]byte(`{"type":"result","session_id":"s1","model":"claude-sonnet-4-6","usage":{"input_tokens":3,"output_tokens":5,"cache_read_input_tokens":2}}`))
	if err != nil {
		t.Fatalf("parseJSONLEvent: %v", err)
	}
	if !ok || !event.Done || event.Model != "claude-sonnet-4-6" || event.ID != "s1" {
		t.Fatalf("event = %#v ok=%t", event, ok)
	}
	if event.Usage.PromptTokens != 5 || event.Usage.CompletionTokens != 5 || event.Usage.TotalTokens != 10 {
		t.Fatalf("usage = %#v", event.Usage)
	}
}

func TestParseJSONLEventError(t *testing.T) {
	_, _, err := parseJSONLEvent([]byte(`{"type":"result","is_error":true,"error":"login required"}`))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestToolBridgeParserConvertsToolEnvelope(t *testing.T) {
	parser := newToolBridgeParser([]toolSpec{{Name: "read_file", Type: "function"}})
	events := parser.consume(codex.StreamEvent{Delta: "```cursor_tool_call\n{\"name\":\"read_file\",\"arguments\":{\"path\":\"README.md\"}}\n```"})
	if len(events) != 1 || events[0].ToolCallDelta == nil {
		t.Fatalf("events = %#v", events)
	}
	delta := events[0].ToolCallDelta
	if delta.Type != "function" || delta.Function.Name != "read_file" || delta.Function.Arguments != `{"path":"README.md"}` || !delta.Final {
		t.Fatalf("delta = %#v", delta)
	}
}

func TestToolBridgeParserBuffersSplitToolEnvelope(t *testing.T) {
	parser := newToolBridgeParser([]toolSpec{{Name: "read_file", Type: "function"}})
	if events := parser.consume(codex.StreamEvent{Delta: "```cursor_tool_call\n"}); len(events) != 0 {
		t.Fatalf("first events = %#v, want buffered", events)
	}
	events := parser.consume(codex.StreamEvent{Delta: "{\"name\":\"read_file\",\"arguments\":{\"path\":\"go.mod\"}}\n```"})
	if len(events) != 1 || events[0].ToolCallDelta == nil {
		t.Fatalf("events = %#v", events)
	}
	got := events[0].ToolCallDelta
	if got.Function.Name != "read_file" || got.Function.Arguments != `{"path":"go.mod"}` {
		t.Fatalf("delta = %#v", got)
	}
}

func TestToolBridgeParserConvertsCustomToolEnvelope(t *testing.T) {
	parser := newToolBridgeParser([]toolSpec{{Name: "apply_patch", Type: "custom"}})
	events := parser.consume(codex.StreamEvent{Delta: "```cursor_tool_call\n{\"name\":\"apply_patch\",\"input\":\"*** Begin Patch\\n*** End Patch\\n\"}\n```"})
	if len(events) != 1 || events[0].ToolCallDelta == nil {
		t.Fatalf("events = %#v", events)
	}
	got := events[0].ToolCallDelta
	if got.Type != "custom" || got.Function.Name != "apply_patch" || got.Function.Arguments != "*** Begin Patch\n*** End Patch\n" {
		t.Fatalf("delta = %#v", got)
	}
}
