package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/teslashibe/open-chat-api/internal/codex"
	"github.com/teslashibe/open-chat-api/internal/config"
	"github.com/teslashibe/open-chat-api/internal/openai"
)

func TestBearerAuthAcceptsMatchingSecret(t *testing.T) {
	cfg := config.Defaults()
	cfg.GatewayBearerSecret = "gateway-secret"
	service := fakeCodexService{
		complete: func(ctx context.Context, req codex.Request) (codex.Completion, error) {
			return codex.Completion{Text: "ok", Model: req.Model}, nil
		},
	}
	app := New(cfg, WithCodexService(service), WithLogOutput(io.Discard), fixedServerOptions())

	resp := doJSON(t, app, `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"hi"}]}`, map[string]string{
		"Authorization": "Bearer gateway-secret",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestBearerAuthRejectsWithoutUpstreamCall(t *testing.T) {
	tests := []struct {
		name          string
		authorization string
	}{
		{name: "missing", authorization: ""},
		{name: "wrong secret", authorization: "Bearer wrong-secret"},
		{name: "no bearer prefix", authorization: "gateway-secret"},
		{name: "basic scheme", authorization: "Basic Z2F0ZXdheS1zZWNyZXQ="},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.GatewayBearerSecret = "gateway-secret"
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

			headers := map[string]string{"Authorization": tc.authorization}
			resp := doJSON(t, app, `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"hi"}]}`, headers)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
			}
			var body openai.ErrorResponse
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Error.Type != "authentication_error" {
				t.Fatalf("error type = %q, want authentication_error", body.Error.Type)
			}
			if calls := upstreamCalls.Load(); calls != 0 {
				t.Fatalf("upstream calls = %d, want 0", calls)
			}
		})
	}
}

func TestBearerAuthMissingHeaderRejected(t *testing.T) {
	cfg := config.Defaults()
	cfg.GatewayBearerSecret = "gateway-secret"
	app := New(cfg, WithLogOutput(io.Discard), fixedServerOptions())

	req, err := http.NewRequest(http.MethodGet, "/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestBearerAuthDisabledAllowsArbitraryAuthorization(t *testing.T) {
	service := fakeCodexService{
		complete: func(ctx context.Context, req codex.Request) (codex.Completion, error) {
			return codex.Completion{Text: "ok", Model: req.Model}, nil
		},
	}
	app := New(config.Defaults(), WithCodexService(service), WithLogOutput(io.Discard), fixedServerOptions())

	// doJSON sends a Cursor-style arbitrary bearer token by default.
	resp := doJSON(t, app, `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestHealthReachableWithoutBearer(t *testing.T) {
	cfg := config.Defaults()
	cfg.GatewayBearerSecret = "gateway-secret"
	app := New(cfg, WithLogOutput(io.Discard), fixedServerOptions())

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
}
