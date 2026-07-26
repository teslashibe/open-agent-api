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

	"github.com/teslashibe/open-chat-api/internal/codex"
	"github.com/teslashibe/open-chat-api/internal/config"
	metricspkg "github.com/teslashibe/open-chat-api/internal/metrics"
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
		`codex_chat_api_requests_total{provider="codex",result="success"} 1`,
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
		`codex_chat_api_requests_total{provider="codex",result="success"} 1`,
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
		`codex_chat_api_requests_total{provider="codex",result="rate_limited"} 1`,
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
	if !strings.Contains(body, `codex_chat_api_active_streams{provider="codex"} 0`) {
		t.Fatalf("active stream gauge did not return to zero:\n%s", body)
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
