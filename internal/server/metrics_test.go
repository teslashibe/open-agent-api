package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/teslashibe/codex-chat-api/internal/codex"
	"github.com/teslashibe/codex-chat-api/internal/config"
	metricspkg "github.com/teslashibe/codex-chat-api/internal/metrics"
)

func TestMetricsEndpointRecordsRequestsAndQueueWait(t *testing.T) {
	cfg := config.Defaults()
	cfg.DegenerateTurnRetryEnabled = false
	metrics := metricspkg.New(true)
	service := fakeCodexService{complete: func(context.Context, codex.Request) (codex.Completion, error) {
		return codex.Completion{Text: "ok"}, nil
	}}
	app := New(cfg, WithCodexService(service), WithMetrics(metrics), WithLogOutput(io.Discard), fixedServerOptions())

	resp := doJSON(t, app, `{
		"model":"gpt-5.6-sol",
		"messages":[{"role":"user","content":"full-model-prompt-hash"}],
		"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]
	}`, map[string]string{
		cfg.GatewayTenantHeader: "raw-tenant@example.test",
		"Authorization":         "Bearer secret-access-token",
	})
	resp.Body.Close()

	body := scrapeServerMetrics(t, app, nil)
	for _, want := range []string{
		`codex_chat_api_requests_total{phase="complete",provider="codex",result="success"} 1`,
		`codex_chat_api_queue_wait_seconds_count{provider="codex",result="acquired"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics body missing %q:\n%s", want, body)
		}
	}
	for _, secret := range []string{"raw-tenant@example.test", "secret-access-token", "full-model-prompt-hash"} {
		if strings.Contains(body, secret) {
			t.Fatalf("metrics leaked %q:\n%s", secret, body)
		}
	}
}

func TestMetricsEndpointRecordsQueueBypassForOrdinaryChat(t *testing.T) {
	cfg := config.Defaults()
	metrics := metricspkg.New(true)
	service := fakeCodexService{complete: func(context.Context, codex.Request) (codex.Completion, error) {
		return codex.Completion{Text: "ok"}, nil
	}}
	app := New(cfg, WithCodexService(service), WithMetrics(metrics), WithLogOutput(io.Discard), fixedServerOptions())

	resp := doJSON(t, app, `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"hi"}]}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	body := scrapeServerMetrics(t, app, nil)
	for _, want := range []string{
		`codex_chat_api_requests_total{phase="complete",provider="codex",result="success"} 1`,
		`codex_chat_api_queue_wait_seconds_count{provider="codex",result="bypassed"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics body missing %q:\n%s", want, body)
		}
	}
}

func TestMetricsEndpointUsesGatewayBearerWhenConfigured(t *testing.T) {
	cfg := config.Defaults()
	cfg.GatewayBearerSecret = "gateway-secret"
	app := New(cfg, WithMetrics(metricspkg.New(true)), WithLogOutput(io.Discard), fixedServerOptions())

	req, err := http.NewRequest(http.MethodGet, "/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status without bearer = %d, want 401", resp.StatusCode)
	}

	scrapeServerMetrics(t, app, map[string]string{"Authorization": "Bearer gateway-secret"})
}

func TestMetricsEndpointDisabledReturnsNotFound(t *testing.T) {
	cfg := config.Defaults()
	cfg.MetricsEnabled = false
	app := New(cfg, WithMetrics(metricspkg.New(false)), WithLogOutput(io.Discard), fixedServerOptions())

	req, err := http.NewRequest(http.MethodGet, "/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestMetricsEndpointRecordsQuota429(t *testing.T) {
	cfg := config.Defaults()
	cfg.QuotaFallbackModel = ""
	metrics := metricspkg.New(true)
	quotaErr := codex.NewError(
		codex.ErrorKindUpstream,
		http.StatusTooManyRequests,
		"usage limit reached",
		fmt.Errorf("%w: quota", codex.ErrUsageLimitReached),
	)
	service := fakeCodexService{complete: func(context.Context, codex.Request) (codex.Completion, error) {
		return codex.Completion{}, quotaErr
	}}
	app := New(cfg, WithCodexService(service), WithMetrics(metrics), WithLogOutput(io.Discard), fixedServerOptions())

	resp := doJSON(t, app, `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"hi"}]}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}

	body := scrapeServerMetrics(t, app, nil)
	for _, want := range []string{
		`codex_chat_api_requests_total{phase="complete",provider="codex",result="rate_limited"} 1`,
		`codex_chat_api_rate_limit_responses_total{failure_class="quota",provider="codex"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics body missing %q:\n%s", want, body)
		}
	}
}

func TestActiveStreamsGaugeReturnsToZero(t *testing.T) {
	cfg := config.Defaults()
	metrics := metricspkg.New(true)
	service := fakeCodexService{stream: func(context.Context, codex.Request) (<-chan codex.StreamEvent, error) {
		events := make(chan codex.StreamEvent, 2)
		events <- codex.StreamEvent{Delta: "ok"}
		events <- codex.StreamEvent{Done: true}
		close(events)
		return events, nil
	}}
	app := New(cfg, WithCodexService(service), WithMetrics(metrics), WithLogOutput(io.Discard), fixedServerOptions())

	resp := doJSON(t, app, `{"model":"gpt-5.6-sol","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	_, err := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("read stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	body := scrapeServerMetrics(t, app, nil)
	for _, want := range []string{
		`codex_chat_api_active_streams{provider="codex"} 0`,
		`codex_chat_api_requests_total{phase="complete",provider="codex",result="success"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics body missing %q:\n%s", want, body)
		}
	}
}

func TestMetricsEndpointRecordsStreamingTerminalOutcomes(t *testing.T) {
	tests := []struct {
		name   string
		events []codex.StreamEvent
		want   string
	}{
		{
			name: "first event upstream error",
			events: []codex.StreamEvent{{Err: codex.NewError(
				codex.ErrorKindUpstream,
				http.StatusBadGateway,
				"upstream failed",
				errors.New("first event failed"),
			)}},
			want: `codex_chat_api_requests_total{phase="first_event",provider="codex",result="upstream_error"} 1`,
		},
		{
			name: "mid stream upstream error",
			events: []codex.StreamEvent{
				{Delta: "partial"},
				{Err: codex.NewError(
					codex.ErrorKindUpstream,
					http.StatusBadGateway,
					"upstream failed",
					errors.New("mid stream failed"),
				)},
			},
			want: `codex_chat_api_requests_total{phase="mid_stream",provider="codex",result="upstream_error"} 1`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metrics := metricspkg.New(true)
			service := fakeCodexService{stream: func(context.Context, codex.Request) (<-chan codex.StreamEvent, error) {
				events := make(chan codex.StreamEvent, len(tt.events))
				for _, event := range tt.events {
					events <- event
				}
				close(events)
				return events, nil
			}}
			app := New(config.Defaults(), WithCodexService(service), WithMetrics(metrics), WithLogOutput(io.Discard), fixedServerOptions())

			resp := doJSON(t, app, `{"model":"gpt-5.6-sol","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
			_, readErr := io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if readErr != nil && !errors.Is(readErr, context.Canceled) {
				t.Fatalf("read stream: %v", readErr)
			}

			body := scrapeServerMetrics(t, app, nil)
			if !strings.Contains(body, tt.want) {
				t.Fatalf("metrics body missing %q:\n%s", tt.want, body)
			}
			if strings.Contains(body, `codex_chat_api_requests_total{phase="complete",provider="codex",result="success"}`) {
				t.Fatalf("failed stream was also counted as success:\n%s", body)
			}
		})
	}
}

