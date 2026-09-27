package claude

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/teslashibe/open-agent-api/internal/openai"
)

func readTool() *toolSet {
	return newToolSet(parseToolSpecs(json.RawMessage(`[
		{"type":"function","function":{"name":"Read","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}},
		{"type":"custom","custom":{"name":"apply_patch","description":"patch"}}
	]`)))
}

func fnCall(id, name, args string) openai.ToolCall {
	return openai.ToolCall{ID: id, Type: "function", Function: openai.ToolCallFunction{Name: name, Arguments: args}}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(data)
}

func TestBuildConversationSplitsSystemHistoryAndUserTail(t *testing.T) {
	conv := buildConversation([]openai.ChatMessage{
		{Role: "system", Content: openai.TextContent("cursor system")},
		{Role: "developer", Content: openai.TextContent("rules")},
		{Role: "user", Content: openai.TextContent("hi")},
		{Role: "assistant", Content: openai.TextContent("hello")},
		{Role: "user", Content: openai.TextContent("read go.mod")},
	}, readTool())
	if conv.System != "cursor system\n\nrules" {
		t.Fatalf("system = %q", conv.System)
	}
	if len(conv.History) != 2 || conv.History[0].Role != "user" || conv.History[1].Role != "assistant" {
		t.Fatalf("history = %s", mustJSON(t, conv.History))
	}
	if len(conv.Tail) != 1 || conv.Tail[0].Role != "user" || conv.Tail[0].Content[0]["text"] != "read go.mod" {
		t.Fatalf("tail = %s", mustJSON(t, conv.Tail))
	}
}

func TestBuildConversationToolContinuationTail(t *testing.T) {
	conv := buildConversation([]openai.ChatMessage{
		{Role: "user", Content: openai.TextContent("read both")},
		{Role: "assistant", ToolCalls: []openai.ToolCall{
			fnCall("call_1", "Read", `{"path":"a"}`),
			fnCall("call_2", "Read", `{"path":"b"}`),
		}},
		{Role: "tool", ToolCallID: "call_1", Content: openai.TextContent("A")},
		{Role: "tool", ToolCallID: "call_2", Content: openai.TextContent("B")},
	}, readTool())
	if len(conv.History) != 1 || len(conv.Tail) != 2 {
		t.Fatalf("history=%d tail=%d", len(conv.History), len(conv.Tail))
	}
	assistant, user := conv.Tail[0], conv.Tail[1]
	if assistant.Role != "assistant" || len(assistant.Content) != 2 {
		t.Fatalf("assistant tail = %s", mustJSON(t, assistant))
	}
	if assistant.Content[0]["name"] != "mcp__c__Read" || mustJSON(t, assistant.Content[0]["input"]) != `{"path":"a"}` {
		t.Fatalf("tool_use = %s", mustJSON(t, assistant.Content[0]))
	}
	if user.Role != "user" || len(user.Content) != 2 || user.Content[0]["tool_use_id"] != "call_1" || user.Content[1]["content"] != "B" {
		t.Fatalf("tool results = %s", mustJSON(t, user))
	}
}

func TestBuildConversationRepairsOrphansAndDuplicateIDs(t *testing.T) {
	conv := buildConversation([]openai.ChatMessage{
		{Role: "user", Content: openai.TextContent("go")},
		// call_x has no result (compacted away); the user interrupts.
		{Role: "assistant", ToolCalls: []openai.ToolCall{fnCall("call_x", "Read", `{"path":"a"}`)}},
		{Role: "user", Content: openai.TextContent("stop, do b")},
		// Legacy bridge reused IDs across turns.
		{Role: "assistant", ToolCalls: []openai.ToolCall{fnCall("call_x", "Read", `{"path":"b"}`)}},
		{Role: "tool", ToolCallID: "call_x", Content: openai.TextContent("B")},
		// A result nobody asked for.
		{Role: "tool", ToolCallID: "call_ghost", Content: openai.TextContent("??")},
	}, readTool())
	all := append(append([]turn{}, conv.History...), conv.Tail...)
	encoded := mustJSON(t, all)
	if !strings.Contains(encoded, `"tool_result unavailable"`) && !strings.Contains(encoded, `(tool result unavailable)`) {
		t.Fatalf("missing synthetic result: %s", encoded)
	}
	if !strings.Contains(encoded, `"id":"call_x_1"`) || !strings.Contains(encoded, `"tool_use_id":"call_x_1"`) {
		t.Fatalf("duplicate id not renamed with its result: %s", encoded)
	}
	if strings.Contains(encoded, `"tool_use_id":"call_ghost"`) || !strings.Contains(encoded, "Tool result (call_ghost)") {
		t.Fatalf("orphan result not converted to text: %s", encoded)
	}
	// Every tool_use is answered in the very next user turn, results first.
	for i, tn := range all {
		if tn.Role != "assistant" || !hasBlockType(tn, "tool_use") {
			continue
		}
		next := all[i+1]
		if next.Role != "user" || next.Content[0]["type"] != "tool_result" {
			t.Fatalf("turn %d not followed by tool_result first: %s", i, mustJSON(t, next))
		}
	}
}

