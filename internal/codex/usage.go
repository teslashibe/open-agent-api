package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/teslashibe/open-agent-api/internal/auth"
	metricspkg "github.com/teslashibe/open-agent-api/internal/metrics"
)

const (
	defaultUsageURL     = "https://chatgpt.com/backend-api/wham/usage"
	defaultUsageTimeout = 5 * time.Second
	defaultUsageTTL     = 30 * time.Second
)

type UsageWindow struct {
	Type        string    `json:"type"`
	UsedPercent float64   `json:"used_percent"`
	Duration    int64     `json:"duration_seconds"`
	ResetAt     time.Time `json:"reset_at"`
}

type AccountUsage struct {
	Label            string        `json:"label"`
	Windows          []UsageWindow `json:"windows"`
	BankedResetCount int           `json:"banked_reset_count"`
	ObservedAt       time.Time     `json:"observed_at"`
	Status           string        `json:"status"`
	ErrorCode        string        `json:"error_code,omitempty"`
}

type UsageResponse struct {
	Accounts []AccountUsage `json:"accounts"`
}

type credentialSource interface {
	Get(context.Context) (auth.Credentials, error)
}

type UsageAccount struct {
	Label  string
	Source credentialSource
}

type UsageMonitor struct {
	accounts   []UsageAccount
	httpClient *http.Client
	url        string
	timeout    time.Duration
	ttl        time.Duration
	now        func() time.Time
	metrics    *metricspkg.Metrics

	mu      sync.Mutex
	cached  UsageResponse
	expires time.Time
}

func NewUsageMonitor(accounts []UsageAccount, metrics *metricspkg.Metrics) *UsageMonitor {
	if metrics == nil {
		metrics = metricspkg.New(false)
	}
	return &UsageMonitor{
		accounts:   accounts,
		httpClient: http.DefaultClient,
		url:        defaultUsageURL,
		timeout:    defaultUsageTimeout,
		ttl:        defaultUsageTTL,
		now:        time.Now,
		metrics:    metrics,
	}
}

func (m *UsageMonitor) Usage(ctx context.Context) UsageResponse {
	now := m.now()
	m.mu.Lock()
	if now.Before(m.expires) {
		response := m.cached
		m.mu.Unlock()
		return response
	}
	m.mu.Unlock()

	accounts := make([]AccountUsage, len(m.accounts))
	var wg sync.WaitGroup
	for i, account := range m.accounts {
		wg.Add(1)
		go func(i int, account UsageAccount) {
			defer wg.Done()
			accountCtx, cancel := context.WithTimeout(ctx, m.timeout)
			defer cancel()
			accounts[i] = m.fetch(accountCtx, account)
		}(i, account)
	}
	wg.Wait()

	response := UsageResponse{Accounts: accounts}
	m.mu.Lock()
	m.cached = response
	m.expires = m.now().Add(m.ttl)
	m.mu.Unlock()
	return response
}

func (m *UsageMonitor) fetch(ctx context.Context, account UsageAccount) AccountUsage {
	observedAt := m.now().UTC()
	result := AccountUsage{Label: account.Label, ObservedAt: observedAt, Status: "error"}
	creds, err := account.Source.Get(ctx)
	if err != nil {
		result.ErrorCode = usageErrorCode(ctx, "auth_error")
		m.metrics.ObserveCodexUsage(account.Label, result.ErrorCode, 0, observedAt)
		return result
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.url, nil)
	if err != nil {
		result.ErrorCode = "invalid_response"
		m.metrics.ObserveCodexUsage(account.Label, result.ErrorCode, 0, observedAt)
		return result
	}
	req.Header.Set("Authorization", "Bearer "+creds.AccessToken)
	req.Header.Set("ChatGPT-Account-ID", creds.AccountID)
	req.Header.Set("Accept", "application/json")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		result.ErrorCode = usageErrorCode(ctx, "upstream_error")
		m.metrics.ObserveCodexUsage(account.Label, result.ErrorCode, 0, observedAt)
		return result
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		result.ErrorCode = "auth_error"
		m.metrics.ObserveCodexUsage(account.Label, result.ErrorCode, 0, observedAt)
		return result
	}
	if resp.StatusCode != http.StatusOK {
		result.ErrorCode = "upstream_error"
		m.metrics.ObserveCodexUsage(account.Label, result.ErrorCode, 0, observedAt)
		return result
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		result.ErrorCode = "invalid_response"
		m.metrics.ObserveCodexUsage(account.Label, result.ErrorCode, 0, observedAt)
		return result
	}
	parsed, err := parseUsage(body)
	if err != nil {
		result.ErrorCode = "invalid_response"
		m.metrics.ObserveCodexUsage(account.Label, result.ErrorCode, 0, observedAt)
		return result
	}
	result.Windows = parsed.windows
	result.BankedResetCount = parsed.resetCount
	result.Status = "ok"
	m.metrics.ObserveCodexUsage(account.Label, "success", result.BankedResetCount, observedAt)
	for _, window := range result.Windows {
		m.metrics.SetCodexRateLimit(account.Label, "usage", window.Type, "used_percent", window.UsedPercent)
		m.metrics.SetCodexRateLimit(account.Label, "usage", window.Type, "window_minutes", float64(window.Duration)/60)
		m.metrics.SetCodexRateLimit(account.Label, "usage", window.Type, "reset_at", float64(window.ResetAt.Unix()))
	}
	return result
}

type parsedUsage struct {
	windows    []UsageWindow
	resetCount int
}

func parseUsage(data []byte) (parsedUsage, error) {
	var body struct {
		RateLimit *struct {
			Primary   *rawUsageWindow `json:"primary_window"`
			Secondary *rawUsageWindow `json:"secondary_window"`
		} `json:"rate_limit"`
		ResetCredits *struct {
			AvailableCount int `json:"available_count"`
		} `json:"rate_limit_reset_credits"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return parsedUsage{}, fmt.Errorf("decode usage: %w", err)
	}
	if body.RateLimit == nil {
		return parsedUsage{}, errors.New("usage response missing rate_limit")
	}
	result := parsedUsage{}
	for _, window := range []struct {
		kind string
		raw  *rawUsageWindow
	}{{"primary", body.RateLimit.Primary}, {"secondary", body.RateLimit.Secondary}} {
		if window.raw == nil {
			continue
		}
		if window.raw.UsedPercent < 0 || window.raw.UsedPercent > 100 || window.raw.Duration <= 0 || window.raw.ResetAt <= 0 {
			return parsedUsage{}, errors.New("usage response contains invalid window")
		}
		result.windows = append(result.windows, UsageWindow{
			Type:        window.kind,
			UsedPercent: window.raw.UsedPercent,
			Duration:    window.raw.Duration,
			ResetAt:     time.Unix(window.raw.ResetAt, 0).UTC(),
		})
	}
	if len(result.windows) == 0 {
		return parsedUsage{}, errors.New("usage response contains no windows")
	}
	if body.ResetCredits != nil && body.ResetCredits.AvailableCount > 0 {
		result.resetCount = body.ResetCredits.AvailableCount
	}
	return result, nil
}

type rawUsageWindow struct {
	UsedPercent float64 `json:"used_percent"`
	Duration    int64   `json:"limit_window_seconds"`
	ResetAt     int64   `json:"reset_at"`
}

func usageErrorCode(ctx context.Context, fallback string) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timeout"
	}
	return fallback
}
