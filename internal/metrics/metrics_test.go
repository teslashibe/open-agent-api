package metrics

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMetricsExposeStableBoundedSurface(t *testing.T) {
	m := New(true)
	m.ObserveRequest("codex", "success")
	m.ObserveRateLimit("codex", "quota")
	m.ObservePoolSelection("work-a", "rotated")
	m.ObservePoolCooldown("work-a", "quota")
	m.ObservePoolCooldownSkip("work-a", "quota")
	m.ObserveQueueWait("codex", "acquired", 125*time.Millisecond)
	m.IncActiveStreams("codex")
	m.DecActiveStreams("codex")

	body := scrape(t, m)
	for _, want := range []string{
		`codex_chat_api_requests_total{provider="codex",result="success"} 1`,
		`codex_chat_api_rate_limit_responses_total{failure_class="quota",provider="codex"} 1`,
		`codex_chat_api_pool_selections_total{client_label="work-a",result="rotated"} 1`,
		`codex_chat_api_pool_cooldowns_total{client_label="work-a",failure_class="quota"} 1`,
		`codex_chat_api_pool_cooldown_skips_total{client_label="work-a",failure_class="quota"} 1`,
		`codex_chat_api_queue_wait_seconds_count{provider="codex",result="acquired"} 1`,
		`codex_chat_api_active_streams{provider="codex"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics body missing %q:\n%s", want, body)
		}
	}
}

func TestMetricsNormalizeUntrustedLabels(t *testing.T) {
	m := New(true)
	secrets := []string{
		"raw-tenant@example.test",
		"Bearer secret-access-token",
		"full-model-prompt-hash-0123456789abcdef",
	}
	m.ObserveRequest(secrets[0], secrets[1])
	m.ObserveRateLimit(secrets[0], secrets[2])
	m.ObservePoolSelection(secrets[1], secrets[2])
	m.ObserveQueueWait(secrets[0], secrets[2], time.Second)

	body := scrape(t, m)
	for _, secret := range secrets {
		if strings.Contains(body, secret) {
			t.Fatalf("metrics leaked untrusted label %q:\n%s", secret, body)
		}
	}
	for _, want := range []string{`provider="unknown"`, `client_label="invalid"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics body missing normalized label %q:\n%s", want, body)
		}
	}
}

func TestDisabledMetricsAreNoopAndNotFound(t *testing.T) {
	m := New(false)
	m.ObserveRequest("codex", "success")

	recorder := httptest.NewRecorder()
	m.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	if recorder.Code != 404 {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
}

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	m.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	if recorder.Code != 200 {
		t.Fatalf("scrape status = %d, want 200", recorder.Code)
	}
	body, err := io.ReadAll(recorder.Result().Body)
	if err != nil {
		t.Fatalf("read scrape: %v", err)
	}
	return string(body)
}
