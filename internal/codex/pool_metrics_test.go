package codex

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	metricspkg "github.com/teslashibe/codex-chat-api/internal/metrics"
)

func TestPooledServiceRecordsCooldownRotationAndSkipMetrics(t *testing.T) {
	metrics := metricspkg.New(true)
	pool, err := NewPooledService(PooledServiceConfig{
		Clients: []PooledClientConfig{
			{
				Label: "client-a",
				Service: poolFakeService{stream: func(context.Context, Request) (<-chan StreamEvent, error) {
					return nil, poolQuotaError()
				}},
			},
			{
				Label: "client-b",
				Service: poolFakeService{stream: func(context.Context, Request) (<-chan StreamEvent, error) {
					return poolEvents(StreamEvent{Delta: "ok"}, StreamEvent{Done: true}), nil
				}},
			},
		},
		LogOutput: io.Discard,
		Metrics:   metrics,
	})
	if err != nil {
		t.Fatalf("NewPooledService() error = %v", err)
	}

	req := Request{
		AffinityKey:     "raw-tenant@example.test Bearer secret-access-token full-model-prompt-hash",
		AffinityKeyHash: "full-model-prompt-hash",
		AffinityKeyMode: "body:tenant_id",
	}
	for pool.selectIndex(req) != 0 {
		req.AffinityKey += "-next"
	}

	for attempt := 0; attempt < 2; attempt++ {
		completion, err := pool.Complete(context.Background(), req)
		if err != nil {
			t.Fatalf("Complete() attempt %d error = %v", attempt, err)
		}
		if completion.Text != "ok" {
			t.Fatalf("Complete() attempt %d text = %q, want ok", attempt, completion.Text)
		}
	}

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body, err := io.ReadAll(recorder.Result().Body)
	if err != nil {
		t.Fatalf("read metrics: %v", err)
	}
	text := string(body)
	for _, want := range []string{
		`codex_chat_api_pool_cooldowns_total{client_label="client-a",failure_class="quota"} 1`,
		`codex_chat_api_pool_cooldown_skips_total{client_label="client-a",failure_class="quota"} 1`,
		`codex_chat_api_pool_selections_total{client_label="client-b",result="rotated"} 2`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics body missing %q:\n%s", want, text)
		}
	}
	for _, secret := range []string{"raw-tenant@example.test", "secret-access-token", "full-model-prompt-hash"} {
		if strings.Contains(text, secret) {
			t.Fatalf("metrics leaked %q:\n%s", secret, text)
		}
	}
}