func TestBuildConversationCustomToolAndOddIDs(t *testing.T) {
	longID := "call_" + strings.Repeat("z", 80)
	conv := buildConversation([]openai.ChatMessage{
		{Role: "user", Content: openai.TextContent("patch it")},
		{Role: "assistant", ToolCalls: []openai.ToolCall{{ID: longID, Type: "custom", Custom: &openai.ToolCallCustom{Name: "apply_patch", Input: "*** Begin Patch"}}}},
		{Role: "tool", ToolCallID: longID, Content: openai.TextContent("done")},
	}, readTool())
	use := conv.Tail[0].Content[0]
	result := conv.Tail[1].Content[0]
	if use["id"] != result["tool_use_id"] || !toolUseIDPattern.MatchString(use["id"].(string)) {
		t.Fatalf("ids not normalized consistently: %v / %v", use["id"], result["tool_use_id"])
	}
	if mustJSON(t, use["input"]) != `{"input":"*** Begin Patch"}` || use["name"] != "mcp__c__apply_patch" {
		t.Fatalf("custom tool_use = %s", mustJSON(t, use))
	}
}

func TestBuildConversationAcceptsPartsAndImages(t *testing.T) {
	conv := buildConversation([]openai.ChatMessage{
		{Role: "user", Content: json.RawMessage(`[{"type":"text","text":"what is this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]`)},
	}, nil)
	content := conv.Tail[0].Content
	if len(content) != 2 || content[1]["type"] != "image" || mustJSON(t, content[1]["source"]) != `{"data":"AAAA","media_type":"image/png","type":"base64"}` {
		t.Fatalf("content = %s", mustJSON(t, content))
	}
}

func TestBuildConversationEndsWithUser(t *testing.T) {
	conv := buildConversation([]openai.ChatMessage{
		{Role: "assistant", Content: openai.TextContent("prefill")},
	}, nil)
	if conv.History[0].Role != "user" || conv.Tail[0].Role != "user" {
		t.Fatalf("conversation must start and end with user: %s / %s", mustJSON(t, conv.History), mustJSON(t, conv.Tail))
	}
}

func TestToolSetNamesAndSchemas(t *testing.T) {
	long := strings.Repeat("x", 70)
	set := newToolSet(parseToolSpecs(json.RawMessage(`[
		{"type":"function","function":{"name":"Grep","parameters":{"$schema":"x","type":"object","properties":{"-A":{"type":"integer"}},"anyOf":[{}]}}},
		{"type":"function","function":{"name":"bad.name","parameters":{"type":"object"}}},
		{"type":"function","function":{"name":"` + long + `"}},
		{"name":"anthropic_style","input_schema":{"type":"object","properties":{}}}
	]`)))
	tools := set.bridgeTools()
	if len(tools) != 4 {
		t.Fatalf("tools = %d", len(tools))
	}
	if string(tools[0].InputSchema) != `{"properties":{"-A":{"type":"integer"}},"type":"object"}` {
		t.Fatalf("Grep schema = %s", tools[0].InputSchema)
	}
	for _, tool := range tools {
		if len(mcpPrefixed(tool.Name)) > maxToolNameLen || !toolNamePattern.MatchString(tool.Name) {
			t.Fatalf("invalid wire name %q", tool.Name)
		}
	}
	for _, name := range []string{"Grep", "bad.name", long, "anthropic_style"} {
		spec, ok := set.clientTool(set.modelName(name))
		if !ok || spec.Name != name {
			t.Fatalf("round trip %q -> %q (%v)", name, spec.Name, ok)
		}
	}
}

func mcpPrefixed(name string) string { return "mcp__c__" + name }
