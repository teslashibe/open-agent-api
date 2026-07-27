package server

import (
	"testing"
)

func TestClassifyToolWire(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "none", raw: `[]`, want: toolWireNone},
		{name: "nested", raw: `[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]`, want: toolWireNested},
		{name: "flat", raw: `[{"type":"function","name":"lookup","parameters":{"type":"object"}}]`, want: toolWireFlat},
		{name: "mixed", raw: `[{"type":"function","function":{"name":"a"}},{"type":"function","name":"b","parameters":{"type":"object"}}]`, want: toolWireMixed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyToolWire([]byte(tc.raw)); got != tc.want {
				t.Fatalf("classifyToolWire() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClassifyToolChoice(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{raw: `"auto"`, want: `"auto"`},
		{raw: `"required"`, want: `"required"`},
		{raw: `{"type":"function","function":{"name":"lookup"}}`, want: "nested_function"},
		{raw: `{"type":"function","name":"lookup"}`, want: "flat_function"},
	}
	for _, tc := range tests {
		if got := classifyToolChoice([]byte(tc.raw)); got != tc.want {
			t.Fatalf("classifyToolChoice(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestSummarizeCursorWire(t *testing.T) {
	summary := summarizeCursorWire([]byte(`{
		"model":"gpt-test",
		"stream":true,
		"parallel_tool_calls":true,
		"metadata":{"cursorRequestId":"abc"},
		"user":"dev@local",
		"tool_choice":"auto",
		"tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}],
		"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"ok"}
		]
	}`))
	if summary.ToolWire != toolWireFlat {
		t.Fatalf("ToolWire = %q, want flat", summary.ToolWire)
	}
	if summary.ToolChoice != `"auto"` {
		t.Fatalf("ToolChoice = %q, want auto", summary.ToolChoice)
	}
	if !summary.HasMetadata || !summary.HasUser {
		t.Fatalf("summary = %#v, want metadata and user", summary)
	}
	if summary.AssistantTools != 1 || summary.ToolMessages != 1 {
		t.Fatalf("summary = %#v, want one assistant tool turn and one tool result", summary)
	}
}

func TestStreamResponseShapeLogFields(t *testing.T) {
	got := (streamResponseShape{Content: true, ReasoningContent: true, ToolCalls: true}).logFields()
	want := "response_content=true response_reasoning_content=true response_tool_calls=true"
	if got != want {
		t.Fatalf("logFields() = %q, want %q", got, want)
	}
}
