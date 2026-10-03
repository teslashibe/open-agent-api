package openai

import (
	"encoding/json"
	"strings"
	"testing"
)

// cursorAnthropicBody mirrors what Cursor sends for a claude-named BYOK model
// (top-level system, Anthropic tools, tool_use/tool_result blocks, thinking,
// cache_control, image tool results).
const cursorAnthropicBody = `{
  "model": "api/claude-opus-5-5-high",
  "max_tokens": 32000,
  "stream": true,
  "stream_options": {"include_usage": true},
  "metadata": {"user_id": "u"},
  "system": [{"type": "text", "text": "You are Cursor's agent.", "cache_control": {"type": "ephemeral"}}],
  "tools": [
    {"name": "Read", "description": "Read a file", "input_schema": {"type": "object", "properties": {"path": {"type": "string"}}}},
    {"type": "web_search_20250305", "name": "web_search", "max_uses": 5}
  ],
  "tool_choice": {"type": "auto", "disable_parallel_tool_use": true},
  "messages": [
    {"role": "user", "content": [{"type": "text", "text": "read the logo and the readme", "cache_control": {"type": "ephemeral"}}]},
    {"role": "assistant", "content": [
      {"type": "thinking", "thinking": "plan", "signature": "sig"},
      {"type": "text", "text": "Reading both."},
      {"type": "tool_use", "id": "toolu_1", "name": "Read", "input": {"path": "logo.png"}},
      {"type": "tool_use", "id": "toolu_2", "name": "Read", "input": {"path": "README.md"}}
    ]},
    {"role": "user", "content": [
      {"type": "tool_result", "tool_use_id": "toolu_1", "content": [{"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "AAAA"}}]},
      {"type": "tool_result", "tool_use_id": "toolu_2", "content": "# Zephyr", "is_error": false},
      {"type": "text", "text": "<system_reminder>keep going</system_reminder>"}
    ]}
  ]
}`

func TestNormalizeAnthropicDialectCursorRequest(t *testing.T) {
	var req ChatCompletionRequest
	if err := json.Unmarshal([]byte(cursorAnthropicBody), &req); err != nil {
		t.Fatal(err)
	}
	req.Messages = req.WithSystemMessage()
	if !NormalizeAnthropicDialect(&req) {
		t.Fatal("expected a change")
	}
	if !req.StreamOptions.IncludeUsage {
		t.Fatal("stream_options lost")
	}
	if string(req.Tools) != `[{"function":{"description":"Read a file","name":"Read","parameters":{"type":"object","properties":{"path":{"type":"string"}}}},"type":"function"}]` {
		t.Fatalf("tools = %s", req.Tools)
	}
	if string(req.ToolChoice) != `"auto"` || req.ParallelToolCalls == nil || *req.ParallelToolCalls {
		t.Fatalf("tool_choice = %s parallel = %v", req.ToolChoice, req.ParallelToolCalls)
	}
	roles := []string{}
	for _, m := range req.Messages {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, ",") != "system,user,assistant,tool,tool,user" {
		t.Fatalf("roles = %v", roles)
	}
	if MessageText(req.Messages[0].Content) != "You are Cursor's agent." {
		t.Fatalf("system = %s", req.Messages[0].Content)
	}
	if strings.Contains(string(req.Messages[1].Content), "cache_control") {
		t.Fatalf("cache_control kept: %s", req.Messages[1].Content)
	}
	assistant := req.Messages[2]
	if MessageText(assistant.Content) != "Reading both." || len(assistant.ToolCalls) != 2 || assistant.ToolCalls[1].ID != "toolu_2" || assistant.ToolCalls[1].Function.Arguments != `{"path":"README.md"}` {
		t.Fatalf("assistant = %+v", assistant)
	}
	if req.Messages[3].ToolCallID != "toolu_1" || !strings.Contains(string(req.Messages[3].Content), `"url":"data:image/png;base64,AAAA"`) {
		t.Fatalf("image tool result = %s", req.Messages[3].Content)
	}
	if req.Messages[4].ToolCallID != "toolu_2" || MessageText(req.Messages[4].Content) != "# Zephyr" {
		t.Fatalf("text tool result = %s", req.Messages[4].Content)
	}
	if !strings.Contains(MessageText(req.Messages[5].Content), "keep going") {
		t.Fatalf("trailing user text = %s", req.Messages[5].Content)
	}
	if NormalizeAnthropicDialect(&req) {
		t.Fatal("normalization must be idempotent")
	}
}

func TestNormalizeAnthropicDialectLeavesOpenAIRequestsAlone(t *testing.T) {
	body := `{"model":"gpt-5.6-sol","tools":[{"type":"function","function":{"name":"Read","parameters":{"type":"object"}}}],"tool_choice":{"type":"function","function":{"name":"Read"}},"messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"https://x/y.png"}}]},{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"Read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"ok"}]}`
	var req ChatCompletionRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(req)
	if NormalizeAnthropicDialect(&req) {
		t.Fatal("OpenAI request must not change")
	}
	after, _ := json.Marshal(req)
	if string(before) != string(after) {
		t.Fatalf("changed:\n%s\n%s", before, after)
	}
}

func TestNormalizeAnthropicToolChoiceVariants(t *testing.T) {
	cases := map[string]string{
		`{"type":"any"}`:                `"required"`,
		`{"type":"none"}`:               `"none"`,
		`{"type":"tool","name":"Read"}`: `{"function":{"name":"Read"},"type":"function"}`,
	}
	for in, want := range cases {
		req := ChatCompletionRequest{ToolChoice: json.RawMessage(in)}
		NormalizeAnthropicDialect(&req)
		if string(req.ToolChoice) != want {
			t.Fatalf("%s -> %s, want %s", in, req.ToolChoice, want)
		}
	}
}

func TestNormalizeAnthropicDropsThinkingOnlyAssistantAndMarksErrors(t *testing.T) {
	req := ChatCompletionRequest{Messages: []ChatMessage{
		{Role: "user", Content: TextContent("go")},
		{Role: "assistant", Content: json.RawMessage(`[{"type":"thinking","thinking":"x","signature":"s"}]`)},
		{Role: "user", Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"t1","content":"boom","is_error":true}]`)},
	}}
	NormalizeAnthropicDialect(&req)
	if len(req.Messages) != 2 || req.Messages[1].Role != "tool" || MessageText(req.Messages[1].Content) != "Error: boom" {
		t.Fatalf("messages = %+v", req.Messages)
	}
}
