package codex

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
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
	unavailable := ErrClientUnavailable
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
	unavailable := ErrClientUnavailable
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

func TestPooledServiceFallbackFirstRetriesPreStreamAuthError(t *testing.T) {
	authErr := NewError(ErrorKindAuth, 401, "load codex credentials", errors.New("missing auth"))
	pool, calls := testPool(t, ClientPoolUnavailableFallbackFirst, 2, map[string]error{"client-1": authErr})
	req := Request{AffinityKey: "body:session-a", AffinityKeyHash: "hash-a", AffinityKeyMode: "body:session_id"}
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
}

func TestPooledServiceFallbackFirstDoesNotRetryOrdinaryStartError(t *testing.T) {
	ordinary := errors.New("ordinary upstream error")
	pool, calls := testPool(t, ClientPoolUnavailableFallbackFirst, 2, map[string]error{"client-1": ordinary})
	req := Request{AffinityKey: "body:session-a", AffinityKeyHash: "hash-a", AffinityKeyMode: "body:session_id"}
	for pool.selectIndex(req) != 1 {
		req.AffinityKey += "-next"
	}

	_, err := pool.Complete(context.Background(), req)
	if !errors.Is(err, ordinary) {
		t.Fatalf("Complete() error = %v, want ordinary error", err)
	}
	called := calledLabels(calls)
	if len(called) != 1 || !called["client-1"] {
		t.Fatalf("called labels = %v, want only selected client", called)
	}
}

func TestPooledServiceFallbackFirstDoesNotRetryMidStreamError(t *testing.T) {
	midstream := errors.New("midstream upstream error")
	pool, calls := testPool(t, ClientPoolUnavailableFallbackFirst, 2, map[string]error{"client-1": midstream})
	req := Request{AffinityKey: "body:session-a", AffinityKeyHash: "hash-a", AffinityKeyMode: "body:session_id"}
	for pool.selectIndex(req) != 1 {
		req.AffinityKey += "-next"
	}

	_, err := pool.Complete(context.Background(), req)
	if !errors.Is(err, midstream) {
		t.Fatalf("Complete() error = %v, want midstream error", err)
	}
	called := calledLabels(calls)
	if len(called) != 1 || !called["client-1"] {
		t.Fatalf("called labels = %v, want only selected client", called)
	}
}

func TestPooledServiceRejectsSaturatedClientWithoutUpstreamCall(t *testing.T) {
	var logs bytes.Buffer
	upstream := make(chan StreamEvent)
	var mu sync.Mutex
	calls := 0
	pool := newLeaseTestPool(t, 1, &logs, PooledClientConfig{
		Label: "client-a",
		Service: poolFakeService{stream: func(context.Context, Request) (<-chan StreamEvent, error) {
			mu.Lock()
			calls++
			mu.Unlock()
			return upstream, nil
		}},
	})

	events, err := pool.Stream(context.Background(), Request{RequestID: "req-active"})
	if err != nil {
		t.Fatalf("first Stream() error = %v", err)
	}
	_, err = pool.Stream(context.Background(), Request{RequestID: "req-saturated"})
	if !errors.Is(err, ErrClientPoolSaturated) {
		t.Fatalf("second Stream() error = %v, want ErrClientPoolSaturated", err)
	}
	serviceErr, ok := ErrorAs(err)
	if !ok || serviceErr.Status != http.StatusTooManyRequests || serviceErr.Message != "codex client pool saturated" {
		t.Fatalf("second Stream() error = %#v, want stable 429", serviceErr)
	}
	mu.Lock()
	gotCalls := calls
	mu.Unlock()
	if gotCalls != 1 {
		t.Fatalf("upstream calls = %d, want 1", gotCalls)
	}

	close(upstream)
	drainEvents(events)
	waitPoolInflight(t, pool, "client-a", 0)
	for _, want := range []string{"client_label=client-a inflight=1", "codex_client_saturated", "codex_client_pool_saturated", "codex_client_release"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("logs = %q, want %q", logs.String(), want)
		}
	}
}

func TestPooledServiceRotatesFromSaturatedClient(t *testing.T) {
	var logs bytes.Buffer
	channels := map[string]chan StreamEvent{
		"client-a": make(chan StreamEvent),
		"client-b": make(chan StreamEvent),
	}
	var mu sync.Mutex
	calls := map[string]int{}
	clients := make([]PooledClientConfig, 0, 2)
	for _, label := range []string{"client-a", "client-b"} {
		label := label
		clients = append(clients, PooledClientConfig{
			Label: label,
			Service: poolFakeService{stream: func(context.Context, Request) (<-chan StreamEvent, error) {
				mu.Lock()
				calls[label]++
				mu.Unlock()
				return channels[label], nil
			}},
		})
	}
	pool := newLeaseTestPool(t, 1, &logs, clients...)
	req := Request{RequestID: "req-rotate", AffinityKey: "sticky-a"}
	for pool.selectIndex(req) != 0 {
		req.AffinityKey += "-next"
	}

	first, err := pool.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("first Stream() error = %v", err)
	}
	second, err := pool.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("second Stream() error = %v", err)
	}
	mu.Lock()
	gotA, gotB := calls["client-a"], calls["client-b"]
	mu.Unlock()
	if gotA != 1 || gotB != 1 {
		t.Fatalf("upstream calls = %v, want one per client", calls)
	}
	if !strings.Contains(logs.String(), "client_label=client-b inflight=1 fallback=false rotated=true") {
		t.Fatalf("logs = %q, want rotated selection", logs.String())
	}

	close(channels["client-a"])
	close(channels["client-b"])
	drainEvents(first)
	drainEvents(second)
	waitPoolInflight(t, pool, "client-a", 0)
	waitPoolInflight(t, pool, "client-b", 0)
}

