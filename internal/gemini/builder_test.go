package gemini

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/teslashibe/open-agent-api/internal/codex"
	"github.com/teslashibe/open-agent-api/internal/openai"
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

func TestConvertMessagesRestoresThoughtSignatureFromExtraAndCache(t *testing.T) {
	store := newThoughtSignatureStore(time.Minute)
	prev := thoughtSignatures
	thoughtSignatures = store
	t.Cleanup(func() { thoughtSignatures = prev })

	store.Remember("call_cached", "sig-from-cache")

	contents, _ := convertMessages([]openai.ChatMessage{
		{Role: "user", Content: openai.TextContent("hi")},
		{
			Role: "assistant",
			ToolCalls: []openai.ToolCall{
				{
					ID:           "call_extra",
					Type:         "function",
					Function:     openai.ToolCallFunction{Name: "lookup", Arguments: `{"q":"x"}`},
					ExtraContent: openai.GoogleThoughtSignatureExtra("sig-from-extra"),
				},
				{
					ID:       "call_cached",
					Type:     "function",
					Function: openai.ToolCallFunction{Name: "lookup", Arguments: `{}`},
				},
				{
					ID:       "call_missing",
					Type:     "function",
					Function: openai.ToolCallFunction{Name: "lookup", Arguments: `{}`},
				},
			},
		},
		{Role: "tool", ToolCallID: "call_extra", Content: openai.TextContent(`{"ok":true}`)},
	})

	if len(contents) < 2 {
		t.Fatalf("contents len = %d, want >= 2", len(contents))
	}
	parts := contents[1].Parts
	if len(parts) != 3 {
		t.Fatalf("assistant parts = %d, want 3", len(parts))
	}
	if parts[0].ThoughtSignature != "sig-from-extra" {
		t.Fatalf("extra signature = %q", parts[0].ThoughtSignature)
	}
	if parts[1].ThoughtSignature != "sig-from-cache" {
		t.Fatalf("cache signature = %q", parts[1].ThoughtSignature)
	}
	if parts[2].ThoughtSignature != skipThoughtSignatureValidator {
		t.Fatalf("fallback signature = %q, want skip validator", parts[2].ThoughtSignature)
	}
}

func TestGeminiToolCallDeltaRemembersThoughtSignature(t *testing.T) {
	store := newThoughtSignatureStore(time.Minute)
	prev := thoughtSignatures
	thoughtSignatures = store
	t.Cleanup(func() { thoughtSignatures = prev })

	delta := geminiToolCallDelta("resp1", 0, &functionCall{ID: "call_1", Name: "lookup", Args: json.RawMessage(`{}`)}, "opaque-sig", nil)
	if delta.ThoughtSignature != "opaque-sig" {
		t.Fatalf("delta signature = %q", delta.ThoughtSignature)
	}
	if got := store.Lookup("call_1"); got != "opaque-sig" {
		t.Fatalf("cache = %q, want opaque-sig", got)
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
