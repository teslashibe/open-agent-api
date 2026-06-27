package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/teslashibe/codex-chat-api/internal/codex"
	"github.com/teslashibe/codex-chat-api/internal/config"
	"github.com/teslashibe/codex-chat-api/internal/openai"
)

func agentQueueTestConfig() config.Config {
	cfg := config.Defaults()
	cfg.DegenerateTurnRetryEnabled = false
	return cfg
}

func TestHealth(t *testing.T) {
	app := New(config.Defaults())

	req, err := http.NewRequest(http.MethodGet, "/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("status body = %q, want ok", body["status"])
	}
}

func TestModels(t *testing.T) {
	app := New(config.Defaults(), WithLogOutput(io.Discard))

	req, err := http.NewRequest(http.MethodGet, "/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var body openai.ModelListResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Object != "list" || len(body.Data) != 4 {
		t.Fatalf("unexpected model list: %#v", body)
	}
	wantIDs := []string{"gpt-5.5", "gpt-5.5-low", "gpt-5.5-high", "gpt-5.5-fast"}
	for i, wantID := range wantIDs {
		model := body.Data[i]
		if model.ID != wantID || model.Object != "model" || model.Created != 0 || model.OwnedBy != "codex-chat-api" {
			t.Fatalf("model[%d] = %#v, want id %q", i, model, wantID)
		}
	}
}

func TestChatCompletionsModelAliases(t *testing.T) {
	tests := []struct {
		name            string
		body            string
		wantModel       string
		wantEffort      string
		wantVerbosity   string
		wantTools       bool
		wantFaithful    bool
		wantParallelSet bool
	}{
		{
			name:          "high alias",
			body:          `{"model":"gpt-5.5-high","messages":[{"role":"user","content":"hi"}]}`,
			wantModel:     "gpt-5.5",
			wantEffort:    "high",
			wantVerbosity: "medium",
			wantFaithful:  true,
		},
		{
			name:          "low alias",
			body:          `{"model":"gpt-5.5-low","messages":[{"role":"user","content":"hi"}]}`,
			wantModel:     "gpt-5.5",
			wantEffort:    "low",
			wantVerbosity: "medium",
			wantFaithful:  true,
		},
		{
			name:          "fast alias",
			body:          `{"model":"gpt-5.5-fast","messages":[{"role":"user","content":"hi"}]}`,
			wantModel:     "gpt-5.5",
			wantEffort:    "low",
			wantVerbosity: "low",
			wantFaithful:  true,
		},
		{
			name:          "explicit fields override alias defaults",
			body:          `{"model":"gpt-5.5-fast","reasoning_effort":"high","verbosity":"high","messages":[{"role":"user","content":"hi"}]}`,
			wantModel:     "gpt-5.5",
			wantEffort:    "high",
			wantVerbosity: "high",
			wantFaithful:  true,
		},
		{
			name:          "unknown model passes through",
			body:          `{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}`,
			wantModel:     "gpt-test",
			wantEffort:    "medium",
			wantVerbosity: "medium",
			wantFaithful:  true,
		},
		{
			name:            "alias keeps client tools in minimal mode",
			body:            `{"model":"gpt-5.5-high","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup"}}],"parallel_tool_calls":true}`,
			wantModel:       "gpt-5.5",
			wantEffort:      "high",
			wantVerbosity:   "medium",
			wantTools:       true,
			wantFaithful:    false,
			wantParallelSet: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service := fakeCodexService{
				complete: func(ctx context.Context, req codex.Request) (codex.Completion, error) {
					if req.Model != tc.wantModel {
						t.Fatalf("Model = %q, want %q", req.Model, tc.wantModel)
					}
					if req.ReasoningEffort != tc.wantEffort {
						t.Fatalf("ReasoningEffort = %q, want %q", req.ReasoningEffort, tc.wantEffort)
					}
					if req.Verbosity != tc.wantVerbosity {
						t.Fatalf("Verbosity = %q, want %q", req.Verbosity, tc.wantVerbosity)
					}
					if rawJSONPresent(req.Tools) != tc.wantTools {
						t.Fatalf("tools present = %t, want %t", rawJSONPresent(req.Tools), tc.wantTools)
					}
					if req.Faithful != tc.wantFaithful {
						t.Fatalf("Faithful = %t, want %t", req.Faithful, tc.wantFaithful)
					}
					if (req.ParallelToolCalls != nil) != tc.wantParallelSet {
						t.Fatalf("ParallelToolCalls = %v, want set %t", req.ParallelToolCalls, tc.wantParallelSet)
					}
					return codex.Completion{Text: "ok", Model: req.Model}, nil
				},
			}
			app := New(config.Defaults(), WithCodexService(service), fixedServerOptions())

			resp := doJSON(t, app, tc.body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
			}
		})
	}
}