func TestPooledServiceReleasesLeaseOnStartupError(t *testing.T) {
	startErr := errors.New("start failed")
	calls := 0
	pool := newLeaseTestPool(t, 1, nil, PooledClientConfig{
		Label: "client-a",
		Service: poolFakeService{stream: func(context.Context, Request) (<-chan StreamEvent, error) {
			calls++
			return nil, startErr
		}},
	})

	for range 2 {
		if _, err := pool.Stream(context.Background(), Request{}); !errors.Is(err, startErr) {
			t.Fatalf("Stream() error = %v, want startup error", err)
		}
	}
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls)
	}
	waitPoolInflight(t, pool, "client-a", 0)
}

func TestPooledServiceReleasesLeaseOnMidstreamError(t *testing.T) {
	midstreamErr := errors.New("midstream failed")
	calls := 0
	pool := newLeaseTestPool(t, 1, nil, PooledClientConfig{
		Label: "client-a",
		Service: poolFakeService{stream: func(context.Context, Request) (<-chan StreamEvent, error) {
			calls++
			events := make(chan StreamEvent, 1)
			events <- StreamEvent{Err: midstreamErr}
			return events, nil
		}},
	})

	for range 2 {
		_, err := pool.Complete(context.Background(), Request{})
		if !errors.Is(err, midstreamErr) {
			t.Fatalf("Complete() error = %v, want midstream error", err)
		}
	}
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls)
	}
	waitPoolInflight(t, pool, "client-a", 0)
}

func TestPooledServiceReleasesLeaseOnContextCancellation(t *testing.T) {
	var mu sync.Mutex
	channels := []chan StreamEvent{}
	pool := newLeaseTestPool(t, 1, nil, PooledClientConfig{
		Label: "client-a",
		Service: poolFakeService{stream: func(context.Context, Request) (<-chan StreamEvent, error) {
			mu.Lock()
			defer mu.Unlock()
			events := make(chan StreamEvent)
			channels = append(channels, events)
			return events, nil
		}},
	})

	ctx, cancel := context.WithCancel(context.Background())
	events, err := pool.Stream(ctx, Request{})
	if err != nil {
		t.Fatalf("first Stream() error = %v", err)
	}
	cancel()
	var cancelErr error
	for event := range events {
		cancelErr = event.Err
	}
	if !errors.Is(cancelErr, context.Canceled) {
		t.Fatalf("cancellation event error = %v, want context.Canceled", cancelErr)
	}
	waitPoolInflight(t, pool, "client-a", 0)

	second, err := pool.Stream(context.Background(), Request{})
	if err != nil {
		t.Fatalf("second Stream() error = %v", err)
	}
	mu.Lock()
	secondUpstream := channels[1]
	mu.Unlock()
	close(secondUpstream)
	drainEvents(second)
	waitPoolInflight(t, pool, "client-a", 0)
}

func newLeaseTestPool(t *testing.T, maxInflight int, logs *bytes.Buffer, clients ...PooledClientConfig) *PooledService {
	t.Helper()
	var output io.Writer
	if logs != nil {
		output = logs
	} else {
		output = io.Discard
	}
	pool, err := NewPooledService(PooledServiceConfig{
		Clients:           clients,
		MaxInflight:       maxInflight,
		UnavailablePolicy: ClientPoolUnavailableFail,
		LogOutput:         output,
	})
	if err != nil {
		t.Fatalf("NewPooledService() error = %v", err)
	}
	return pool
}

func drainEvents(events <-chan StreamEvent) {
	for range events {
	}
}

func waitPoolInflight(t *testing.T, pool *PooledService, label string, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		pool.mu.Lock()
		got := pool.inflight[label]
		pool.mu.Unlock()
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("inflight[%q] = %d, want %d", label, got, want)
		}
		time.Sleep(time.Millisecond)
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
			stream: func(ctx context.Context, req Request) (<-chan StreamEvent, error) {
				mu.Lock()
				calls[label]++
				mu.Unlock()
				if err := fail[label]; err != nil {
					if errors.Is(err, ErrClientUnavailable) || strings.Contains(err.Error(), "ordinary") {
						return nil, err
					}
					if codexErr, ok := ErrorAs(err); ok && codexErr.Kind == ErrorKindAuth {
						return nil, err
					}
					events := make(chan StreamEvent, 1)
					events <- StreamEvent{Err: err}
					close(events)
					return events, nil
				}
				events := make(chan StreamEvent, 2)
				events <- StreamEvent{Delta: label, Model: req.Model}
				events <- StreamEvent{Done: true}
				close(events)
				return events, nil
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
	stream func(context.Context, Request) (<-chan StreamEvent, error)
}

func (f poolFakeService) Complete(ctx context.Context, req Request) (Completion, error) {
	events, err := f.Stream(ctx, req)
	if err != nil {
		return Completion{}, err
	}
	var completion Completion
	for event := range events {
		if event.Err != nil {
			return Completion{}, event.Err
		}
		completion.Text += event.Delta
	}
	return completion, nil
}

func (f poolFakeService) Stream(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	return f.stream(ctx, req)
}
