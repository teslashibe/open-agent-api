package codex

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseStreamEventToolCallLifecycle(t *testing.T) {
	start, terminal, err := parseStreamEvent([]byte(`{
		"type":"response.output_item.added",
		"output_index":0,
		"item":{"id":"fc_123","type":"function_call","call_id":"call_123","name":"lookup","arguments":""}
	}`))
	if err != nil || terminal {
		t.Fatalf("start parse err=%v terminal=%t", err, terminal)
	}
	if start.ToolCallDelta == nil {
		t.Fatal("start ToolCallDelta = nil")
	}
	if got := *start.ToolCallDelta; got.Index != 0 || got.ID != "call_123" || got.Type != "function" || got.Function.Name != "lookup" || got.Function.Arguments != "" {
		t.Fatalf("start delta = %#v", got)
	}

	fragment, terminal, err := parseStreamEvent([]byte(`{
		"type":"response.function_call_arguments.delta",
		"output_index":0,
		"item_id":"fc_123",
		"delta":"{\"q\":"
	}`))
	if err != nil || terminal {
		t.Fatalf("fragment parse err=%v terminal=%t", err, terminal)
	}
	if fragment.ToolCallDelta == nil {
		t.Fatal("fragment ToolCallDelta = nil")
	}
	if got := *fragment.ToolCallDelta; got.Index != 0 || got.ID != "" || got.Type != "function" || got.Function.Arguments != `{"q":` {
		t.Fatalf("fragment delta = %#v", got)
	}

	done, terminal, err := parseStreamEvent([]byte(`{
		"type":"response.function_call_arguments.done",
		"output_index":0,
		"item_id":"fc_123",
		"arguments":"{\"q\":\"codex\"}"
	}`))
	if err != nil || terminal {
		t.Fatalf("done parse err=%v terminal=%t", err, terminal)
	}
	if done.ToolCallDelta == nil {
		t.Fatal("done ToolCallDelta = nil")
	}
	if got := *done.ToolCallDelta; got.Index != 0 || got.ID != "" || got.Type != "function" || got.Function.Arguments != `{"q":"codex"}` || !got.Final {
		t.Fatalf("done delta = %#v", got)
	}
}

func TestParseStreamEventToolCallCompatibilityShape(t *testing.T) {
	event, terminal, err := parseStreamEvent([]byte(`{
		"type":"response.tool_call.delta",
		"tool_calls":[{"id":"call_123","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"codex\"}"}}],
		"tool_call_delta":{"index":0,"id":"call_123","type":"function","function":{"name":"lookup","arguments":"{\"q\":"}}
	}`))
	if err != nil || terminal {
		t.Fatalf("parse err=%v terminal=%t", err, terminal)
	}
	if event.ToolCallDelta == nil {
		t.Fatal("ToolCallDelta = nil")
	}
	if event.ToolCallDelta.ID != "call_123" || event.ToolCallDelta.Function.Name != "lookup" || event.ToolCallDelta.Function.Arguments != `{"q":` {
		t.Fatalf("tool_call_delta = %#v", event.ToolCallDelta)
	}
}

func TestParseStreamEventOutputItemDoneEmitsFullToolCall(t *testing.T) {
	event, terminal, err := parseStreamEvent([]byte(`{
		"type":"response.output_item.done",
		"output_index":0,
		"item":{"id":"fc_123","type":"function_call","call_id":"call_123","name":"lookup","arguments":"{\"q\":\"codex\"}"}
	}`))
	if err != nil || terminal {
		t.Fatalf("parse err=%v terminal=%t", err, terminal)
	}
	if len(event.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1", len(event.ToolCalls))
	}
	toolCall := event.ToolCalls[0]
	if toolCall.ID != "call_123" || toolCall.Type != "function" || toolCall.Function.Name != "lookup" || toolCall.Function.Arguments != `{"q":"codex"}` {
		t.Fatalf("tool call = %#v", toolCall)
	}
}

func TestCompleteAggregatesParsedToolCallDeltas(t *testing.T) {
	events := make(chan StreamEvent, 4)
	events <- StreamEvent{ToolCallDelta: &ToolCallDelta{
		Index: 0,
		ID:    "call_123",
		Type:  "function",
		Function: ToolCallFunctionDelta{
			Name: "lookup",
		},
	}}
	events <- StreamEvent{ToolCallDelta: &ToolCallDelta{
		Index: 0,
		Function: ToolCallFunctionDelta{
			Arguments: `{"q":`,
		},
	}}
	events <- StreamEvent{ToolCallDelta: &ToolCallDelta{
		Index: 0,
		Function: ToolCallFunctionDelta{
			Arguments: `"codex"}`,
		},
	}}
	close(events)

	completion := Completion{}
	for event := range events {
		if event.ToolCallDelta != nil {
			applyToolCallDelta(&completion.ToolCalls, *event.ToolCallDelta)
		}
	}
	if len(completion.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1", len(completion.ToolCalls))
	}
	toolCall := completion.ToolCalls[0]
	if toolCall.ID != "call_123" || toolCall.Type != "function" || toolCall.Function.Name != "lookup" || toolCall.Function.Arguments != `{"q":"codex"}` {
		t.Fatalf("tool call = %#v", toolCall)
	}
}

func TestCodexToolEventLoggingIsRedacted(t *testing.T) {
	const secretArgs = "secret query"
	var logs bytes.Buffer
	client := &Client{logBodyShape: true, logOutput: &logs}

	client.logCodexToolEvent([]byte(`{"type":"response.function_call_arguments.delta","delta":"` + secretArgs + `"}`))
	if logs.Len() != 0 {
		t.Fatalf("body-shape logging wrote tool event %q", logs.String())
	}

	client.logToolEvents = true

	client.logCodexToolEvent([]byte(`{
		"type":"response.function_call_arguments.delta",
		"item_id":"fc_123",
		"delta":"` + secretArgs + `"
	}`))

	body := logs.String()
	if !strings.Contains(body, "codex_tool_event valid_json=true") ||
		!strings.Contains(body, "type=response.function_call_arguments.delta") ||
		!strings.Contains(body, "fields=delta,item_id,type") {
		t.Fatalf("logs = %q", body)
	}
	if strings.Contains(body, secretArgs) {
		t.Fatalf("logs leaked arguments: %q", body)
	}

	logs.Reset()
	client.logToolEvents = false
	client.logCodexToolEvent([]byte(`{"type":"response.function_call_arguments.delta","delta":"` + secretArgs + `"}`))
	if logs.Len() != 0 {
		t.Fatalf("debug-gated logging wrote %q", logs.String())
	}
}

func TestParseStreamEventPlainTextStillWorks(t *testing.T) {
	event, terminal, err := parseStreamEvent([]byte(`{"type":"response.output_text.delta","delta":"hello"}`))
	if err != nil || terminal {
		t.Fatalf("parse err=%v terminal=%t", err, terminal)
	}
	if event.Delta != "hello" || event.ReasoningDelta != "" || event.ToolCallDelta != nil || len(event.ToolCalls) != 0 {
		t.Fatalf("event = %#v", event)
	}
}

func TestParseStreamEventReasoningSummaryText(t *testing.T) {
	event, terminal, err := parseStreamEvent([]byte(`{"type":"response.reasoning_summary_text.delta","delta":"thinking"}`))
	if err != nil || terminal {
		t.Fatalf("parse err=%v terminal=%t", err, terminal)
	}
	if event.ReasoningDelta != "thinking" || event.Delta != "" {
		t.Fatalf("event = %#v", event)
	}
}