func TestChatCompletionsStreamingModelAlias(t *testing.T) {
	service := fakeCodexService{
		stream: func(ctx context.Context, req codex.Request) (<-chan codex.StreamEvent, error) {
			if req.Model != "gpt-5.5" {
				t.Fatalf("Model = %q, want gpt-5.5", req.Model)
			}
			if req.ReasoningEffort != "low" {
				t.Fatalf("ReasoningEffort = %q, want low", req.ReasoningEffort)
			}
			if req.Verbosity != "low" {
				t.Fatalf("Verbosity = %q, want low", req.Verbosity)
			}
			events := make(chan codex.StreamEvent, 2)
			events <- codex.StreamEvent{Delta: "ok"}
			events <- codex.StreamEvent{Done: true}
			close(events)
			return events, nil
		},
	}
	var logs bytes.Buffer
	app := New(config.Defaults(), WithCodexService(service), WithLogOutput(&logs), fixedServerOptions())

	resp := doJSON(t, app, `{"model":"gpt-5.5-fast","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body := readString(t, resp.Body)
	if !strings.Contains(body, `"content":"ok"`) || !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("stream = %q, want unchanged SSE response", body)
	}
}

func TestChatCompletionsNonStreamingSuccess(t *testing.T) {
	service := fakeCodexService{
		complete: func(ctx context.Context, req codex.Request) (codex.Completion, error) {
			if req.Model != "gpt-test" {
				t.Fatalf("Model = %q, want gpt-test", req.Model)
			}
			if len(req.Messages) != 1 || req.Messages[0].Role != "user" {
				t.Fatalf("Messages = %#v", req.Messages)
			}
			return codex.Completion{
				ID:    "chatcmpl-test",
				Model: req.Model,
				Text:  "hello from fake",
				Usage: openai.Usage{
					PromptTokens:     2,
					CompletionTokens: 3,
					TotalTokens:      5,
				},
			}, nil
		},
	}
	var logs bytes.Buffer
	app := New(config.Defaults(), WithCodexService(service), WithLogOutput(&logs), fixedServerOptions())

	resp := doJSON(t, app, `{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var body openai.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.ID != "chatcmpl-test" || body.Object != "chat.completion" || body.Model != "gpt-test" {
		t.Fatalf("unexpected response metadata: %#v", body)
	}
	if got := string(body.Choices[0].Message.Content); got != `"hello from fake"` {
		t.Fatalf("message content = %s, want quoted hello", got)
	}
	if body.Usage.TotalTokens != 5 {
		t.Fatalf("usage = %#v", body.Usage)
	}
	logBody := logs.String()
	for _, want := range []string{
		"request_timing request_id=chatcmpl-fixed",
		"context_ms=0",
		"queue_wait_ms=0",
		"upstream_stream_ms=0",
		"first_delta_ms=-1",
		"total_ms=0",
	} {
		if !strings.Contains(logBody, want) {
			t.Fatalf("logs = %q, want %q", logBody, want)
		}
	}
}

func TestChatCompletionsNonStreamingToolCalls(t *testing.T) {
	service := fakeCodexService{
		complete: func(ctx context.Context, req codex.Request) (codex.Completion, error) {
			if !json.Valid(req.Tools) || !strings.Contains(string(req.Tools), `"name":"lookup"`) || !strings.Contains(string(req.Tools), `"description":"Look up things"`) || !strings.Contains(string(req.Tools), `"parameters"`) {
				t.Fatalf("Tools = %s", req.Tools)
			}
			if !json.Valid(req.ToolChoice) || !strings.Contains(string(req.ToolChoice), `"name":"lookup"`) {
				t.Fatalf("ToolChoice = %s", req.ToolChoice)
			}
			if req.ParallelToolCalls == nil || !*req.ParallelToolCalls {
				t.Fatalf("ParallelToolCalls = %v, want true", req.ParallelToolCalls)
			}
			if req.Faithful {
				t.Fatal("Faithful = true, want false when client tools are present by default")
			}
			return codex.Completion{
				ID:    "chatcmpl-tool",
				Model: req.Model,
				ToolCalls: []codex.ToolCall{
					{
						ID:   "call_123",
						Type: "function",
						Function: codex.ToolCallFunction{
							Name:      "lookup",
							Arguments: `{"q":"codex"}`,
						},
					},
				},
			}, nil
		},
	}
	var logs bytes.Buffer
	app := New(config.Defaults(), WithCodexService(service), WithLogOutput(&logs), fixedServerOptions())

	resp := doJSON(t, app, `{"model":"gpt-test","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup","description":"Look up things","parameters":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}}}],"tool_choice":{"type":"function","function":{"name":"lookup"}},"parallel_tool_calls":true}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var body openai.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	choice := body.Choices[0]
	if choice.FinishReason != "tool_calls" {
		t.Fatalf("finish_reason = %q, want tool_calls", choice.FinishReason)
	}
	if got := string(choice.Message.Content); got != "null" {
		t.Fatalf("message content = %s, want null", got)
	}
	if len(choice.Message.ToolCalls) != 1 {
		t.Fatalf("tool_calls len = %d, want 1", len(choice.Message.ToolCalls))
	}
	toolCall := choice.Message.ToolCalls[0]
	if toolCall.ID != "call_123" || toolCall.Type != "function" || toolCall.Function.Name != "lookup" || toolCall.Function.Arguments != `{"q":"codex"}` {
		t.Fatalf("tool call = %#v", toolCall)
	}
}

func TestChatCompletionsNonStreamingToolResultContinuation(t *testing.T) {
	service := fakeCodexService{
		complete: func(ctx context.Context, req codex.Request) (codex.Completion, error) {
			if len(req.Messages) != 3 {
				t.Fatalf("Messages len = %d, want 3", len(req.Messages))
			}
			assistant := req.Messages[1]
			if assistant.Role != "assistant" || len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].ID != "call_123" {
				t.Fatalf("assistant continuation message = %#v", assistant)
			}
			tool := req.Messages[2]
			if tool.Role != "tool" || tool.ToolCallID != "call_123" || string(tool.Content) != `"module github.com/teslashibe/codex-chat-api"` {
				t.Fatalf("tool continuation message = %#v", tool)
			}
			return codex.Completion{
				ID:    "chatcmpl-final",
				Model: req.Model,
				Text:  "go.mod declares module github.com/teslashibe/codex-chat-api.",
			}, nil
		},
	}
	app := New(config.Defaults(), WithCodexService(service), fixedServerOptions())

	resp := doJSON(t, app, `{"model":"gpt-test","messages":[{"role":"user","content":"read go.mod"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_123","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"go.mod\"}"}}]},{"role":"tool","tool_call_id":"call_123","content":"module github.com/teslashibe/codex-chat-api"}],"tools":[{"type":"function","function":{"name":"read_file"}}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var body openai.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	choice := body.Choices[0]
	if choice.FinishReason != "stop" {
		t.Fatalf("finish_reason = %q, want stop", choice.FinishReason)
	}
	if len(choice.Message.ToolCalls) != 0 {
		t.Fatalf("tool_calls = %#v, want none", choice.Message.ToolCalls)
	}
	if got := string(choice.Message.Content); got != `"go.mod declares module github.com/teslashibe/codex-chat-api."` {
		t.Fatalf("message content = %s", got)
	}
}

func TestChatCompletionsNonStreamingSequentialToolResultContinuation(t *testing.T) {
	service := fakeCodexService{
		complete: func(ctx context.Context, req codex.Request) (codex.Completion, error) {
			if len(req.Messages) != 5 {
				t.Fatalf("Messages len = %d, want 5", len(req.Messages))
			}
			want := []struct {
				index  int
				role   string
				callID string
			}{
				{index: 1, role: "assistant", callID: "call_list"},
				{index: 2, role: "tool", callID: "call_list"},
				{index: 3, role: "assistant", callID: "call_read"},
				{index: 4, role: "tool", callID: "call_read"},
			}
			for _, tc := range want {
				msg := req.Messages[tc.index]
				if msg.Role != tc.role {
					t.Fatalf("Messages[%d].Role = %q, want %q", tc.index, msg.Role, tc.role)
				}
				if tc.role == "assistant" {
					if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != tc.callID {
						t.Fatalf("assistant Messages[%d] = %#v, want call %s", tc.index, msg, tc.callID)
					}
					continue
				}
				if msg.ToolCallID != tc.callID {
					t.Fatalf("tool Messages[%d].ToolCallID = %q, want %q", tc.index, msg.ToolCallID, tc.callID)
				}
			}
			return codex.Completion{
				ID:    "chatcmpl-final",
				Model: req.Model,
				Text:  "Found README.md and go.mod; go.mod declares module github.com/teslashibe/codex-chat-api.",
			}, nil
		},
	}
	app := New(config.Defaults(), WithCodexService(service), fixedServerOptions())

	resp := doJSON(t, app, `{"model":"gpt-test","messages":[{"role":"user","content":"list files then read go.mod"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_list","type":"function","function":{"name":"list_dir","arguments":"{\"path\":\".\"}"}}]},{"role":"tool","tool_call_id":"call_list","content":"README.md\ngo.mod"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_read","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"go.mod\"}"}}]},{"role":"tool","tool_call_id":"call_read","content":"module github.com/teslashibe/codex-chat-api"}],"tools":[{"type":"function","function":{"name":"list_dir"}},{"type":"function","function":{"name":"read_file"}}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var body openai.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	choice := body.Choices[0]
	if choice.FinishReason != "stop" {
		t.Fatalf("finish_reason = %q, want stop", choice.FinishReason)
	}
	if len(choice.Message.ToolCalls) != 0 {
		t.Fatalf("tool_calls = %#v, want none", choice.Message.ToolCalls)
	}
	if got := string(choice.Message.Content); !strings.Contains(got, "README.md") || !strings.Contains(got, "github.com/teslashibe/codex-chat-api") {
		t.Fatalf("message content = %s, want final answer with real tool outputs", got)
	}
}

func TestChatCompletionsManagesToolPresentContext(t *testing.T) {
	const secretToolOutput = "secret-tool-output"
	service := fakeCodexService{
		complete: func(ctx context.Context, req codex.Request) (codex.Completion, error) {
			if len(req.Messages) != 3 {
				t.Fatalf("Messages len = %d, want 3", len(req.Messages))
			}
			assistant := req.Messages[1]
			if assistant.Role != "assistant" || len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].ID != "call_123" {
				t.Fatalf("assistant message = %#v, want preserved tool call", assistant)
			}
			tool := req.Messages[2]
			if tool.Role != "tool" || tool.ToolCallID != "call_123" {
				t.Fatalf("tool message = %#v, want preserved tool_call_id", tool)
			}
			text := openai.MessageText(tool.Content)
			if !strings.Contains(text, "tool output truncated from") {
				t.Fatalf("tool content = %q, want truncation marker", text)
			}
			if strings.Contains(text, secretToolOutput) {
				t.Fatalf("tool content leaked untruncated secret output: %q", text)
			}
			return codex.Completion{Text: "ok", Model: req.Model}, nil
		},
	}
	cfg := config.Defaults()
	cfg.ContextManagementEnabled = true
	cfg.ContextToolOutputMaxBytes = 70
	cfg.ContextMaxBytes = 0
	cfg.ContextMaxMessages = 0
	var logs bytes.Buffer
	app := New(cfg, WithCodexService(service), WithLogOutput(&logs), fixedServerOptions())

	body := mustJSON(t, openai.ChatCompletionRequest{
		Model: "gpt-test",
		Messages: []openai.ChatMessage{
			{Role: "user", Content: openai.TextContent("read")},
			{Role: "assistant", Content: json.RawMessage("null"), ToolCalls: []openai.ToolCall{{ID: "call_123", Type: "function", Function: openai.ToolCallFunction{Name: "read_file", Arguments: `{"path":"big.txt"}`}}}},
			{Role: "tool", ToolCallID: "call_123", Content: openai.TextContent(strings.Repeat(secretToolOutput, 20))},
		},
		Tools: json.RawMessage(`[{"type":"function","function":{"name":"read_file"}}]`),
	})
	resp := doJSON(t, app, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	logBody := logs.String()
	for _, want := range []string{
		"context_manage request_id=chatcmpl-fixed",
		"before_messages=3",
		"after_messages=3",
		"truncated_tools=1",
		"compacted_tools=0",
	} {
		if !strings.Contains(logBody, want) {
			t.Fatalf("logs = %q, want %q", logBody, want)
		}
	}
	if strings.Contains(logBody, secretToolOutput) || strings.Contains(logBody, "big.txt") {
		t.Fatalf("context management logs leaked request content: %q", logBody)
	}
}

func TestChatCompletionsContextManagementSkipsPlainRequests(t *testing.T) {
	const toolLikeSecret = "tool-like-secret"
	service := fakeCodexService{
		complete: func(ctx context.Context, req codex.Request) (codex.Completion, error) {
			if got := openai.MessageText(req.Messages[1].Content); got != strings.Repeat(toolLikeSecret, 10) {
				t.Fatalf("plain request message changed: %q", got)
			}
			return codex.Completion{Text: "ok", Model: req.Model}, nil
		},
	}
	cfg := config.Defaults()
	cfg.ContextManagementEnabled = true
	cfg.ContextToolOutputMaxBytes = 10
	var logs bytes.Buffer
	app := New(cfg, WithCodexService(service), WithLogOutput(&logs), fixedServerOptions())

	body := mustJSON(t, openai.ChatCompletionRequest{
		Model: "gpt-test",
		Messages: []openai.ChatMessage{
			{Role: "user", Content: openai.TextContent("hi")},
			{Role: "tool", ToolCallID: "call_123", Content: openai.TextContent(strings.Repeat(toolLikeSecret, 10))},
		},
	})
	resp := doJSON(t, app, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if strings.Contains(logs.String(), "context_manage") {
		t.Fatalf("logs = %q, want no context management log for plain request", logs.String())
	}
}

func TestChatCompletionsAcceptsArbitraryAuthorization(t *testing.T) {
	var called bool
	service := fakeCodexService{
		complete: func(ctx context.Context, req codex.Request) (codex.Completion, error) {
			called = true
			return codex.Completion{Text: "ok", Model: req.Model}, nil
		},
	}
	var logs bytes.Buffer
	app := New(config.Defaults(), WithCodexService(service), WithLogOutput(&logs), fixedServerOptions())

	req, err := http.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer local-codex-chat-api")
	resp, err := app.Test(req, 2000)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if !called {
		t.Fatal("codex service was not called")
	}
	logBody := logs.String()
	if !strings.Contains(logBody, "authorization_present=true") {
		t.Fatalf("logs = %q, want authorization presence", logBody)
	}
	if strings.Contains(logBody, "local-codex-chat-api") {
		t.Fatalf("logs leaked authorization value: %q", logBody)
	}
}

func TestChatCompletionsStreamingSuccess(t *testing.T) {
	service := fakeCodexService{
		stream: func(ctx context.Context, req codex.Request) (<-chan codex.StreamEvent, error) {
			events := make(chan codex.StreamEvent, 3)
			events <- codex.StreamEvent{Delta: "Hel"}
			events <- codex.StreamEvent{Delta: "lo"}
			events <- codex.StreamEvent{Done: true}
			close(events)
			return events, nil
		},
	}
	app := New(config.Defaults(), WithCodexService(service), fixedServerOptions())

	resp := doJSON(t, app, `{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	body := string(data)
	for _, event := range strings.Split(strings.TrimSuffix(body, "\n\n"), "\n\n") {
		if !strings.HasPrefix(event, "data: ") {
			t.Fatalf("SSE event missing data prefix: %q in body %q", event, body)
		}
	}
	if !strings.Contains(body, `"role":"assistant"`) {
		t.Fatalf("stream missing assistant role chunk: %q", body)
	}
	if !strings.Contains(body, `"content":"Hel"`) || !strings.Contains(body, `"content":"lo"`) {
		t.Fatalf("stream missing content chunks: %q", body)
	}
	if !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("stream = %q, want terminal DONE event", body)
	}
}

func TestChatCompletionsStreamingToolCalls(t *testing.T) {
	service := fakeCodexService{
		stream: func(ctx context.Context, req codex.Request) (<-chan codex.StreamEvent, error) {
			events := make(chan codex.StreamEvent, 4)
			events <- codex.StreamEvent{
				ToolCallDelta: &codex.ToolCallDelta{
					Index: 0,
					ID:    "call_123",
					Type:  "function",
					Function: codex.ToolCallFunctionDelta{
						Name:      "lookup",
						Arguments: `{"q":`,
					},
				},
			}
			events <- codex.StreamEvent{
				ToolCallDelta: &codex.ToolCallDelta{
					Index: 0,
					Function: codex.ToolCallFunctionDelta{
						Arguments: `"codex"}`,
					},
				},
			}
			events <- codex.StreamEvent{Done: true}
			close(events)
			return events, nil
		},
	}
	app := New(config.Defaults(), WithCodexService(service), fixedServerOptions())

	resp := doJSON(t, app, `{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	body := readString(t, resp.Body)
	for _, want := range []string{
		`"role":"assistant"`,
		`"tool_calls":[{"index":0,"id":"call_123","type":"function","function":{"name":"lookup","arguments":"{\"q\":"}}]`,
		`"tool_calls":[{"index":0,"function":{"arguments":"\"codex\"}"}}]`,
		`"finish_reason":"tool_calls"`,
		"data: [DONE]\n\n",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream = %q, want %q", body, want)
		}
	}
}

func TestChatCompletionsStreamingMapsAgentTextToReasoningContent(t *testing.T) {
	service := fakeCodexService{
		stream: func(ctx context.Context, req codex.Request) (<-chan codex.StreamEvent, error) {
			events := make(chan codex.StreamEvent, 5)
			events <- codex.StreamEvent{Delta: "I'll inspect the repo now."}
			events <- codex.StreamEvent{
				ToolCallDelta: &codex.ToolCallDelta{
					Index: 0,
					ID:    "call_123",
					Type:  "function",
					Function: codex.ToolCallFunctionDelta{
						Name:      "lookup",
						Arguments: `{"q":"codex"}`,
					},
				},
			}
			events <- codex.StreamEvent{Done: true}
			close(events)
			return events, nil
		},
	}
	cfg := config.Defaults()
	cfg.DegenerateTurnRetryEnabled = false
	app := New(cfg, WithCodexService(service), fixedServerOptions())

	resp := doJSON(t, app, `{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	body := readString(t, resp.Body)
	if strings.Contains(body, `"content":"I'll inspect the repo now."`) || strings.Contains(body, `"content":"I`) {
		t.Fatalf("stream leaked agent thinking into content: %q", body)
	}
	if !strings.Contains(body, `"reasoning_content":"I'll inspect the repo now."`) {
		t.Fatalf("stream = %q, want reasoning_content chunk", body)
	}
	if !strings.Contains(body, `"tool_calls"`) || !strings.Contains(body, `"finish_reason":"tool_calls"`) {
		t.Fatalf("stream = %q, want tool_calls finish", body)
	}
}

func TestChatCompletionsStreamingSkipsCompletedToolCallAfterDeltas(t *testing.T) {
	service := fakeCodexService{
		stream: func(ctx context.Context, req codex.Request) (<-chan codex.StreamEvent, error) {
			events := make(chan codex.StreamEvent, 4)
			events <- codex.StreamEvent{
				ToolCallDelta: &codex.ToolCallDelta{
					Index: 0,
					ID:    "call_123",
					Type:  "function",
					Function: codex.ToolCallFunctionDelta{
						Name:      "lookup",
						Arguments: `{"q":`,
					},
				},
			}
			events <- codex.StreamEvent{
				ToolCallDelta: &codex.ToolCallDelta{
					Index: 0,
					Function: codex.ToolCallFunctionDelta{
						Arguments: `"codex"}`,
					},
				},
			}
			events <- codex.StreamEvent{
				ToolCalls: []codex.ToolCall{
					{
						ID:   "call_123",
						Type: "function",
						Function: codex.ToolCallFunction{
							Name:      "lookup",
							Arguments: `{"q":"codex"}`,
						},
					},
				},
			}
			events <- codex.StreamEvent{Done: true}
			close(events)
			return events, nil
		},
	}
	app := New(config.Defaults(), WithCodexService(service), fixedServerOptions())

	resp := doJSON(t, app, `{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	body := readString(t, resp.Body)
	for _, want := range []string{
		`"tool_calls":[{"index":0,"id":"call_123","type":"function","function":{"name":"lookup","arguments":"{\"q\":"}}]`,
		`"tool_calls":[{"index":0,"function":{"arguments":"\"codex\"}"}}]`,
		`"finish_reason":"tool_calls"`,
		"data: [DONE]\n\n",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream = %q, want %q", body, want)
		}
	}
	if strings.Contains(body, `"arguments":"{\"q\":\"codex\"}"`) {
		t.Fatalf("stream = %q, want completed tool call suppressed after deltas", body)
	}
	if got := strings.Count(body, `"delta":{"tool_calls"`); got != 2 {
		t.Fatalf("tool_calls chunks = %d, want 2 in stream %q", got, body)
	}
}

func TestChatCompletionsStreamingToolResultContinuation(t *testing.T) {
	service := fakeCodexService{
		stream: func(ctx context.Context, req codex.Request) (<-chan codex.StreamEvent, error) {
			if len(req.Messages) != 3 {
				t.Fatalf("Messages len = %d, want 3", len(req.Messages))
			}
			if req.Messages[1].Role != "assistant" || len(req.Messages[1].ToolCalls) != 1 {
				t.Fatalf("assistant continuation message = %#v", req.Messages[1])
			}
			if req.Messages[2].Role != "tool" || req.Messages[2].ToolCallID != "call_123" {
				t.Fatalf("tool continuation message = %#v", req.Messages[2])
			}
			events := make(chan codex.StreamEvent, 3)
			events <- codex.StreamEvent{Delta: "go.mod declares "}
			events <- codex.StreamEvent{Delta: "the module."}
			events <- codex.StreamEvent{Done: true}
			close(events)
			return events, nil
		},
	}
	var logs bytes.Buffer
	app := New(config.Defaults(), WithCodexService(service), WithLogOutput(&logs), fixedServerOptions())

	resp := doJSON(t, app, `{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"read go.mod"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_123","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"go.mod\"}"}}]},{"role":"tool","tool_call_id":"call_123","content":"module github.com/teslashibe/codex-chat-api"}],"tools":[{"type":"function","function":{"name":"read_file"}}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	body := readString(t, resp.Body)
	for _, want := range []string{
		`"role":"assistant"`,
		`"content":"go.mod declares "`,
		`"content":"the module."`,
		`"finish_reason":"stop"`,
		"data: [DONE]\n\n",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream = %q, want %q", body, want)
		}
	}
	if strings.Contains(body, `"tool_calls"`) || strings.Contains(body, `"finish_reason":"tool_calls"`) {
		t.Fatalf("stream = %q, want final text without tool calls", body)
	}
	logBody := logs.String()
	if strings.Contains(logBody, "degenerate_turn") {
		t.Fatalf("logs = %q, want no degenerate_turn for valid final prose", logBody)
	}
	for _, want := range []string{
		"request_timing request_id=chatcmpl-fixed",
		"context_ms=",
		"queue_wait_ms=",
		"upstream_stream_ms=",
		"first_delta_ms=",
		"total_ms=",
	} {
		if !strings.Contains(logBody, want) {
			t.Fatalf("logs = %q, want %q", logBody, want)
		}
	}
}

func TestAgentQueueSerializesToolStreams(t *testing.T) {
	firstEvents := make(chan codex.StreamEvent)
	secondEvents := make(chan codex.StreamEvent)
	started := make(chan int, 2)
	var mu sync.Mutex
	calls := 0
	firstOpen := false
	concurrent := false

	service := fakeCodexService{
		stream: func(ctx context.Context, req codex.Request) (<-chan codex.StreamEvent, error) {
			mu.Lock()
			calls++
			call := calls
			if call == 1 {
				firstOpen = true
			}
			if call == 2 && firstOpen {
				concurrent = true
			}
			mu.Unlock()
			started <- call
			if call == 1 {
				return firstEvents, nil
			}
			return secondEvents, nil
		},
	}
	var logs bytes.Buffer
	app := New(agentQueueTestConfig(), WithCodexService(service), WithLogOutput(&logs))

	firstDone := postJSONAsync(t, app, `{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function"}]}`)
	if got := waitStarted(t, started, time.Second); got != 1 {
		t.Fatalf("first stream call = %d, want 1", got)
	}

	secondDone := postJSONAsync(t, app, `{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function"}]}`)
	select {
	case got := <-started:
		t.Fatalf("second tool stream started before first finished: %d", got)
	case <-time.After(50 * time.Millisecond):
	}

	firstEvents <- codex.StreamEvent{Done: true}
	close(firstEvents)
	mu.Lock()
	firstOpen = false
	mu.Unlock()
	resp := waitResponse(t, firstDone)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	if got := waitStarted(t, started, time.Second); got != 2 {
		t.Fatalf("second stream call = %d, want 2", got)
	}
	secondEvents <- codex.StreamEvent{Done: true}
	close(secondEvents)
	resp = waitResponse(t, secondDone)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	mu.Lock()
	wasConcurrent := concurrent
	mu.Unlock()
	if wasConcurrent {
		t.Fatal("tool streams ran concurrently with default queue settings")
	}
	body := logs.String()
	for _, want := range []string{"agent_queue_acquire request_id=", "agent_queue_wait request_id=", "agent_queue_release request_id=", "stream_start id=", "stream_end id="} {
		if !strings.Contains(body, want) {
			t.Fatalf("logs = %q, want %q", body, want)
		}
	}
}

func TestAgentQueueHonorsMaxActive(t *testing.T) {
	cfg := agentQueueTestConfig()
	cfg.AgentMaxActive = 2
	cfg.AgentQueueKeyMode = "header:x-cursor-session-id"
	started := make(chan int, 3)
	release := make(chan struct{})
	var mu sync.Mutex
	calls := 0

	service := fakeCodexService{
		complete: func(ctx context.Context, req codex.Request) (codex.Completion, error) {
			mu.Lock()
			calls++
			call := calls
			mu.Unlock()
			started <- call
			select {
			case <-release:
			case <-ctx.Done():
				return codex.Completion{}, ctx.Err()
			}
			return codex.Completion{Text: "ok", Model: req.Model}, nil
		},
	}
	app := New(cfg, WithCodexService(service), WithLogOutput(io.Discard))

	firstDone := postJSONAsync(t, app, `{"messages":[{"role":"user","content":"one"}],"tools":[{"type":"function"}]}`, map[string]string{"X-Cursor-Session-Id": "session-a"})
	secondDone := postJSONAsync(t, app, `{"messages":[{"role":"user","content":"two"}],"tools":[{"type":"function"}]}`, map[string]string{"X-Cursor-Session-Id": "session-b"})
	firstStarted := waitStarted(t, started, time.Second)
	secondStarted := waitStarted(t, started, time.Second)
	if got := map[int]bool{firstStarted: true, secondStarted: true}; !got[1] || !got[2] {
		t.Fatalf("started calls = %d,%d, want calls 1 and 2", firstStarted, secondStarted)
	}

	thirdDone := postJSONAsync(t, app, `{"messages":[{"role":"user","content":"three"}],"tools":[{"type":"function"}]}`, map[string]string{"X-Cursor-Session-Id": "session-c"})
	select {
	case got := <-started:
		t.Fatalf("third complete started before a slot released: %d", got)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	resp := waitResponse(t, firstDone)
	resp.Body.Close()
	if got := waitStarted(t, started, time.Second); got != 3 {
		t.Fatalf("third complete call = %d, want 3", got)
	}
	for _, done := range []<-chan *http.Response{secondDone, thirdDone} {
		resp := waitResponse(t, done)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
		}
	}
}

func TestAgentQueueSerializesSameKey(t *testing.T) {
	cfg := agentQueueTestConfig()
	cfg.AgentMaxActive = 2
	cfg.AgentQueueKeyMode = "body:session_id"
	firstRelease := make(chan struct{})
	secondRelease := make(chan struct{})
	started := make(chan int, 2)
	var mu sync.Mutex
	calls := 0

	service := fakeCodexService{
		complete: func(ctx context.Context, req codex.Request) (codex.Completion, error) {
			mu.Lock()
			calls++
			call := calls
			mu.Unlock()
			started <- call
			release := firstRelease
			if call == 2 {
				release = secondRelease
			}
			select {
			case <-release:
			case <-ctx.Done():
				return codex.Completion{}, ctx.Err()
			}
			return codex.Completion{Text: "ok", Model: req.Model}, nil
		},
	}
	var logs bytes.Buffer
	app := New(cfg, WithCodexService(service), WithLogOutput(&logs))

	firstDone := postJSONAsync(t, app, `{"session_id":"same-session","messages":[{"role":"user","content":"one"}],"tools":[{"type":"function"}]}`)
	if got := waitStarted(t, started, time.Second); got != 1 {
		t.Fatalf("first complete call = %d, want 1", got)
	}
	secondDone := postJSONAsync(t, app, `{"session_id":"same-session","messages":[{"role":"user","content":"two"}],"tools":[{"type":"function"}]}`)
	select {
	case got := <-started:
		t.Fatalf("second same-key request started before first released: %d", got)
	case <-time.After(50 * time.Millisecond):
	}

	close(firstRelease)
	resp := waitResponse(t, firstDone)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got := waitStarted(t, started, time.Second); got != 2 {
		t.Fatalf("second complete call = %d, want 2", got)
	}
	close(secondRelease)
	resp = waitResponse(t, secondDone)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	for _, want := range []string{"key_mode=body:session_id", "key_hash=", "active_global=", "active_key="} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("logs = %q, want %q", logs.String(), want)
		}
	}
	if strings.Contains(logs.String(), "same-session") {
		t.Fatalf("logs leaked queue key value: %q", logs.String())
	}
}

func TestServerPassesQueueKeyAffinityToCodexRequest(t *testing.T) {
	cfg := agentQueueTestConfig()
	cfg.AgentQueueKeyMode = "body:session_id"
	var mu sync.Mutex
	var requests []codex.Request

	service := fakeCodexService{
		complete: func(ctx context.Context, req codex.Request) (codex.Completion, error) {
			mu.Lock()
			requests = append(requests, req)
			mu.Unlock()
			return codex.Completion{Text: "ok", Model: req.Model}, nil
		},
	}
	app := New(cfg, WithCodexService(service), fixedServerOptions())

	for range 2 {
		resp := doJSON(t, app, `{"session_id":"same-session","messages":[{"role":"user","content":"one"}],"tools":[{"type":"function"}]}`)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(requests))
	}
	for i, req := range requests {
		if req.RequestID != "chatcmpl-fixed" {
			t.Fatalf("request %d RequestID = %q", i, req.RequestID)
		}
		if req.AffinityKeyMode != "body:session_id" {
			t.Fatalf("request %d AffinityKeyMode = %q", i, req.AffinityKeyMode)
		}
		if req.AffinityKey == "" || req.AffinityKeyHash == "" {
			t.Fatalf("request %d affinity = key:%q hash:%q", i, req.AffinityKey, req.AffinityKeyHash)
		}
	}
	if requests[0].AffinityKey != requests[1].AffinityKey || requests[0].AffinityKeyHash != requests[1].AffinityKeyHash {
		t.Fatalf("same queue key routed with different affinity: %#v %#v", requests[0], requests[1])
	}
	if strings.Contains(requests[0].AffinityKeyHash, "same-session") {
		t.Fatalf("affinity hash leaked raw session: %q", requests[0].AffinityKeyHash)
	}
}

