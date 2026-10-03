package server

import (
	"context"
	"io"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/teslashibe/open-agent-api/internal/codex"
	"github.com/teslashibe/open-agent-api/internal/config"
	"github.com/teslashibe/open-agent-api/internal/openai"
)

func TestInvalidModelControlsRejectedBeforeProvider(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct{ model, effort, speed string }{
			{"gpt-5.5", "max", ""}, {"gpt-6.1-sol-ultra", "", ""}, {"gpt-6-sol", "none", ""},
			{"api/claude-opus-4-6", "xhigh", ""}, {"api/claude-haiku-4-5", "high", ""},
			{"api/claude-sonnet-5-5", "high", "fast"}, {"gpt-6.1-sol", "high", "turbo"},
		} {
			t.Run(tc.model+tc.effort+tc.speed, func(t *testing.T) {
				var calls atomic.Int64
				service := fakeCodexService{complete: func(context.Context, codex.Request) (codex.Completion, error) {
					calls.Add(1)
					return codex.Completion{}, nil
				}, stream: func(context.Context, codex.Request) (<-chan codex.StreamEvent, error) {
					calls.Add(1)
					ch := make(chan codex.StreamEvent)
					close(ch)
					return ch, nil
				}}
				app := New(config.Defaults(), WithCodexService(service), WithLogOutput(io.Discard))
				resp := doJSON(t, app, mustJSON(t, openai.ChatCompletionRequest{Model: tc.model, ReasoningEffort: tc.effort, Speed: tc.speed, Stream: stream, Messages: []openai.ChatMessage{{Role: "user", Content: openai.TextContent("hello")}}}))
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusBadRequest || calls.Load() != 0 {
					t.Fatalf("status %d calls %d", resp.StatusCode, calls.Load())
				}
			})
		}
	}
}

func TestCurrentModelsRouteWithExactEffortAndSpeed(t *testing.T) {
	for _, tc := range []struct{ public, model, effort, speed, tier, provider string }{
		{"gpt-6.1-sol-fast-max", "gpt-6.1-sol", "max", "fast", "priority", codex.ProviderCodex},
		{"gpt-5.6-luna-normal-max", "gpt-5.6-luna", "max", "normal", "", codex.ProviderCodex},
		{"api/claude-opus-5-5-fast-xhigh", "claude-opus-5-5", "xhigh", "fast", "", codex.ProviderClaude},
		{"api/claude-sonnet-4-6-normal-max", "api/claude-sonnet-4-6", "max", "normal", "", codex.ProviderClaude},
		{"claude-sonnet-4-6", "claude-sonnet-4-6", "medium", "", "", codex.ProviderGemini},
	} {
		t.Run(tc.public, func(t *testing.T) {
			provider := ""
			var received codex.Request
			service := func(name string) fakeCodexService {
				return fakeCodexService{complete: func(_ context.Context, req codex.Request) (codex.Completion, error) {
					provider = name
					received = req
					return codex.Completion{Text: "hello", Model: req.Model}, nil
				}}
			}
			router := codex.Router{Codex: service(codex.ProviderCodex), Claude: service(codex.ProviderClaude), Gemini: service(codex.ProviderGemini)}
			app := New(config.Defaults(), WithCodexService(router), WithLogOutput(io.Discard))
			resp := doJSON(t, app, mustJSON(t, openai.ChatCompletionRequest{Model: tc.public, Messages: []openai.ChatMessage{{Role: "user", Content: openai.TextContent("hello")}}}))
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK || provider != tc.provider || received.Model != tc.model || received.ReasoningEffort != tc.effort || received.Speed != tc.speed || received.ServiceTier != tc.tier {
				t.Fatalf("status=%d provider=%s request=%#v", resp.StatusCode, provider, received)
			}
		})
	}
}
