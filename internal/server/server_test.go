package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/teslashibe/codex-chat-api/internal/codex"
	"github.com/teslashibe/codex-chat-api/internal/config"
	"github.com/teslashibe/codex-chat-api/internal/openai"
)

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
	wantIDs := []string{openai.DefaultModel, "gpt-5.5-low", "gpt-5.5-high", "gpt-5.5-fast"}
	if body.Object != "list" || len(body.Data) != len(wantIDs) {
		t.Fatalf("unexpected model list: %#v", body)
	}
	for i, model := range body.Data {
		if model.ID != wantIDs[i] || model.Object != "model" || model.Created != 0 || model.OwnedBy != "codex-chat-api" {
			t.Fatalf("unexpected model at %d: %#v", i, model)
		}
	}
}

func TestChatCompletionsResolvesModelAliases(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantModel     string
		wantEffort    string
		wantVerbosity string
	}{
		{
			name:          "high",
			body:          `{"model":"gpt-5.5-high","messages":[{"role":"user","content":"hi"}]}`,
			wantModel:     openai.DefaultModel,
			wantEffort:    "high",
			wantVerbosity: "medium",
		},
		{
			name:          "low",
			body:          `{"model":"gpt-5.5-low","messages":[{"role":"user","content":"hi"}]}`,
			wantModel:     openai.DefaultModel,
			wantEffort:    "low",
			wantVerbosity: "medium",
		},
		{
			name:          "fast",
			body:          `{"model":"gpt-5.5-fast","messages":[{"role":"user","content":"hi"}]}`,
			wantModel:     openai.DefaultModel,
			wantEffort:    "low",
			wantVerbosity: "low",
		},
		{
			name:          "unknown passthrough",
			body:          `{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}`,
			wantModel:     "gpt-test",
			wantEffort:    "medium",
			wantVerbosity: "medium",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := fakeCodexService{
				complete: func(ctx context.Context, req codex.Request) (codex.Completion, error) {
					if req.Model != tt.wantModel {
						t.Fatalf("Model = %q, want %q", req.Model, tt.wantModel)
					}
					if req.ReasoningEffort != tt.wantEffort {
						t.Fatalf("ReasoningEffort = %q, want %q", req.ReasoningEffort, tt.wantEffort)
					}
					if req.Verbosity != tt.wantVerbosity {
						t.Fatalf("Verbosity = %q, want %q", req.Verbosity, tt.wantVerbosity)
					}
					return codex.Completion{Text: "ok", Model: req.Model}, nil
				},
			}
			app := New(config.Defaults(), WithCodexService(service), fixedServerOptions())

			resp := doJSON(t, app, tt.body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
			}
		})
	}
}

func TestChatCompletionsExplicitReasoningAndVerbosityOverrideAlias(t *testing.T) {
	service := fakeCodexService{
		complete: func(ctx context.Context, req codex.Request) (codex.Completion, error) {
			if req.Model != openai.DefaultModel {
				t.Fatalf("Model = %q, want %q", req.Model, openai.DefaultModel)
			}
			if req.ReasoningEffort != "high" {
				t.Fatalf("ReasoningEffort = %q, want high", req.ReasoningEffort)
			}
			if req.Verbosity != "high" {
				t.Fatalf("Verbosity = %q, want high", req.Verbosity)
			}
			return codex.Completion{Text: "ok", Model: req.Model}, nil
		},
	}
	app := New(config.Defaults(), WithCodexService(service), fixedServerOptions())

	resp := doJSON(t, app, `{"model":"gpt-5.5-fast","reasoning_effort":"high","verbosity":"high","messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
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
	app := New(config.Defaults(), WithCodexService(service), fixedServerOptions())

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
}

func TestChatCompletionsNonStreamingToolCalls(t *testing.T) {
	service := fakeCodexService{
		complete: func(ctx context.Context, req codex.Request) (codex.Completion, error) {
			if req.Model != openai.DefaultModel {
				t.Fatalf("Model = %q, want %q", req.Model, openai.DefaultModel)
			}
			if req.ReasoningEffort != "high" || req.Verbosity != "medium" {
				t.Fatalf("alias defaults = effort %q verbosity %q, want high/medium", req.ReasoningEffort, req.Verbosity)
			}
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
	app := New(config.Defaults(), WithCodexService(service), fixedServerOptions())

	resp := doJSON(t, app, `{"model":"gpt-5.5-high","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup","description":"Look up things","parameters":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}}}],"tool_choice":{"type":"function","function":{"name":"lookup"}},"parallel_tool_calls":true}`)
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

func TestChatCompletionsStreamingResolvesModelAlias(t *testing.T) {
	service := fakeCodexService{
		stream: func(ctx context.Context, req codex.Request) (<-chan codex.StreamEvent, error) {
			if req.Model != openai.DefaultModel {
				t.Fatalf("Model = %q, want %q", req.Model, openai.DefaultModel)
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
	app := New(config.Defaults(), WithCodexService(service), fixedServerOptions())

	resp := doJSON(t, app, `{"model":"gpt-5.5-fast","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body := readString(t, resp.Body)
	if !strings.Contains(body, `"content":"ok"`) || !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("stream = %q, want normal SSE response", body)
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
	app := New(config.Defaults(), WithCodexService(service), fixedServerOptions())

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
		bytes.NewBufferString(`{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"`+prompt+`"}],"tools":[{"type":"function"}]}`),
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
		"chat_completion model=gpt-test stream=true tools_present=true",
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
}, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret-access-token")
	resp, err := app.Test(req, 2000)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	return resp
}

func readString(t *testing.T, r io.Reader) string {
	t.Helper()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(data)
}
