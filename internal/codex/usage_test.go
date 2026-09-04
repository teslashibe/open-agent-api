package codex

import (
	"context"
	"errors"
	"fmt"
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

func TestRedeemResetCreditSuccessRetryFailuresAndCacheRefresh(t *testing.T) {
	const requestID = "16fd2706-8baf-433b-82eb-8c7fada847da"
	var redeemCalls, usageCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access-secret" || r.Header.Get("ChatGPT-Account-ID") != "account-secret" {
			t.Fatal("missing account credentials")
		}
		switch r.URL.Path {
		case "/consume":
			redeemCalls.Add(1)
			body, _ := io.ReadAll(r.Body)
			if string(body) != `{"redeem_request_id":"`+requestID+`"}` {
				t.Fatalf("body = %s", body)
			}
			code := "reset"
			if redeemCalls.Load() > 1 {
				code = "already_redeemed"
			}
			io.WriteString(w, `{"code":"`+code+`","windows_reset":2,"secret":"ignored"}`)
		case "/usage":
			call := usageCalls.Add(1)
			io.WriteString(w, fmt.Sprintf(`{"rate_limit":{"primary_window":{"used_percent":%d,"limit_window_seconds":18000,"reset_at":1780000000}},"rate_limit_reset_credits":{"available_count":1}}`, 50-call))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	monitor := NewUsageMonitor([]UsageAccount{{
		Label: "safe-label",
		Source: usageSourceFunc(func(context.Context) (auth.Credentials, error) {
			return auth.Credentials{AccessToken: "access-secret", AccountID: "account-secret"}, nil
		}),
	}}, nil)
	monitor.url = upstream.URL + "/usage"
	monitor.redeemURL = upstream.URL + "/consume"
	monitor.Usage(context.Background())
	if usageCalls.Load() != 1 {
		t.Fatal("usage cache not primed")
	}
	first, err := monitor.Redeem(context.Background(), "safe-label", requestID)
	if err != nil || first.Outcome != "reset" || first.WindowsReset != 2 || first.Usage.Windows[0].UsedPercent != 48 {
		t.Fatalf("first = %#v, err %v", first, err)
	}
	second, err := monitor.Redeem(context.Background(), "safe-label", requestID)
	if err != nil || second.Outcome != "already_redeemed" || usageCalls.Load() != 3 {
		t.Fatalf("second = %#v, err %v, usage calls %d", second, err, usageCalls.Load())
	}
	monitor.Usage(context.Background())
	if usageCalls.Load() != 3 {
		t.Fatalf("refreshed usage should be cached; calls = %d", usageCalls.Load())
	}

	if _, err := monitor.Redeem(context.Background(), "missing", requestID); redemptionCode(err) != "unknown_account" {
		t.Fatalf("unknown label err = %v", err)
	}
}

func TestRedeemResetCreditAuthUpstreamTimeoutAndNoSecretLeak(t *testing.T) {
	const requestID = "16fd2706-8baf-433b-82eb-8c7fada847da"
	tests := []struct {
		name       string
		source     usageSourceFunc
		handler    http.Handler
		timeout    time.Duration
		wantCode   string
		wantStatus int
	}{
		{"auth", func(context.Context) (auth.Credentials, error) {
			return auth.Credentials{}, errors.New("access-secret account-secret /secret/auth.json")
		}, http.NotFoundHandler(), time.Second, "auth_error", http.StatusBadGateway},
		{"upstream", validUsageSource, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "access-secret", http.StatusInternalServerError)
		}), time.Second, "upstream_error", http.StatusBadGateway},
		{"timeout", validUsageSource, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(50 * time.Millisecond)
		}), 10 * time.Millisecond, "timeout", http.StatusGatewayTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := httptest.NewServer(tt.handler)
			defer upstream.Close()
			monitor := NewUsageMonitor([]UsageAccount{{Label: "safe", Source: tt.source}}, nil)
			monitor.redeemURL, monitor.timeout = upstream.URL, tt.timeout
			_, err := monitor.Redeem(context.Background(), "safe", requestID)
			var redemptionErr *RedemptionError
			if !errors.As(err, &redemptionErr) || redemptionErr.Code != tt.wantCode || redemptionErr.HTTPStatus != tt.wantStatus {
				t.Fatalf("err = %#v", err)
			}
			for _, secret := range []string{"access-secret", "account-secret", "auth.json"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaked %q: %v", secret, err)
				}
			}
		})
	}
}

