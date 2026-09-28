package server

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teslashibe/open-agent-api/internal/codex"
	"github.com/teslashibe/open-agent-api/internal/config"
	"github.com/teslashibe/open-agent-api/internal/openai"
)

const anthropicDialectBody = `{
  "model": "api/claude-sonnet-5",
  "stream": true,
  "max_tokens": 32000,
  "stream_options": {"include_usage": true},
  "system": [{"type": "text", "text": "You are Cursor's agent.", "cache_control": {"type": "ephemeral"}}],
  "tools": [{"name": "Read", "description": "Read a file", "input_schema": {"type": "object", "properties": {"path": {"type": "string"}}}}],
  "tool_choice": {"type": "auto"},
  "messages": [
    {"role": "user", "content": "read the readme"},
    {"role": "assistant", "content": [{"type": "tool_use", "id": "toolu_1", "name": "Read", "input": {"path": "README.md"}}]},
    {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_1", "content": "# Zephyr"}]}
  ]
}`

func TestAnthropicDialectReachesProviderAsChatCompletions(t *testing.T) {
	var mu sync.Mutex
	var got codex.Request
	service := fakeCodexService{stream: func(_ context.Context, req codex.Request) (<-chan codex.StreamEvent, error) {
		mu.Lock()
		got = req
		mu.Unlock()
		return streamEvents(
			codex.StreamEvent{Delta: "It is Zephyr."},
			codex.StreamEvent{Done: true, Usage: openai.Usage{PromptTokens: 120, CompletionTokens: 5, TotalTokens: 125}},
		), nil
	}}
	app := New(config.Defaults(), WithCodexService(service), fixedServerOptions())
	resp := doJSON(t, app, anthropicDialectBody)
	defer resp.Body.Close()
	body := readString(t, resp.Body)

	mu.Lock()
	defer mu.Unlock()
	roles := []string{}
	for _, m := range got.Messages {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, ",") != "system,user,assistant,tool" {
		t.Fatalf("roles = %v", roles)
	}
	if openai.MessageText(got.Messages[0].Content) != "You are Cursor's agent." {
		t.Fatalf("system prompt = %s", got.Messages[0].Content)
	}
	if got.Messages[2].ToolCalls[0].ID != "toolu_1" || got.Messages[3].ToolCallID != "toolu_1" {
		t.Fatalf("tool pair = %+v / %+v", got.Messages[2], got.Messages[3])
	}
	if !strings.Contains(string(got.Tools), `"type":"function"`) || string(got.ToolChoice) != `"auto"` || !got.IncludeUsage {
		t.Fatalf("tools=%s tool_choice=%s include_usage=%v", got.Tools, got.ToolChoice, got.IncludeUsage)
	}
	usageAt := strings.Index(body, `"choices":[],"usage":{"prompt_tokens":120,"completion_tokens":5,"total_tokens":125}`)
	finishAt := strings.Index(body, `"finish_reason":"stop"`)
	doneAt := strings.Index(body, "data: [DONE]")
	if usageAt < 0 || finishAt < 0 || !(finishAt < usageAt && usageAt < doneAt) {
		t.Fatalf("want finish, then usage chunk, then [DONE]; body = %s", body)
	}
}

func TestClaudeAgentTurnStreamsTextAsContent(t *testing.T) {
	service := fakeCodexService{stream: func(context.Context, codex.Request) (<-chan codex.StreamEvent, error) {
		return streamEvents(
			codex.StreamEvent{Delta: "Reading the readme."},
			codex.StreamEvent{ToolCallDelta: &codex.ToolCallDelta{Index: 0, ID: "toolu_9", Type: "function", Function: codex.ToolCallFunctionDelta{Name: "Read", Arguments: `{"path":"README.md"}`}, Final: true}},
			codex.StreamEvent{Done: true},
		), nil
	}}
	app := New(config.Defaults(), WithCodexService(service), fixedServerOptions())
	resp := doJSON(t, app, `{"model":"api/claude-sonnet-5","stream":true,"tools":[{"type":"function","function":{"name":"Read","parameters":{"type":"object"}}}],"messages":[{"role":"user","content":"read it"}]}`)
	defer resp.Body.Close()
	body := readString(t, resp.Body)
	if !strings.Contains(body, `"content":"Reading the readme."`) || strings.Contains(body, "reasoning_content") {
		t.Fatalf("claude narration must stream as content: %s", body)
	}
	if !strings.Contains(body, `"finish_reason":"tool_calls"`) {
		t.Fatalf("missing tool_calls finish: %s", body)
	}
}

