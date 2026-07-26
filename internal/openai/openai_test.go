package openai

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestModelAliases(t *testing.T) {
	aliases := ModelAliases()
	got := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		got = append(got, alias.ID)
	}
	want := []string{
		"gpt-5.6-sol", "gpt-5.6",
		"gpt-5.6-sol-low", "gpt-5.6-sol-medium", "gpt-5.6-sol-high", "gpt-5.6-sol-xhigh", "gpt-5.6-sol-max",
		"gpt-5.6-terra", "gpt-5.6-terra-low", "gpt-5.6-terra-medium", "gpt-5.6-terra-high", "gpt-5.6-terra-xhigh", "gpt-5.6-terra-max",
		"gpt-5.6-luna", "gpt-5.6-luna-low", "gpt-5.6-luna-medium", "gpt-5.6-luna-high", "gpt-5.6-luna-xhigh", "gpt-5.6-luna-max",
		"gpt-5.6-luna-fast", "codex-sol", "codex-terra", "codex-luna",
		"gemini-2.5-flash", "gemini-2.5-flash-lite", "gemini-2.5-pro",
		"gemini-3.1-pro-low", "gemini-3.1-pro-high",
		"gemini-3.5-flash-low", "gemini-3.5-flash-medium", "gemini-3.5-flash-high",
		"gemini-3.1-flash-lite", "gemini-3-flash",
		"claude-sonnet-4-6", "claude-opus-4-6-thinking", "gpt-oss-120b-medium",
		"claude-opus-4-8", "claude-sonnet-5", "claude-haiku-4-5-20251001", "claude-fable-5",
		"opus", "sonnet", "haiku", "fable",
		"api/claude-opus-4-8", "api/claude-sonnet-5", "api/claude-haiku-4-5-20251001", "api/claude-fable-5",
		"api/claude-fable-5-low", "api/claude-fable-5-medium", "api/claude-fable-5-high",
		"gpt-5.5", "gpt-5.5-low", "gpt-5.5-high", "gpt-5.5-fast", "gpt-5.5-mini", "gpt-5.5-lite", "gpt-5.5-deep", "gpt-5.5-verbose", "gpt-5.5-fast-verbose",
		"gpt-5.3-codex-spark", "gpt-5.3-codex-spark-preview",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ModelAliases IDs = %#v, want %#v", got, want)
	}
	listed := ListedModelAliases()
	listedIDs := make([]string, 0, len(listed))
	for _, alias := range listed {
		listedIDs = append(listedIDs, alias.ID)
		if alias.Unlisted {
			t.Fatalf("ListedModelAliases included unlisted %q", alias.ID)
		}
	}
	if !reflect.DeepEqual(listedIDs, want) {
		t.Fatalf("ListedModelAliases IDs = %#v, want %#v", listedIDs, want)
	}

	aliases[0].ID = "mutated"
	if ModelAliases()[0].ID != DefaultModel {
		t.Fatal("ModelAliases returned mutable package state")
	}
}

