package openai

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestChatCompletionRequestParsesToolCallFields(t *testing.T) {
	var req ChatCompletionRequest
	raw := []byte(`{
		"model":"gpt-test",
		"messages":[
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_123","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"codex\"}"}},{"id":"call_456","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"go.mod\"}"}}]},
			{"role":"tool","tool_call_id":"call_123","content":"result"},
			{"role":"tool","tool_call_id":"call_456","content":"module github.com/teslashibe/codex-chat-api"}
		],
		"tools":[{"type":"function","function":{"name":"lookup"}}],
		"tool_choice":{"type":"function","function":{"name":"lookup"}},
		"parallel_tool_calls":true
	}`)
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if !json.Valid(req.Tools) || !json.Valid(req.ToolChoice) {
		t.Fatalf("tool fields did not preserve raw JSON: tools=%s tool_choice=%s", req.Tools, req.ToolChoice)
	}
	if req.ParallelToolCalls == nil || !*req.ParallelToolCalls {
		t.Fatalf("ParallelToolCalls = %v, want true", req.ParallelToolCalls)
	}
	if got := string(req.Messages[0].Content); got != "null" {
		t.Fatalf("assistant content = %s, want null", got)
	}
	if len(req.Messages[0].ToolCalls) != 2 {
		t.Fatalf("tool_calls len = %d, want 2", len(req.Messages[0].ToolCalls))
	}
	toolCall := req.Messages[0].ToolCalls[0]
	if toolCall.ID != "call_123" || toolCall.Type != "function" || toolCall.Function.Name != "lookup" {
		t.Fatalf("tool call = %#v", toolCall)
	}
	if req.Messages[1].ToolCallID != "call_123" {
		t.Fatalf("tool_call_id = %q, want call_123", req.Messages[1].ToolCallID)
	}
	if req.Messages[2].ToolCallID != "call_456" {
		t.Fatalf("second tool_call_id = %q, want call_456", req.Messages[2].ToolCallID)
	}
}

func TestChatCompletionToolCallShapesMarshal(t *testing.T) {
	response := ChatCompletionResponse{
		ID:      "chatcmpl-test",
		Object:  "chat.completion",
		Created: 123,
		Model:   "gpt-test",
		Choices: []ChatCompletionChoice{
			{
				Index: 0,
				Message: ChatMessage{
					Role:    "assistant",
					Content: json.RawMessage("null"),
					ToolCalls: []ToolCall{
						{
							ID:   "call_123",
							Type: "function",
							Function: ToolCallFunction{
								Name:      "lookup",
								Arguments: `{"q":"codex"}`,
							},
						},
					},
				},
				FinishReason: "tool_calls",
			},
		},
	}
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Marshal(response) error = %v", err)
	}
	for _, want := range []string{
		`"content":null`,
		`"tool_calls":[{"id":"call_123","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"codex\"}"}}]`,
		`"finish_reason":"tool_calls"`,
	} {
		if !containsJSON(data, want) {
			t.Fatalf("response JSON = %s, want %s", data, want)
		}
	}

	chunks := []ChatCompletionChunk{
		{
			ID:      "chatcmpl-test",
			Object:  "chat.completion.chunk",
			Created: 123,
			Model:   "gpt-test",
			Choices: []ChatCompletionChunkChoice{
				{
					Index: 0,
					Delta: ChatDelta{
						ToolCalls: []ToolCallDelta{
							{
								Index: 0,
								ID:    "call_123",
								Type:  "function",
								Function: &ToolCallFunctionDelta{
									Name:      "lookup",
									Arguments: `{"q":`,
								},
							},
						},
					},
				},
			},
		},
		{
			ID:      "chatcmpl-test",
			Object:  "chat.completion.chunk",
			Created: 123,
			Model:   "gpt-test",
			Choices: []ChatCompletionChunkChoice{
				{
					Index: 0,
					Delta: ChatDelta{
						ToolCalls: []ToolCallDelta{
							{
								Index: 0,
								Function: &ToolCallFunctionDelta{
									Arguments: `"codex"}`,
								},
							},
						},
					},
				},
			},
		},
	}
	finish := "tool_calls"
	chunks = append(chunks, ChatCompletionChunk{
		ID:      "chatcmpl-test",
		Object:  "chat.completion.chunk",
		Created: 123,
		Model:   "gpt-test",
		Choices: []ChatCompletionChunkChoice{
			{Index: 0, Delta: ChatDelta{}, FinishReason: &finish},
		},
	})

	for _, chunk := range chunks {
		data, err := json.Marshal(chunk)
		if err != nil {
			t.Fatalf("Marshal(chunk) error = %v", err)
		}
		if !containsJSON(data, `"index":0`) {
			t.Fatalf("chunk JSON = %s, want tool call index", data)
		}
	}
}

func containsJSON(data []byte, want string) bool {
	return json.Valid(data) && strings.Contains(string(data), want)
}