func TestStreamErrorGateReturnsHTTPStatus(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		want   string
	}{
		{"rate limit", codex.NewError(codex.ErrorKindUpstream, http.StatusTooManyRequests, "claude code usage limit reached", nil), http.StatusTooManyRequests, "rate_limit_error"},
		{"overloaded", codex.NewError(codex.ErrorKindUpstream, 529, "overloaded", nil), 529, "api_error"},
		{"context", codex.NewError(codex.ErrorKindClient, http.StatusBadRequest, "too long", codex.ErrContextWindowExceeded), http.StatusBadRequest, "context_length_exceeded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service := fakeCodexService{stream: func(context.Context, codex.Request) (<-chan codex.StreamEvent, error) {
				// Metadata first (as the Claude CLI's init event), then the error.
				return streamEvents(codex.StreamEvent{Model: "claude-sonnet-5", ID: "s1"}, codex.StreamEvent{Err: tc.err}), nil
			}}
			cfg := config.Defaults()
			cfg.StreamErrorGate = 2 * time.Second
			app := New(cfg, WithCodexService(service), fixedServerOptions())
			resp := doJSON(t, app, `{"model":"api/claude-sonnet-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
			defer resp.Body.Close()
			body := readString(t, resp.Body)
			if resp.StatusCode != tc.status || !strings.Contains(body, tc.want) {
				t.Fatalf("status=%d body=%s", resp.StatusCode, body)
			}
			if tc.name == "context" && !strings.Contains(body, "prompt is too long") {
				t.Fatalf("context overflow must carry Cursor's summarize trigger phrase: %s", body)
			}
		})
	}
}

func TestStreamErrorGatePassesHealthyStreams(t *testing.T) {
	service := fakeCodexService{stream: func(context.Context, codex.Request) (<-chan codex.StreamEvent, error) {
		return streamEvents(codex.StreamEvent{Model: "m"}, codex.StreamEvent{Delta: "hi"}, codex.StreamEvent{Done: true}), nil
	}}
	cfg := config.Defaults()
	cfg.StreamErrorGate = 2 * time.Second
	app := New(cfg, WithCodexService(service), fixedServerOptions())
	resp := doJSON(t, app, `{"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	body := readString(t, resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"content":"hi"`) || !strings.Contains(body, "[DONE]") {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
}

func TestStreamKeepaliveDuringSilence(t *testing.T) {
	service := fakeCodexService{stream: func(ctx context.Context, _ codex.Request) (<-chan codex.StreamEvent, error) {
		out := make(chan codex.StreamEvent)
		go func() {
			defer close(out)
			select {
			case <-time.After(150 * time.Millisecond):
			case <-ctx.Done():
				return
			}
			out <- codex.StreamEvent{Delta: "done thinking"}
			out <- codex.StreamEvent{Done: true}
		}()
		return out, nil
	}}
	cfg := config.Defaults()
	cfg.StreamKeepaliveInterval = 20 * time.Millisecond
	app := New(cfg, WithCodexService(service), fixedServerOptions())
	resp := doJSON(t, app, `{"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	body := readString(t, resp.Body)
	if !strings.Contains(body, ": keepalive\n\n") || !strings.Contains(body, `"content":"done thinking"`) {
		t.Fatalf("body = %q", body)
	}
}

func TestStreamKeepaliveChunkMode(t *testing.T) {
	service := fakeCodexService{stream: func(ctx context.Context, _ codex.Request) (<-chan codex.StreamEvent, error) {
		out := make(chan codex.StreamEvent)
		go func() {
			defer close(out)
			select {
			case <-time.After(150 * time.Millisecond):
			case <-ctx.Done():
				return
			}
			out <- codex.StreamEvent{Delta: "ok"}
			out <- codex.StreamEvent{Done: true}
		}()
		return out, nil
	}}
	cfg := config.Defaults()
	cfg.StreamKeepaliveInterval = 20 * time.Millisecond
	cfg.StreamKeepaliveMode = "chunk"
	app := New(cfg, WithCodexService(service), fixedServerOptions())
	resp := doJSON(t, app, `{"model":"api/claude-sonnet-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	body := readString(t, resp.Body)
	if strings.Contains(body, ": keepalive") || !strings.Contains(body, `"choices":[{"index":0,"delta":{},"finish_reason":null}]`) || !strings.Contains(body, `"content":"ok"`) {
		t.Fatalf("body = %q", body)
	}
}
