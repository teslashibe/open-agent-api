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
	client.observeRateLimitEvent([]byte(`{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":42,"window_minutes":300,"reset_at":1234,"secret":"do-not-leak"}},"unknown":{"raw":"prompt-secret"}}`))
	client.observeRateLimitEvent([]byte(`{"type":"unknown","rate_limits":{"primary":{"used_percent":999}}}`))
	client.observeRateLimitEvent([]byte(`not-json`))

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(recorder.Result().Body)
	text := string(body)
	for _, want := range []string{
		`codex_chat_api_upstream_rate_limit{client_label="primary",field="used_percent",limit_type="primary",source="event"} 42`,
		`codex_chat_api_upstream_rate_limit{client_label="primary",field="window_minutes",limit_type="primary",source="event"} 300`,
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

func TestObserveRateLimitHeadersUsesCodexWindowFields(t *testing.T) {
	metrics := metricspkg.New(true)
	client := &Client{metrics: metrics, clientLabel: "secondary"}
	response := &http.Response{Header: http.Header{
		"X-Codex-Primary-Used-Percent":   []string{"12.5"},
		"X-Codex-Primary-Window-Minutes": []string{"300"},
		"X-Codex-Primary-Reset-At":       []string{"1234"},
		"X-RateLimit-Remaining":          []string{"should-be-ignored"},
	}}
	client.observeRateLimitHeaders(response)

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(recorder.Result().Body)
	text := string(body)
	for _, want := range []string{
		`codex_chat_api_upstream_rate_limit{client_label="secondary",field="used_percent",limit_type="primary",source="header"} 12.5`,
		`codex_chat_api_upstream_rate_limit{client_label="secondary",field="window_minutes",limit_type="primary",source="header"} 300`,
		`codex_chat_api_upstream_rate_limit{client_label="secondary",field="reset_at",limit_type="primary",source="header"} 1234`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "should-be-ignored") {
		t.Fatalf("metrics included a generic undocumented header:\n%s", text)
	}
}
