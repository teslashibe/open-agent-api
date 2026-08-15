package codex

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metricspkg "github.com/teslashibe/open-agent-api/internal/metrics"
)

func TestObserveRateLimitEventRecognizedFieldsOnly(t *testing.T) {
	metrics := metricspkg.New(true)
	client := &Client{metrics: metrics, clientLabel: "primary"}
	client.observeRateLimitEvent([]byte(`{"type":"codex.rate_limits","rate_limits":{"allowed":true,"limit_reached":false,"primary":{"used_percent":42,"window_minutes":60,"reset_at":1700000000,"secret":"do-not-leak"},"secondary":null},"credits":{"balance":"prompt-secret"}}`))
	client.observeRateLimitEvent([]byte(`{"type":"unknown","rate_limits":{"primary":{"used_percent":999}}}`))
	client.observeRateLimitEvent([]byte(`not-json`))

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(recorder.Result().Body)
	text := string(body)
	for _, want := range []string{
		`codex_chat_api_upstream_rate_limit{client_label="primary",field="used_percent",limit_type="primary",source="event"} 42`,
		`codex_chat_api_upstream_rate_limit{client_label="primary",field="window_minutes",limit_type="primary",source="event"} 60`,
		`codex_chat_api_upstream_rate_limit{client_label="primary",field="reset_at",limit_type="primary",source="event"} 1.7e+09`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics missing %q:\n%s", want, text)
		}
	}
	for _, forbidden := range []string{"secret", "prompt-secret", "999"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("metrics leaked ignored value %q:\n%s", forbidden, text)
		}
	}
}

func TestObserveRateLimitEventSanitizesClientLabel(t *testing.T) {
	metrics := metricspkg.New(true)
	client := &Client{metrics: metrics, clientLabel: "person@example.test bearer-secret"}
	client.observeRateLimitEvent([]byte(`{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":1}}}`))

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(recorder.Result().Body)
	text := string(body)
	if !strings.Contains(text, `client_label="invalid"`) || strings.Contains(text, "person@example.test") {
		t.Fatalf("unsafe client label was not sanitized:\n%s", text)
	}
}

func TestObserveRateLimitHeadersUsesCodexWindowSchema(t *testing.T) {
	metrics := metricspkg.New(true)
	client := &Client{metrics: metrics, clientLabel: "secondary"}
	client.observeRateLimitHeaders(&http.Response{Header: http.Header{
		"X-Codex-Primary-Used-Percent":     []string{"12.5"},
		"X-Codex-Primary-Window-Minutes":   []string{"60"},
		"X-Codex-Primary-Reset-At":         []string{"1704069000"},
		"X-Codex-Secondary-Used-Percent":   []string{"87.5"},
		"X-Codex-Secondary-Window-Minutes": []string{"invalid"},
		"X-Codex-Secondary-Reset-At":       []string{"-1"},
		"X-RateLimit-Remaining":            []string{"999"},
		"X-Codex-Credits-Balance":          []string{"secret"},
	}})

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(recorder.Result().Body)
	text := string(body)
	for _, want := range []string{
		`field="used_percent",limit_type="primary",source="header"} 12.5`,
		`field="window_minutes",limit_type="primary",source="header"} 60`,
		`field="reset_at",limit_type="primary",source="header"} 1.704069e+09`,
		`field="used_percent",limit_type="secondary",source="header"} 87.5`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics missing %q:\n%s", want, text)
		}
	}
	for _, forbidden := range []string{"999", "secret", `limit_type="secondary",source="header"} -1`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("metrics exposed unsupported or invalid value %q:\n%s", forbidden, text)
		}
	}
}
