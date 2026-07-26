package codex

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	metricspkg "github.com/teslashibe/open-agent-api/internal/metrics"
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

	// First request: hash prefers cooling client-a, rotates to client-b, and soft-pins.
	completion, err := pool.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("Complete() first error = %v", err)
	}
	if completion.Text != "ok" {
		t.Fatalf("Complete() first text = %q, want ok", completion.Text)
	}

	// Second request: soft-pin serves client-b directly (no cooldown skip).
	completion, err = pool.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("Complete() pinned error = %v", err)
	}
	if completion.Text != "ok" {
		t.Fatalf("Complete() pinned text = %q, want ok", completion.Text)
	}

	// Third request: a fresh affinity key that still hashes to client-a exercises
	// cooldown-skip observability without the soft-pin short-circuit.
	skipReq := Request{
		AffinityKey:     "other-tenant@example.test Bearer other-access-token other-model-prompt-hash",
		AffinityKeyHash: "other-model-prompt-hash",
		AffinityKeyMode: "body:tenant_id",
	}
	for pool.selectIndex(skipReq) != 0 {
		skipReq.AffinityKey += "-next"
	}
	completion, err = pool.Complete(context.Background(), skipReq)
	if err != nil {
		t.Fatalf("Complete() skip-probe error = %v", err)
	}
	if completion.Text != "ok" {
		t.Fatalf("Complete() skip-probe text = %q, want ok", completion.Text)
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
		`codex_chat_api_pool_selections_total{client_label="client-b",result="pinned"} 1`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics body missing %q:\n%s", want, text)
		}
	}
	for _, secret := range []string{
		"raw-tenant@example.test",
		"secret-access-token",
		"full-model-prompt-hash",
		"other-tenant@example.test",
		"other-access-token",
		"other-model-prompt-hash",
	} {
		if strings.Contains(text, secret) {
			t.Fatalf("metrics leaked %q:\n%s", secret, text)
		}
	}
}
