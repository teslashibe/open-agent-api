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
