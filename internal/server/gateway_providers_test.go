package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/teslashibe/open-agent-api/internal/codex"
	"github.com/teslashibe/open-agent-api/internal/config"
	"github.com/teslashibe/open-agent-api/internal/openai"
)

func TestModelsFiltersDisabledProviders(t *testing.T) {
	cfg := config.Defaults()
	cfg.GatewayProviders = []string{"codex", "gemini"}
	app := New(cfg, WithLogOutput(io.Discard))

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
	sawGemini := false
	sawCodex := false
	for _, model := range body.Data {
		// Claude Code provider must be omitted. Antigravity gateway IDs such as
		// claude-sonnet-4-6 route via Gemini and stay listed when Gemini is on.
		if provider := codex.ProviderForModel(openai.ResolveModelAlias(model.ID).UpstreamModel); provider == codex.ProviderClaude {
			t.Fatalf("model list contains Claude Code id %q", model.ID)
		}
		if strings.HasPrefix(model.ID, "gemini-") {
			sawGemini = true
		}
		if strings.HasPrefix(model.ID, "gpt-") {
			sawCodex = true
		}
	}
	if !sawGemini || !sawCodex {
		t.Fatalf("model list missing codex/gemini aliases: codex=%t gemini=%t", sawCodex, sawGemini)
	}
}

func TestChatCompletionsRejectsDisabledProvider(t *testing.T) {
	for _, model := range []string{"claude-sonnet-5", "sonnet", "anthropic/claude-sonnet-5", "api/claude-fable-5"} {
		t.Run(model, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.GatewayProviders = []string{"codex", "gemini"}
			var upstreamCalls atomic.Int64
			service := fakeCodexService{
				complete: func(ctx context.Context, req codex.Request) (codex.Completion, error) {
					upstreamCalls.Add(1)
					return codex.Completion{Text: "ok"}, nil
				},
				stream: func(ctx context.Context, req codex.Request) (<-chan codex.StreamEvent, error) {
					upstreamCalls.Add(1)
					events := make(chan codex.StreamEvent)
					close(events)
					return events, nil
				},
			}
			app := New(cfg, WithCodexService(service), WithLogOutput(io.Discard), fixedServerOptions())

			resp := doJSON(t, app, `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
			}
			var body openai.ErrorResponse
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Error.Type != "invalid_request_error" || body.Error.Message != "model not found" {
				t.Fatalf("error body = %#v, want invalid_request_error/model not found", body.Error)
			}
			if calls := upstreamCalls.Load(); calls != 0 {
				t.Fatalf("upstream calls = %d, want 0", calls)
			}
		})
	}
}

func TestChatCompletionsAllowsEnabledProviders(t *testing.T) {
	for _, model := range []string{"gpt-5.6-sol", "gemini-2.5-pro"} {
		t.Run(model, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.GatewayProviders = []string{"codex", "gemini"}
			service := fakeCodexService{
				complete: func(ctx context.Context, req codex.Request) (codex.Completion, error) {
					return codex.Completion{Text: "ok", Model: req.Model}, nil
				},
			}
			app := New(cfg, WithCodexService(service), WithLogOutput(io.Discard), fixedServerOptions())

			resp := doJSON(t, app, `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
			}
		})
	}
}