func TestRedeemInvalidationRejectsInFlightStaleUsageCacheWrite(t *testing.T) {
	const requestID = "16fd2706-8baf-433b-82eb-8c7fada847da"
	staleStarted := make(chan struct{})
	releaseStale := make(chan struct{})
	var usageCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/consume":
			io.WriteString(w, `{"code":"reset","windows_reset":1}`)
		case "/usage":
			switch usageCalls.Add(1) {
			case 1:
				close(staleStarted)
				<-releaseStale
				io.WriteString(w, usageJSON(90))
			default:
				io.WriteString(w, usageJSON(10))
			}
		}
	}))
	defer upstream.Close()
	monitor := NewUsageMonitor([]UsageAccount{{Label: "safe", Source: usageSourceFunc(validUsageSource)}}, nil)
	monitor.url, monitor.redeemURL = upstream.URL+"/usage", upstream.URL+"/consume"

	staleDone := make(chan UsageResponse, 1)
	go func() { staleDone <- monitor.Usage(context.Background()) }()
	<-staleStarted
	result, err := monitor.Redeem(context.Background(), "safe", requestID)
	if err != nil || result.Usage.Windows[0].UsedPercent != 10 {
		t.Fatalf("redeem = %#v, err %v", result, err)
	}
	close(releaseStale)
	if stale := <-staleDone; stale.Accounts[0].Windows[0].UsedPercent != 90 {
		t.Fatalf("in-flight response = %#v", stale)
	}
	cached := monitor.Usage(context.Background())
	if cached.Accounts[0].Windows[0].UsedPercent != 10 || usageCalls.Load() != 2 {
		t.Fatalf("cache restored stale data: %#v, calls %d", cached, usageCalls.Load())
	}
}

func TestRedeemRefreshGetsFreshBoundedTimeout(t *testing.T) {
	const requestID = "16fd2706-8baf-433b-82eb-8c7fada847da"
	redeemDelay := 35 * time.Millisecond
	refreshDelay := 20 * time.Millisecond
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/consume":
			time.Sleep(redeemDelay)
			io.WriteString(w, `{"code":"reset","windows_reset":1}`)
		case "/usage":
			time.Sleep(refreshDelay)
			io.WriteString(w, usageJSON(7))
		}
	}))
	defer upstream.Close()
	monitor := NewUsageMonitor([]UsageAccount{{Label: "safe", Source: usageSourceFunc(validUsageSource)}}, nil)
	monitor.url, monitor.redeemURL = upstream.URL+"/usage", upstream.URL+"/consume"
	monitor.timeout = 45 * time.Millisecond

	start := time.Now()
	result, err := monitor.Redeem(context.Background(), "safe", requestID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage.Status != "ok" || result.Usage.Windows[0].UsedPercent != 7 {
		t.Fatalf("refresh reused expiring redemption timeout: %#v", result.Usage)
	}
	if elapsed := time.Since(start); elapsed < redeemDelay+refreshDelay {
		t.Fatalf("request returned before both phases: %v", elapsed)
	}
}

func usageJSON(used int) string {
	return fmt.Sprintf(`{"rate_limit":{"primary_window":{"used_percent":%d,"limit_window_seconds":18000,"reset_at":1780000000}}}`, used)
}

func validUsageSource(context.Context) (auth.Credentials, error) {
	return auth.Credentials{AccessToken: "access-secret", AccountID: "account-secret"}, nil
}

func redemptionCode(err error) string {
	var redemptionErr *RedemptionError
	if errors.As(err, &redemptionErr) {
		return redemptionErr.Code
	}
	return ""
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