func TestMetricsEndpointRecordsStreamingConnectFailure(t *testing.T) {
	metrics := metricspkg.New(true)
	service := fakeCodexService{stream: func(context.Context, codex.Request) (<-chan codex.StreamEvent, error) {
		return nil, codex.NewError(codex.ErrorKindUpstream, http.StatusBadGateway, "connect failed", errors.New("offline"))
	}}
	app := New(config.Defaults(), WithCodexService(service), WithMetrics(metrics), WithLogOutput(io.Discard), fixedServerOptions())

	resp := doJSON(t, app, `{"model":"gpt-5.6-sol","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}

	body := scrapeServerMetrics(t, app, nil)
	want := `codex_chat_api_requests_total{phase="connect",provider="codex",result="server_error"} 1`
	if !strings.Contains(body, want) {
		t.Fatalf("metrics body missing %q:\n%s", want, body)
	}
}

func TestMetricsEndpointRecordsStreamingCancellation(t *testing.T) {
	metrics := metricspkg.New(true)
	service := fakeCodexService{stream: func(context.Context, codex.Request) (<-chan codex.StreamEvent, error) {
		events := make(chan codex.StreamEvent, 1)
		events <- codex.StreamEvent{Done: true}
		close(events)
		return events, nil
	}}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	app := New(
		config.Defaults(),
		WithCodexService(service),
		WithMetrics(metrics),
		WithLogOutput(io.Discard),
		fixedServerOptions(),
		func(opts *options) {
			opts.requestContext = func(*fiber.Ctx) context.Context { return canceled }
		},
	)

	resp := doJSON(t, app, `{"model":"gpt-5.6-sol","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	body := scrapeServerMetrics(t, app, nil)
	want := `codex_chat_api_requests_total{phase="connect",provider="codex",result="canceled"} 1`
	if !strings.Contains(body, want) {
		t.Fatalf("metrics body missing %q:\n%s", want, body)
	}
	if strings.Contains(body, `codex_chat_api_requests_total{phase="complete",provider="codex",result="success"}`) {
		t.Fatalf("canceled stream was also counted as success:\n%s", body)
	}
}

func scrapeServerMetrics(t *testing.T, app *fiber.App, headers map[string]string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read metrics: %v", err)
	}
	return string(body)
}
