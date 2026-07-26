package claude

import (
	"testing"

	"github.com/teslashibe/open-chat-api/internal/codex"
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

func TestToolBridgeParserEmitsMultipleToolCalls(t *testing.T) {
	parser := newToolBridgeParser([]toolSpec{{Name: "read_file", Type: "function"}, {Name: "list_dir", Type: "function"}})
	events := parser.consume(codex.StreamEvent{Delta: "```cursor_tool_call\n{\"name\":\"read_file\",\"arguments\":{\"path\":\"a\"}}\n```\n```cursor_tool_call\n{\"name\":\"list_dir\",\"arguments\":{\"path\":\".\"}}\n```"})
	var calls []*codex.ToolCallDelta
	for _, e := range events {
		if e.ToolCallDelta != nil {
			calls = append(calls, e.ToolCallDelta)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %#v", calls)
	}
	if calls[0].Index != 0 || calls[0].Function.Name != "read_file" {
		t.Fatalf("first call = %#v", calls[0])
	}
	if calls[1].Index != 1 || calls[1].Function.Name != "list_dir" {
		t.Fatalf("second call = %#v", calls[1])
	}
	if calls[0].ID == calls[1].ID {
		t.Fatalf("tool call IDs must be unique: %q", calls[0].ID)
	}
}

func TestToolBridgeParserSuppressesFenceOnReasoningChannel(t *testing.T) {
	parser := newToolBridgeParser([]toolSpec{{Name: "read_file", Type: "function"}})
	events := parser.consume(codex.StreamEvent{ReasoningDelta: "```cursor_tool_call\n{\"name\":\"read_file\",\"arguments\":{\"path\":\"go.mod\"}}\n```"})
	if len(events) != 1 || events[0].ToolCallDelta == nil {
		t.Fatalf("events = %#v, want a single tool call and no leaked reasoning", events)
	}
	for _, e := range events {
		if e.ReasoningDelta != "" {
			t.Fatalf("reasoning fence leaked: %q", e.ReasoningDelta)
		}
	}
}

func TestToolBridgeParserHoldsPartialFencePrefix(t *testing.T) {
	parser := newToolBridgeParser([]toolSpec{{Name: "read_file", Type: "function"}})
	events := parser.consume(codex.StreamEvent{Delta: "here ```cur"})
	var text string
	for _, e := range events {
		text += e.Delta
	}
	if text != "here " {
		t.Fatalf("expected partial fence held back, got %q", text)
	}
	events = parser.consume(codex.StreamEvent{Delta: "sor_tool_call\n{\"name\":\"read_file\",\"arguments\":{}}\n```"})
	var call *codex.ToolCallDelta
	for _, e := range events {
		if e.ToolCallDelta != nil {
			call = e.ToolCallDelta
		}
		text += e.Delta
	}
	if call == nil || call.Function.Name != "read_file" {
		t.Fatalf("expected resolved tool call, events=%#v", events)
	}
	if text != "here " {
		t.Fatalf("fence prefix leaked into text: %q", text)
	}
}

func TestToolBridgeParserTolerantOfSurroundingProse(t *testing.T) {
	parser := newToolBridgeParser([]toolSpec{{Name: "read_file", Type: "function"}})
	events := parser.consume(codex.StreamEvent{Delta: "```cursor_tool_call\nSure! {\"name\":\"read_file\",\"arguments\":{\"path\":\"x\"}} done\n```"})
	var call *codex.ToolCallDelta
	for _, e := range events {
		if e.ToolCallDelta != nil {
			call = e.ToolCallDelta
		}
	}
	if call == nil || call.Function.Name != "read_file" || call.Function.Arguments != `{"path":"x"}` {
		t.Fatalf("expected tolerant parse, events=%#v", events)
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
