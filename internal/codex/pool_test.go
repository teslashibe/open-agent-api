package codex

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestPooledServiceSameQueueKeyMapsToSameClient(t *testing.T) {
	pool, calls := testPool(t, ClientPoolUnavailableFail, 3, nil)
	req := Request{RequestID: "req-1", AffinityKey: "body:session-a", AffinityKeyHash: "hash-a", AffinityKeyMode: "body:session_id"}

	for range 5 {
		if _, err := pool.Complete(context.Background(), req); err != nil {
			t.Fatalf("Complete() error = %v", err)
		}
	}

	called := calledLabels(calls)
	if len(called) != 1 {
		t.Fatalf("called labels = %v, want one stable client", called)
	}
}

func TestPooledServiceDifferentQueueKeysCanMapToDifferentClients(t *testing.T) {
	pool, calls := testPool(t, ClientPoolUnavailableFail, 4, nil)
	first := Request{AffinityKey: "body:first", AffinityKeyHash: "hash-first", AffinityKeyMode: "body:session_id"}

	second := Request{AffinityKey: "body:second", AffinityKeyHash: "hash-second", AffinityKeyMode: "body:session_id"}
	for pool.selectIndex(first) == pool.selectIndex(second) {
		second.AffinityKey += "-next"
	}

	if _, err := pool.Complete(context.Background(), first); err != nil {
		t.Fatalf("first Complete() error = %v", err)
	}
	if _, err := pool.Complete(context.Background(), second); err != nil {
		t.Fatalf("second Complete() error = %v", err)
	}

	called := calledLabels(calls)
	if len(called) != 2 {
		t.Fatalf("called labels = %v, want different clients", called)
	}
}

func TestPooledServiceUnavailableClientFailsGracefully(t *testing.T) {
	unavailable := errors.New("client unavailable")
	pool, _ := testPool(t, ClientPoolUnavailableFail, 2, map[string]error{"client-1": unavailable})
	req := Request{AffinityKey: "body:session-a", AffinityKeyHash: "hash-a", AffinityKeyMode: "body:session_id"}
	for pool.selectIndex(req) != 1 {
		req.AffinityKey += "-next"
	}

	_, err := pool.Complete(context.Background(), req)
	if !errors.Is(err, unavailable) {
		t.Fatalf("Complete() error = %v, want unavailable error", err)
	}
}

func TestPooledServiceUnavailableClientFallbackFirst(t *testing.T) {
	var logs bytes.Buffer
	unavailable := errors.New("client unavailable")
	pool, calls := testPool(t, ClientPoolUnavailableFallbackFirst, 2, map[string]error{"client-1": unavailable})
	pool.logOutput = &logs
	req := Request{RequestID: "req-fallback", AffinityKey: "body:session-a", AffinityKeyHash: "hash-a", AffinityKeyMode: "body:session_id"}
	for pool.selectIndex(req) != 1 {
		req.AffinityKey += "-next"
	}

	completion, err := pool.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if completion.Text != "client-0" {
		t.Fatalf("completion text = %q, want fallback client", completion.Text)
	}
	called := calledLabels(calls)
	if len(called) != 2 || !called["client-1"] || !called["client-0"] {
		t.Fatalf("called labels = %v, want selected and fallback clients", called)
	}
	logBody := logs.String()
	for _, want := range []string{"codex_client_select", "request_id=req-fallback", "key_mode=body:session_id", "key_hash=hash-a", "client_label=client-1", "client_label=client-0", "fallback=true"} {
		if !strings.Contains(logBody, want) {
			t.Fatalf("logs = %q, want %q", logBody, want)
		}
	}
	if strings.Contains(logBody, req.AffinityKey) {
		t.Fatalf("logs leaked raw affinity key: %q", logBody)
	}
}

func testPool(t *testing.T, policy string, count int, fail map[string]error) (*PooledService, map[string]int) {
	t.Helper()
	var mu sync.Mutex
	calls := map[string]int{}
	clients := make([]PooledClientConfig, 0, count)
	for i := range count {
		label := "client-" + string(rune('0'+i))
		service := poolFakeService{
			complete: func(ctx context.Context, req Request) (Completion, error) {
				mu.Lock()
				defer mu.Unlock()
				calls[label]++
				if err := fail[label]; err != nil {
					return Completion{}, err
				}
				return Completion{Text: label, Model: req.Model}, nil
			},
		}
		clients = append(clients, PooledClientConfig{Label: label, Service: service})
	}
	pool, err := NewPooledService(PooledServiceConfig{
		Clients:           clients,
		UnavailablePolicy: policy,
		LogOutput:         &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("NewPooledService() error = %v", err)
	}
	return pool, calls
}

func calledLabels(calls map[string]int) map[string]bool {
	labels := map[string]bool{}
	for label, count := range calls {
		if count > 0 {
			labels[label] = true
		}
	}
	return labels
}

type poolFakeService struct {
	complete func(context.Context, Request) (Completion, error)
}

func (f poolFakeService) Complete(ctx context.Context, req Request) (Completion, error) {
	return f.complete(ctx, req)
}

func (f poolFakeService) Stream(context.Context, Request) (<-chan StreamEvent, error) {
	return nil, errors.New("unexpected Stream call")
}