func TestResolveModelAlias(t *testing.T) {
	tests := []struct {
		name          string
		model         string
		wantID        string
		wantUpstream  string
		wantEffort    string
		wantVerbosity string
	}{
		{
			name:          "empty model uses default",
			wantID:        DefaultModel,
			wantUpstream:  DefaultModel,
			wantEffort:    "low",
			wantVerbosity: "low",
		},
		{
			name:          "sol high alias",
			model:         "gpt-5.6-sol-high",
			wantID:        "gpt-5.6-sol-high",
			wantUpstream:  DefaultModel,
			wantEffort:    "high",
			wantVerbosity: "low",
		},
		{
			name:          "terra max alias",
			model:         "gpt-5.6-terra-max",
			wantID:        "gpt-5.6-terra-max",
			wantUpstream:  "gpt-5.6-terra",
			wantEffort:    "max",
			wantVerbosity: "low",
		},
		{
			name:          "luna fast alias",
			model:         "gpt-5.6-luna-fast",
			wantID:        "gpt-5.6-luna-fast",
			wantUpstream:  "gpt-5.6-luna",
			wantEffort:    "low",
			wantVerbosity: "low",
		},
		{
			name:          "codex-terra friendly alias",
			model:         "codex-terra",
			wantID:        "codex-terra",
			wantUpstream:  "gpt-5.6-terra",
			wantEffort:    DefaultReasoningEffort,
			wantVerbosity: "low",
		},
		{
			name:          "high alias",
			model:         "gpt-5.5-high",
			wantID:        "gpt-5.5-high",
			wantUpstream:  LegacyGPT55,
			wantEffort:    "high",
			wantVerbosity: DefaultVerbosity,
		},
		{
			name:          "fast alias",
			model:         "gpt-5.5-fast",
			wantID:        "gpt-5.5-fast",
			wantUpstream:  LegacyGPT55,
			wantEffort:    "low",
			wantVerbosity: "low",
		},
		{
			name:          "mini alias",
			model:         "gpt-5.5-mini",
			wantID:        "gpt-5.5-mini",
			wantUpstream:  LegacyGPT55,
			wantEffort:    "low",
			wantVerbosity: "low",
		},
		{
			name:          "lite alias",
			model:         "gpt-5.5-lite",
			wantID:        "gpt-5.5-lite",
			wantUpstream:  LegacyGPT55,
			wantEffort:    "low",
			wantVerbosity: DefaultVerbosity,
		},
		{
			name:          "deep alias",
			model:         "gpt-5.5-deep",
			wantID:        "gpt-5.5-deep",
			wantUpstream:  LegacyGPT55,
			wantEffort:    "high",
			wantVerbosity: DefaultVerbosity,
		},
		{
			name:          "verbose alias",
			model:         "gpt-5.5-verbose",
			wantID:        "gpt-5.5-verbose",
			wantUpstream:  LegacyGPT55,
			wantEffort:    DefaultReasoningEffort,
			wantVerbosity: "high",
		},
		{
			name:          "fast verbose alias",
			model:         "gpt-5.5-fast-verbose",
			wantID:        "gpt-5.5-fast-verbose",
			wantUpstream:  LegacyGPT55,
			wantEffort:    "low",
			wantVerbosity: "high",
		},
		{
			name:          "antigravity pro high remaps to gemini-pro-agent",
			model:         "gemini-3.1-pro-high",
			wantID:        "gemini-3.1-pro-high",
			wantUpstream:  "gemini-pro-agent",
			wantEffort:    DefaultReasoningEffort,
			wantVerbosity: DefaultVerbosity,
		},
		{
			name:          "antigravity flash medium remaps to flash-low wire id",
			model:         "gemini-3.5-flash-medium",
			wantID:        "gemini-3.5-flash-medium",
			wantUpstream:  "gemini-3.5-flash-low",
			wantEffort:    DefaultReasoningEffort,
			wantVerbosity: DefaultVerbosity,
		},
		{
			name:          "spark alias",
			model:         "gpt-5.3-codex-spark",
			wantID:        "gpt-5.3-codex-spark",
			wantUpstream:  "gpt-5.3-codex-spark",
			wantEffort:    "low",
			wantVerbosity: "low",
		},
		{
			name:          "spark preview alias",
			model:         "gpt-5.3-codex-spark-preview",
			wantID:        "gpt-5.3-codex-spark-preview",
			wantUpstream:  "gpt-5.3-codex-spark",
			wantEffort:    "low",
			wantVerbosity: "low",
		},
		{
			name:          "claude fable high alias",
			model:         "api/claude-fable-5-high",
			wantID:        "api/claude-fable-5-high",
			wantUpstream:  "claude-fable-5",
			wantEffort:    "high",
			wantVerbosity: DefaultVerbosity,
		},
		{
			name:          "unknown model passes through",
			model:         "gpt-test",
			wantID:        "gpt-test",
			wantUpstream:  "gpt-test",
			wantEffort:    DefaultReasoningEffort,
			wantVerbosity: DefaultVerbosity,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			alias := ResolveModelAlias(tc.model)
			if alias.ID != tc.wantID ||
				alias.UpstreamModel != tc.wantUpstream ||
				alias.ReasoningEffort != tc.wantEffort ||
				alias.Verbosity != tc.wantVerbosity {
				t.Fatalf("ResolveModelAlias(%q) = %#v", tc.model, alias)
			}
		})
	}
}

func TestChatCompletionRequestParsesToolCallFields(t *testing.T) {
	var req ChatCompletionRequest
	raw := []byte(`{
		"model":"gpt-test",
		"messages":[
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_123","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"codex\"}"}},{"id":"call_456","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"go.mod\"}"}}]},
			{"role":"tool","tool_call_id":"call_123","content":"result"},
			{"role":"tool","tool_call_id":"call_456","content":"module github.com/teslashibe/open-chat-api"}
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

func TestMessageTextStructuredContent(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{name: "plain string", raw: TextContent("hello"), want: "hello"},
		{name: "text parts", raw: []byte(`[{"type":"text","text":"hello "},{"type":"text","text":"world"}]`), want: "hello world"},
		{name: "input and output parts", raw: []byte(`[{"type":"input_text","text":"alpha"},{"type":"output_text","text":"beta"}]`), want: "alphabeta"},
		{name: "content string field", raw: []byte(`{"type":"text","content":"nested"}`), want: "nested"},
		{name: "nested content array", raw: []byte(`{"content":[{"text":"one"},{"text":"two"}]}`), want: "onetwo"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := MessageText(tc.raw); got != tc.want {
				t.Fatalf("MessageText() = %q, want %q", got, tc.want)
			}
		})
	}
}