func TestAgentQueueSharedLockSerializesSameKeyAcrossQueues(t *testing.T) {
	lockDir := t.TempDir()
	logf := func(string, ...any) {}
	q1 := newAgentQueue(true, 1, 1, 10, time.Second, lockDir, false, time.Now, logf)
	q2 := newAgentQueue(true, 1, 1, 10, time.Second, lockDir, false, time.Now, logf)
	key := newAgentQueueKey("body:session_id", "same-session")

	releaseFirst, _, err := q1.acquire(context.Background(), "request-1", key, turnClassToolGenerating)
	if err != nil {
		t.Fatalf("first acquire error = %v", err)
	}

	acquiredSecond := make(chan func(), 1)
	errs := make(chan error, 1)
	go func() {
		releaseSecond, _, err := q2.acquire(context.Background(), "request-2", key, turnClassToolGenerating)
		if err != nil {
			errs <- err
			return
		}
		acquiredSecond <- releaseSecond
	}()

	select {
	case releaseSecond := <-acquiredSecond:
		releaseSecond()
		t.Fatal("second queue acquired same key before first shared lock released")
	case err := <-errs:
		t.Fatalf("second acquire error = %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	releaseFirst()
	select {
	case releaseSecond := <-acquiredSecond:
		releaseSecond()
	case err := <-errs:
		t.Fatalf("second acquire error after release = %v", err)
	case <-time.After(time.Second):
		t.Fatal("second queue did not acquire same key after first shared lock released")
	}
}

func TestAgentQueueDisabledStillUsesSharedLockForSameKey(t *testing.T) {
	lockDir := t.TempDir()
	logf := func(string, ...any) {}
	q1 := newAgentQueue(false, 1, 1, 10, time.Second, lockDir, false, time.Now, logf)
	q2 := newAgentQueue(false, 1, 1, 10, time.Second, lockDir, false, time.Now, logf)
	key := newAgentQueueKey("body:session_id", "same-session")

	releaseFirst, _, err := q1.acquire(context.Background(), "request-1", key, turnClassToolGenerating)
	if err != nil {
		t.Fatalf("first acquire error = %v", err)
	}

	acquiredSecond := make(chan func(), 1)
	errs := make(chan error, 1)
	go func() {
		releaseSecond, _, err := q2.acquire(context.Background(), "request-2", key, turnClassToolGenerating)
		if err != nil {
			errs <- err
			return
		}
		acquiredSecond <- releaseSecond
	}()

	select {
	case releaseSecond := <-acquiredSecond:
		releaseSecond()
		t.Fatal("second disabled queue acquired same key before first shared lock released")
	case err := <-errs:
		t.Fatalf("second acquire error = %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	releaseFirst()
	select {
	case releaseSecond := <-acquiredSecond:
		releaseSecond()
	case err := <-errs:
		t.Fatalf("second acquire error after release = %v", err)
	case <-time.After(time.Second):
		t.Fatal("second disabled queue did not acquire same key after first shared lock released")
	}
}

func TestAgentQueueBypassesRequestsWithoutTools(t *testing.T) {
	toolEvents := make(chan codex.StreamEvent)
	toolStarted := make(chan struct{}, 1)
	startedNoTools := make(chan struct{}, 1)
	service := fakeCodexService{
		stream: func(ctx context.Context, req codex.Request) (<-chan codex.StreamEvent, error) {
			if rawJSONPresent(req.Tools) {
				toolStarted <- struct{}{}
				return toolEvents, nil
			}
			startedNoTools <- struct{}{}
			events := make(chan codex.StreamEvent, 1)
			events <- codex.StreamEvent{Done: true}
			close(events)
			return events, nil
		},
	}
	app := New(agentQueueTestConfig(), WithCodexService(service), WithLogOutput(io.Discard))

	toolDone := postJSONAsync(t, app, `{"stream":true,"messages":[{"role":"user","content":"tool"}],"tools":[{"type":"function"}]}`)
	select {
	case <-toolStarted:
	case <-time.After(time.Second):
		t.Fatal("tool request did not start")
	}
	resp := doJSON(t, app, `{"stream":true,"messages":[{"role":"user","content":"ask"}]}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("no-tools status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	select {
	case <-startedNoTools:
	case <-time.After(time.Second):
		t.Fatal("request without tools did not bypass active Agent queue slot")
	}
	toolEvents <- codex.StreamEvent{Done: true}
	close(toolEvents)
	resp = waitResponse(t, toolDone)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tool status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestAgentQueuePriorityOrdersDifferentKeysViaHandler(t *testing.T) {
	cfg := agentQueueTestConfig()
	cfg.AgentMaxActive = 1
	cfg.AgentQueueKeyMode = "header:x-cursor-session-id"
	cfg.AgentQueuePriorityEnabled = true

	releases := map[string]chan struct{}{
		"first": make(chan struct{}),
		"low":   make(chan struct{}),
		"high":  make(chan struct{}),
	}
	started := make(chan string, 3)
	service := fakeCodexService{
		complete: func(ctx context.Context, req codex.Request) (codex.Completion, error) {
			label := strings.TrimSpace(openai.MessageText(req.Messages[0].Content))
			started <- label
			select {
			case <-releases[label]:
			case <-ctx.Done():
				return codex.Completion{}, ctx.Err()
			}
			return codex.Completion{Text: "ok", Model: req.Model}, nil
		},
	}
	var logs bytes.Buffer
	app := New(cfg, WithCodexService(service), WithLogOutput(&logs))

	firstDone := postJSONAsync(t, app, `{"messages":[{"role":"user","content":"first"}],"tools":[{"type":"function"}]}`, map[string]string{"X-Cursor-Session-Id": "session-a"})
	if got := waitStartedLabel(t, started, time.Second); got != "first" {
		t.Fatalf("first started = %q, want first", got)
	}

	lowDone := postJSONAsync(t, app, `{"messages":[{"role":"user","content":"low"}],"tools":[{"type":"function"}]}`, map[string]string{"X-Cursor-Session-Id": "session-b"})
	waitFor(t, time.Second, func() bool {
		return strings.Count(logs.String(), "agent_queue_wait request_id=") == 1
	}, "low-priority waiter to queue")
	highDone := postJSONAsync(t, app, `{"messages":[{"role":"user","content":"high"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_high","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_high","content":"result"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`, map[string]string{"X-Cursor-Session-Id": "session-c"})
	waitFor(t, time.Second, func() bool {
		return strings.Count(logs.String(), "agent_queue_wait request_id=") == 2
	}, "high-priority waiter to queue")

	close(releases["first"])
	resp := waitResponse(t, firstDone)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got := waitStartedLabel(t, started, time.Second); got != "high" {
		t.Fatalf("next started = %q, want high-priority continuation", got)
	}
	select {
	case got := <-started:
		t.Fatalf("low-priority request started while high was active: %q", got)
	case <-time.After(30 * time.Millisecond):
	}

	close(releases["high"])
	resp = waitResponse(t, highDone)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("high status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got := waitStartedLabel(t, started, time.Second); got != "low" {
		t.Fatalf("last started = %q, want low", got)
	}
	close(releases["low"])
	resp = waitResponse(t, lowDone)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("low status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	for _, want := range []string{"turn_class=tool_result_continuation", "priority=10", "turn_class=tool_generating", "priority=0"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("logs = %q, want %q", logs.String(), want)
		}
	}
}

func TestAgentQueueFullReturns429(t *testing.T) {
	cfg := agentQueueTestConfig()
	cfg.AgentQueueLimit = 0
	events := make(chan codex.StreamEvent)
	started := make(chan struct{}, 1)
	service := fakeCodexService{
		stream: func(ctx context.Context, req codex.Request) (<-chan codex.StreamEvent, error) {
			started <- struct{}{}
			return events, nil
		},
	}
	var logs bytes.Buffer
	app := New(cfg, WithCodexService(service), WithLogOutput(&logs))

	firstDone := postJSONAsync(t, app, `{"stream":true,"messages":[{"role":"user","content":"shared prompt"}],"tools":[{"type":"function"}]}`)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first request did not start")
	}
	resp := doJSON(t, app, `{"stream":true,"messages":[{"role":"user","content":"shared prompt"},{"role":"user","content":"follow up"}],"tools":[{"type":"function"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusTooManyRequests)
	}
	body := readString(t, resp.Body)
	if !strings.Contains(body, `"type":"rate_limit_error"`) || !strings.Contains(body, "agent queue full") {
		t.Fatalf("body = %q, want OpenAI-shaped queue full error", body)
	}
	for _, want := range []string{"agent_queue_full request_id=", "key_mode=cursor:conversation_fingerprint", "key_hash=", "limit=0"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("logs = %q, want %q", logs.String(), want)
		}
	}

	events <- codex.StreamEvent{Done: true}
	close(events)
	resp = waitResponse(t, firstDone)
	resp.Body.Close()
}

func TestAgentQueueTimeoutReturns429(t *testing.T) {
	cfg := agentQueueTestConfig()
	cfg.AgentQueueTimeout = 20 * time.Millisecond
	events := make(chan codex.StreamEvent)
	started := make(chan struct{}, 1)
	service := fakeCodexService{
		stream: func(ctx context.Context, req codex.Request) (<-chan codex.StreamEvent, error) {
			started <- struct{}{}
			return events, nil
		},
	}
	var logs bytes.Buffer
	app := New(cfg, WithCodexService(service), WithLogOutput(&logs))

	firstDone := postJSONAsync(t, app, `{"stream":true,"messages":[{"role":"user","content":"shared prompt"}],"tools":[{"type":"function"}]}`)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first request did not start")
	}
	resp := doJSON(t, app, `{"stream":true,"messages":[{"role":"user","content":"shared prompt"},{"role":"user","content":"follow up"}],"tools":[{"type":"function"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusTooManyRequests)
	}
	body := readString(t, resp.Body)
	if !strings.Contains(body, `"type":"rate_limit_error"`) || !strings.Contains(body, "agent queue timeout") {
		t.Fatalf("body = %q, want OpenAI-shaped queue timeout error", body)
	}
	for _, want := range []string{"agent_queue_timeout request_id=", "key_mode=cursor:conversation_fingerprint", "key_hash=", "wait_ms="} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("logs = %q, want %q", logs.String(), want)
		}
	}

	events <- codex.StreamEvent{Done: true}
	close(events)
	resp = waitResponse(t, firstDone)
	resp.Body.Close()
}

func TestAgentQueueReleasesAfterStreamingUpstreamError(t *testing.T) {
	firstEvents := make(chan codex.StreamEvent, 1)
	secondEvents := make(chan codex.StreamEvent)
	started := make(chan int, 2)
	var mu sync.Mutex
	calls := 0
	service := fakeCodexService{
		stream: func(ctx context.Context, req codex.Request) (<-chan codex.StreamEvent, error) {
			mu.Lock()
			calls++
			call := calls
			mu.Unlock()
			started <- call
			if call == 1 {
				return firstEvents, nil
			}
			return secondEvents, nil
		},
	}
	app := New(agentQueueTestConfig(), WithCodexService(service), WithLogOutput(io.Discard))

	firstDone := postJSONAsync(t, app, `{"stream":true,"messages":[{"role":"user","content":"one"}],"tools":[{"type":"function"}]}`)
	if got := waitStarted(t, started, time.Second); got != 1 {
		t.Fatalf("first stream call = %d, want 1", got)
	}
	secondDone := postJSONAsync(t, app, `{"stream":true,"messages":[{"role":"user","content":"two"}],"tools":[{"type":"function"}]}`)
	firstEvents <- codex.StreamEvent{Err: codex.NewError(codex.ErrorKindUpstream, http.StatusBadGateway, "raw upstream", errors.New("boom"))}
	close(firstEvents)
	resp := waitResponse(t, firstDone)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got := waitStarted(t, started, time.Second); got != 2 {
		t.Fatalf("second stream call = %d, want 2", got)
	}
	secondEvents <- codex.StreamEvent{Done: true}
	close(secondEvents)
	resp = waitResponse(t, secondDone)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestRequestLogsAreRedacted(t *testing.T) {
	const secret = "secret-access-token"
	const prompt = "do not log this prompt"
	service := fakeCodexService{
		complete: func(ctx context.Context, req codex.Request) (codex.Completion, error) {
			return codex.Completion{Text: "ok", Model: req.Model}, nil
		},
		stream: func(ctx context.Context, req codex.Request) (<-chan codex.StreamEvent, error) {
			events := make(chan codex.StreamEvent, 1)
			events <- codex.StreamEvent{Done: true}
			close(events)
			return events, nil
		},
	}
	cfg := config.Defaults()
	cfg.LogBodyShape = true
	var logs bytes.Buffer
	app := New(cfg, WithCodexService(service), WithLogOutput(&logs), fixedServerOptions())

	req, err := http.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		bytes.NewBufferString(`{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"`+prompt+`"}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}]}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := app.Test(req, 2000)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	body := logs.String()
	for _, leaked := range []string{secret, prompt, "Bearer"} {
		if strings.Contains(body, leaked) {
			t.Fatalf("logs leaked %q: %q", leaked, body)
		}
	}
	for _, want := range []string{
		"body_shape path=/v1/chat/completions valid_json=true",
		"fields=messages,model,stream,tools",
		"messages=1",
		"message_roles=user",
		"tools_present=true",
		"tools_count=1",
		"tool_wire=flat",
		"tool_choice=absent",
		"chat_completion model=gpt-test stream=true tools_present=true turn_class=tool_generating",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("logs = %q, want %q", body, want)
		}
	}
}

func TestRequestIdentityLogsAreRedacted(t *testing.T) {
	const secret = "secret-access-token"
	const cookie = "session-cookie-secret"
	const prompt = "do not log this prompt"
	const toolArgs = "do-not-log-tool-args"
	service := fakeCodexService{
		complete: func(ctx context.Context, req codex.Request) (codex.Completion, error) {
			return codex.Completion{Text: "ok", Model: req.Model}, nil
		},
	}
	cfg := config.Defaults()
	cfg.LogRequestIdentity = true
	var logs bytes.Buffer
	app := New(cfg, WithCodexService(service), WithLogOutput(&logs), fixedServerOptions())

	req, err := http.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		bytes.NewBufferString(`{"model":"gpt-test","stream":false,"user":"user@example.test","session_id":"body-session","metadata":{"workspace_id":"workspace-123","nested":{"secret":"`+secret+`"}},"messages":[{"role":"user","content":"`+prompt+`"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_123","type":"function","function":{"name":"lookup","arguments":"`+toolArgs+`"}}]}],"tools":[{"type":"function","function":{"name":"lookup","description":"secret schema","parameters":{"type":"object"}}}]}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Cookie", cookie)
	req.Header.Set("User-Agent", "Cursor/Test")
	req.Header.Set("X-Cursor-Session-Id", "cursor-value-secret")
	req.Header.Set("X-Request-Id", "request-value-secret")
	resp, err := app.Test(req, 2000)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	body := logs.String()
	for _, leaked := range []string{secret, cookie, prompt, toolArgs, "Bearer", "user@example.test", "body-session", "workspace-123", "cursor-value-secret", "request-value-secret", "secret schema"} {
		if strings.Contains(body, leaked) {
			t.Fatalf("logs leaked %q: %q", leaked, body)
		}
	}
	for _, want := range []string{
		"request_identity request_id=chatcmpl-fixed method=POST path=/v1/chat/completions",
		"user_agent_hash=",
		"header_names=",
		"authorization",
		"cookie",
		"cursor_headers=",
		"x-cursor-session-id=",
		"x-request-id=",
		"body_fields=messages,metadata,model,session_id,stream,tools,user",
		"body_scalars=model=",
		"session_id=",
		"user=",
		"metadata_fields=nested,workspace_id",
		"metadata_scalars=workspace_id=",
		"message_count=2",
		"message_roles=user,assistant",
		"tool_count=1",
		"stream=false",
		"tools_present=true",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("logs = %q, want %q", body, want)
		}
	}
}

func TestRequestLogs404s(t *testing.T) {
	var logs bytes.Buffer
	app := New(config.Defaults(), WithLogOutput(&logs))

	req, err := http.NewRequest(http.MethodGet, "/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}

	body := logs.String()
	for _, want := range []string{"status=404", "method=GET", "path=/v1/responses", "authorization_present=false", "not_found=true"} {
		if !strings.Contains(body, want) {
			t.Fatalf("logs = %q, want %q", body, want)
		}
	}
}

func TestChatCompletionsUpstreamErrorIsSanitized(t *testing.T) {
	const secret = "secret-access-token"
	service := fakeCodexService{
		complete: func(ctx context.Context, req codex.Request) (codex.Completion, error) {
			return codex.Completion{}, codex.NewError(
				codex.ErrorKindUpstream,
				http.StatusBadGateway,
				"upstream unavailable "+secret,
				errors.New("backend included "+secret),
			)
		},
	}
	app := New(config.Defaults(), WithCodexService(service), fixedServerOptions())

	resp := doJSON(t, app, `{"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadGateway)
	}
	body := readString(t, resp.Body)
	if !strings.Contains(body, "upstream error") {
		t.Fatalf("body = %q, want sanitized upstream message", body)
	}
	if strings.Contains(body, secret) {
		t.Fatalf("response leaked secret: %q", body)
	}
}

func TestChatCompletionsAuthErrorIsSanitized(t *testing.T) {
	const secret = "secret-access-token"
	service := fakeCodexService{
		complete: func(ctx context.Context, req codex.Request) (codex.Completion, error) {
			return codex.Completion{}, codex.NewError(
				codex.ErrorKindAuth,
				http.StatusUnauthorized,
				"bad token "+secret,
				errors.New("authorization header had "+secret),
			)
		},
	}
	app := New(config.Defaults(), WithCodexService(service), fixedServerOptions())

	resp := doJSON(t, app, `{"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
	body := readString(t, resp.Body)
	if !strings.Contains(body, "authentication failed") {
		t.Fatalf("body = %q, want sanitized auth message", body)
	}
	if strings.Contains(body, secret) {
		t.Fatalf("response leaked secret: %q", body)
	}
}

func TestChatCompletionsStreamingErrorIsSanitized(t *testing.T) {
	const secret = "secret-access-token"
	service := fakeCodexService{
		stream: func(ctx context.Context, req codex.Request) (<-chan codex.StreamEvent, error) {
			events := make(chan codex.StreamEvent, 1)
			events <- codex.StreamEvent{
				Err: codex.NewError(
					codex.ErrorKindUpstream,
					http.StatusBadGateway,
					"raw upstream "+secret,
					errors.New("payload contained "+secret),
				),
			}
			close(events)
			return events, nil
		},
	}
	app := New(config.Defaults(), WithCodexService(service), fixedServerOptions())

	resp := doJSON(t, app, `{"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body := readString(t, resp.Body)
	if !strings.Contains(body, "[error: upstream error]") {
		t.Fatalf("body = %q, want sanitized streaming error", body)
	}
	if strings.Contains(body, secret) {
		t.Fatalf("response leaked secret: %q", body)
	}
	if !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("stream = %q, want terminal DONE event", body)
	}
}

func TestChatCompletionsBadRequestIsSanitized(t *testing.T) {
	const secret = "secret-access-token"
	app := New(config.Defaults(), WithCodexService(fakeCodexService{}), fixedServerOptions())

	resp := doJSON(t, app, `{"messages":`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	body := readString(t, resp.Body)
	if strings.Contains(body, secret) {
		t.Fatalf("response leaked secret: %q", body)
	}

	resp = doJSON(t, app, `{"messages":[]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty messages status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestChatCompletionsCancellation(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	var sawCanceledContext bool
	service := fakeCodexService{
		complete: func(ctx context.Context, req codex.Request) (codex.Completion, error) {
			select {
			case <-ctx.Done():
				sawCanceledContext = true
				return codex.Completion{}, ctx.Err()
			case <-time.After(time.Second):
				t.Fatal("service did not receive canceled context")
				return codex.Completion{}, nil
			}
		},
	}
	app := New(
		config.Defaults(),
		WithCodexService(service),
		fixedServerOptions(),
		func(opts *options) {
			opts.requestContext = func(*fiber.Ctx) context.Context {
				return canceled
			}
		},
	)

	resp := doJSON(t, app, `{"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != 499 {
		t.Fatalf("status = %d, want 499", resp.StatusCode)
	}
	if !sawCanceledContext {
		t.Fatal("service did not observe canceled context")
	}
}

type fakeCodexService struct {
	complete func(context.Context, codex.Request) (codex.Completion, error)
	stream   func(context.Context, codex.Request) (<-chan codex.StreamEvent, error)
}

func (f fakeCodexService) Complete(ctx context.Context, req codex.Request) (codex.Completion, error) {
	if f.complete == nil {
		return codex.Completion{}, errors.New("unexpected Complete call")
	}
	return f.complete(ctx, req)
}

func (f fakeCodexService) Stream(ctx context.Context, req codex.Request) (<-chan codex.StreamEvent, error) {
	if f.stream == nil {
		return nil, errors.New("unexpected Stream call")
	}
	return f.stream(ctx, req)
}

func fixedServerOptions() Option {
	return func(opts *options) {
		opts.now = func() time.Time {
			return time.Unix(123, 0)
		}
		opts.newID = func() string {
			return "chatcmpl-fixed"
		}
	}
}

func doJSON(t *testing.T, app interface {
	Test(*http.Request, ...int) (*http.Response, error)
}, body string, headers ...map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret-access-token")
	for _, headerSet := range headers {
		for key, value := range headerSet {
			req.Header.Set(key, value)
		}
	}
	resp, err := app.Test(req, 2000)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	return resp
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func postJSONAsync(t *testing.T, app interface {
	Test(*http.Request, ...int) (*http.Response, error)
}, body string, headers ...map[string]string) <-chan *http.Response {
	t.Helper()
	done := make(chan *http.Response, 1)
	go func() {
		req, err := http.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
		if err != nil {
			t.Errorf("NewRequest() error = %v", err)
			done <- &http.Response{StatusCode: 0, Body: io.NopCloser(strings.NewReader(""))}
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer secret-access-token")
		for _, headerSet := range headers {
			for key, value := range headerSet {
				req.Header.Set(key, value)
			}
		}
		resp, err := app.Test(req, 2000)
		if err != nil {
			t.Errorf("app.Test() error = %v", err)
			done <- &http.Response{StatusCode: 0, Body: io.NopCloser(strings.NewReader(""))}
			return
		}
		done <- resp
	}()
	return done
}

func waitStarted(t *testing.T, started <-chan int, timeout time.Duration) int {
	t.Helper()
	select {
	case got := <-started:
		return got
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for stream call")
		return 0
	}
}

func waitStartedLabel(t *testing.T, started <-chan string, timeout time.Duration) string {
	t.Helper()
	select {
	case got := <-started:
		return got
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for service call")
		return ""
	}
}

func waitResponse(t *testing.T, done <-chan *http.Response) *http.Response {
	t.Helper()
	select {
	case resp := <-done:
		return resp
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for response")
		return nil
	}
}

func readString(t *testing.T, r io.Reader) string {
	t.Helper()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(data)
}
