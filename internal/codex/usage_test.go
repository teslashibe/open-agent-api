package codex

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teslashibe/open-agent-api/internal/auth"
	metricspkg "github.com/teslashibe/open-agent-api/internal/metrics"
)

type usageSourceFunc func(context.Context) (auth.Credentials, error)

func (f usageSourceFunc) Get(ctx context.Context) (auth.Credentials, error) {
	return f(ctx)
}

func TestParseUsageAndMissingResetCredits(t *testing.T) {
	parsed, err := parseUsage([]byte(`{
		"email":"secret@example.test",
		"account_id":"secret-account",
		"rate_limit":{
			"primary_window":{"used_percent":25.5,"limit_window_seconds":18000,"reset_at":1780000000},
			"secondary_window":{"used_percent":5,"limit_window_seconds":604800,"reset_at":1780500000}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.resetCount != 0 {
		t.Fatalf("reset count = %d, want 0", parsed.resetCount)
	}
	if len(parsed.windows) != 2 || parsed.windows[0].Type != "primary" || parsed.windows[0].Duration != 18000 || parsed.windows[0].UsedPercent != 25.5 {
		t.Fatalf("windows = %#v", parsed.windows)
	}
}

func TestParseUsagePreservesSecondaryWindowTypeWithoutPrimary(t *testing.T) {
	parsed, err := parseUsage([]byte(`{
		"rate_limit":{
			"secondary_window":{"used_percent":5,"limit_window_seconds":604800,"reset_at":1780500000}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.windows) != 1 || parsed.windows[0].Type != "secondary" {
		t.Fatalf("windows = %#v", parsed.windows)
	}
}

func TestUsageMonitorPartialFailureAuthHeadersCacheAndMetrics(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer access-secret" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("ChatGPT-Account-ID"); got != "account-secret" {
			t.Errorf("ChatGPT-Account-ID = %q", got)
		}
		io.WriteString(w, `{"rate_limit":{"primary_window":{"used_percent":42,"limit_window_seconds":18000,"reset_at":1780000000}},"rate_limit_reset_credits":{"available_count":2},"email":"secret@example.test"}`)
	}))
	defer upstream.Close()

	metrics := metricspkg.New(true)
	monitor := NewUsageMonitor([]UsageAccount{
		{Label: "good", Source: usageSourceFunc(func(context.Context) (auth.Credentials, error) {
			return auth.Credentials{AccessToken: "access-secret", AccountID: "account-secret"}, nil
		})},
		{Label: "bad", Source: usageSourceFunc(func(context.Context) (auth.Credentials, error) {
			return auth.Credentials{}, errors.New("path /secret/auth.json token access-secret")
		})},
	}, metrics)
	monitor.url = upstream.URL
	monitor.now = func() time.Time { return time.Unix(1770000000, 0).UTC() }

	first := monitor.Usage(context.Background())
	second := monitor.Usage(context.Background())
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want cached 1", calls.Load())
	}
	if len(first.Accounts) != 2 || first.Accounts[0].Status != "ok" || first.Accounts[0].BankedResetCount != 2 {
		t.Fatalf("first response = %#v", first)
	}
	if first.Accounts[1].ErrorCode != "auth_error" || second.Accounts[1].ErrorCode != "auth_error" {
		t.Fatalf("failure response = %#v", first.Accounts[1])
	}

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := recorder.Body.String()
	for _, want := range []string{
		`codex_chat_api_upstream_rate_limit{client_label="good",field="used_percent",limit_type="primary",source="usage"} 42`,
		`codex_chat_api_upstream_reset_credits{client_label="good"} 2`,
		`codex_chat_api_upstream_usage_last_success_timestamp_seconds{client_label="good"} 1.77e+09`,
		`codex_chat_api_upstream_usage_polls_total{client_label="good",result="success"} 1`,
		`codex_chat_api_upstream_usage_polls_total{client_label="bad",result="auth_error"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
}

func TestUsageMonitorTimesOutAccountWithoutFailingOthers(t *testing.T) {
	monitor := NewUsageMonitor([]UsageAccount{
		{Label: "slow", Source: usageSourceFunc(func(ctx context.Context) (auth.Credentials, error) {
			<-ctx.Done()
			return auth.Credentials{}, ctx.Err()
		})},
		{Label: "failed", Source: usageSourceFunc(func(context.Context) (auth.Credentials, error) {
			return auth.Credentials{}, errors.New("no credentials")
		})},
	}, nil)
	monitor.timeout = 20 * time.Millisecond

	start := time.Now()
	got := monitor.Usage(context.Background())
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("usage took %v", elapsed)
	}
	if got.Accounts[0].ErrorCode != "timeout" || got.Accounts[1].ErrorCode != "auth_error" {
		t.Fatalf("accounts = %#v", got.Accounts)
	}
}
