package gemini

import (
	"encoding/json"
	"testing"

	"github.com/teslashibe/codex-chat-api/internal/codex"
	"github.com/teslashibe/codex-chat-api/internal/openai"
)

func TestBuildGenerateContentRequest(t *testing.T) {
	req := buildGenerateContentRequest(codex.Request{
		Model: "gemini-2.5-flash",
		Messages: []openai.ChatMessage{
			{Role: "system", Content: openai.TextContent("be brief")},
			{Role: "user", Content: openai.TextContent("hi")},
		},
		Tools: json.RawMessage(`[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","additionalProperties":false,"properties":{"q":{"type":"string"}}}}}]`),
	}, "project-1", "session-1")
	if req.Project != "project-1" || req.Request.SessionID != "session-1" {
		t.Fatalf("metadata not set: %#v", req)
	}
	if req.UserAgent != "antigravity" {
		t.Fatalf("userAgent = %q, want antigravity", req.UserAgent)
	}
	if req.RequestID == "" {
		t.Fatalf("requestId missing")
	}
	if req.Request.SystemInstruction == nil || req.Request.SystemInstruction.Parts[0].Text != "be brief" {
		t.Fatalf("system instruction missing: %#v", req.Request.SystemInstruction)
	}
	if len(req.Request.Tools) != 1 || len(req.Request.Tools[0].FunctionDeclarations) != 1 {
		t.Fatalf("tool declarations missing: %#v", req.Request.Tools)
	}
	if string(req.Request.Tools[0].FunctionDeclarations[0].Parameters) == "" {
		t.Fatalf("parameters missing")
	}
	if json.Valid(req.Request.Tools[0].FunctionDeclarations[0].Parameters) == false {
		t.Fatalf("parameters invalid json")
	}
}

func TestBuildGenerateContentRequestCustomTool(t *testing.T) {
	req := buildGenerateContentRequest(codex.Request{
		Model: "gemini-2.5-flash",
		Messages: []openai.ChatMessage{
			{Role: "assistant", ToolCalls: []openai.ToolCall{{
				ID:   "call_custom",
				Type: "custom",
				Custom: &openai.ToolCallCustom{
					Name:  "apply_patch",
					Input: "*** Begin Patch\n*** End Patch\n",
				},
			}}},
			{Role: "tool", ToolCallID: "call_custom", Content: openai.TextContent("applied")},
		},
		Tools: json.RawMessage(`[{"type":"custom","custom":{"name":"apply_patch","description":"apply patch"}}]`),
	}, "project-1", "session-1")

	if len(req.Request.Tools) != 1 || len(req.Request.Tools[0].FunctionDeclarations) != 1 {
		t.Fatalf("tool declarations missing: %#v", req.Request.Tools)
	}
	decl := req.Request.Tools[0].FunctionDeclarations[0]
	if decl.Name != "apply_patch" {
		t.Fatalf("decl name = %q", decl.Name)
	}
	if got := string(req.Request.Contents[0].Parts[0].FunctionCall.Args); got != `{"input":"*** Begin Patch\n*** End Patch\n"}` {
		t.Fatalf("custom args = %s", got)
	}
	if req.Request.Contents[1].Parts[0].FunctionResponse.Name != "apply_patch" {
		t.Fatalf("function response name = %q", req.Request.Contents[1].Parts[0].FunctionResponse.Name)
	}
}
