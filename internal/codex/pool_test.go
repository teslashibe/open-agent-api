package codex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

func TestPooledServiceRotatesConnectQuotaWithoutChangingModel(t *testing.T) {
	var logs bytes.Buffer
	var mu sync.Mutex
	calls := map[string]int{}
	models := map[string][]string{}
	quota := poolQuotaError()
	pool := newTestPooledService(t, ClientPoolUnavailableFail, &logs, nil,
		PooledClientConfig{Label: "client-a", Service: poolFakeService{stream: func(_ context.Context, req Request) (<-chan StreamEvent, error) {
			mu.Lock()
			calls["client-a"]++
			models["client-a"] = append(models["client-a"], req.Model)
			mu.Unlock()
			return nil, quota
		}}},
		PooledClientConfig{Label: "client-b", Service: poolFakeService{stream: func(_ context.Context, req Request) (<-chan StreamEvent, error) {
			mu.Lock()
			calls["client-b"]++
			models["client-b"] = append(models["client-b"], req.Model)
			mu.Unlock()
			return poolEvents(StreamEvent{Delta: "ok", Model: req.Model}, StreamEvent{Done: true}), nil
		}}},
	)
	req := requestForPoolIndex(pool, 0)
	req.RequestID = "req-rotate"
	req.Model = "gpt-5.6-sol"

	completion, err := pool.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if completion.Text != "ok" || completion.Model != req.Model {
		t.Fatalf("completion = %#v", completion)
	}
	if calls["client-a"] != 1 || calls["client-b"] != 1 {
		t.Fatalf("calls = %v", calls)
	}
	if fmt.Sprint(models["client-a"]) != "[gpt-5.6-sol]" || fmt.Sprint(models["client-b"]) != "[gpt-5.6-sol]" {
		t.Fatalf("models = %v", models)
	}
	logBody := logs.String()
	for _, want := range []string{"codex_client_cooldown label=client-a", "client_label=client-b", "rotated=true"} {
		if !strings.Contains(logBody, want) {
			t.Fatalf("logs = %q, want %q", logBody, want)
		}
	}
	if strings.Contains(logBody, req.AffinityKey) {
		t.Fatalf("logs leaked raw affinity key: %q", logBody)
	}
}

func TestPooledServiceRotatesFirstEventQuota(t *testing.T) {
	var calls [2]int
	pool := newTestPooledService(t, ClientPoolUnavailableFail, &bytes.Buffer{}, nil,
		PooledClientConfig{Label: "client-a", Service: poolFakeService{stream: func(context.Context, Request) (<-chan StreamEvent, error) {
			calls[0]++
			return poolEvents(StreamEvent{Err: poolQuotaError()}), nil
		}}},
		PooledClientConfig{Label: "client-b", Service: poolFakeService{stream: func(context.Context, Request) (<-chan StreamEvent, error) {
			calls[1]++
			return poolEvents(StreamEvent{Delta: "from-b"}, StreamEvent{Done: true}), nil
		}}},
	)

	completion, err := pool.Complete(context.Background(), requestForPoolIndex(pool, 0))
	if err != nil || completion.Text != "from-b" {
		t.Fatalf("Complete() = %#v, %v", completion, err)
	}
	if calls != [2]int{1, 1} {
		t.Fatalf("calls = %v", calls)
	}
}

func TestPooledServiceCoolingStickyClientIsSkippedUntilExpiry(t *testing.T) {
	now := time.Date(2026, 7, 21, 20, 0, 0, 0, time.UTC)
	var calls [2]int
	pool := newTestPooledService(t, ClientPoolUnavailableFail, &bytes.Buffer{}, func() time.Time { return now },
		PooledClientConfig{Label: "client-a", Service: poolFakeService{stream: func(context.Context, Request) (<-chan StreamEvent, error) {
			calls[0]++
			if calls[0] == 1 {
				return poolEvents(StreamEvent{Err: poolQuotaError()}), nil
			}
			return poolEvents(StreamEvent{Delta: "from-a"}, StreamEvent{Done: true}), nil
		}}},
		PooledClientConfig{Label: "client-b", Service: poolFakeService{stream: func(context.Context, Request) (<-chan StreamEvent, error) {
			calls[1]++
			return poolEvents(StreamEvent{Delta: "from-b"}, StreamEvent{Done: true}), nil
		}}},
	)
	req := requestForPoolIndex(pool, 0)

	for i := 0; i < 2; i++ {
		completion, err := pool.Complete(context.Background(), req)
		if err != nil || completion.Text != "from-b" {
			t.Fatalf("Complete() %d = %#v, %v", i, completion, err)
		}
	}
	if calls != [2]int{1, 2} {
		t.Fatalf("calls while cooling = %v", calls)
	}

	now = now.Add(DefaultClientCooldown + time.Second)
	completion, err := pool.Complete(context.Background(), req)
	if err != nil || completion.Text != "from-a" {
		t.Fatalf("Complete() after expiry = %#v, %v", completion, err)
	}
	if calls != [2]int{2, 2} {
		t.Fatalf("calls after expiry = %v", calls)
	}
}

func TestPooledServiceDoesNotRotateAfterContentOrToolDelta(t *testing.T) {
	tests := map[string]StreamEvent{
		"content": {Delta: "partial"},
		"tool":    {ToolCallDelta: &ToolCallDelta{Index: 0, ID: "call-1", Type: "function", Function: ToolCallFunctionDelta{Name: "lookup"}}},
	}
	for name, first := range tests {
		t.Run(name, func(t *testing.T) {
			var calls [2]int
			pool := newTestPooledService(t, ClientPoolUnavailableFail, &bytes.Buffer{}, nil,
				PooledClientConfig{Label: "client-a", Service: poolFakeService{stream: func(context.Context, Request) (<-chan StreamEvent, error) {
					calls[0]++
					return poolEvents(first, StreamEvent{Err: poolQuotaError()}), nil
				}}},
				PooledClientConfig{Label: "client-b", Service: poolFakeService{stream: func(context.Context, Request) (<-chan StreamEvent, error) {
					calls[1]++
					return poolEvents(StreamEvent{Delta: "unexpected"}), nil
				}}},
			)

			_, err := pool.Complete(context.Background(), requestForPoolIndex(pool, 0))
			if !errors.Is(err, ErrUsageLimitReached) {
				t.Fatalf("Complete() error = %v", err)
			}
			if calls != [2]int{1, 0} {
				t.Fatalf("calls = %v, rotated after output", calls)
			}
		})
	}
}

func TestPooledServiceBoundsRotationToOneAlternate(t *testing.T) {
	var calls [3]int
	clients := make([]PooledClientConfig, 3)
	for i := range clients {
		index := i
		clients[i] = PooledClientConfig{Label: fmt.Sprintf("client-%d", i), Service: poolFakeService{stream: func(context.Context, Request) (<-chan StreamEvent, error) {
			calls[index]++
			if index < 2 {
				return nil, poolQuotaError()
			}
			return poolEvents(StreamEvent{Delta: "third"}), nil
		}}}
	}
	pool := newTestPooledService(t, ClientPoolUnavailableFail, &bytes.Buffer{}, nil, clients...)

	_, err := pool.Complete(context.Background(), requestForPoolIndex(pool, 0))
	if !errors.Is(err, ErrUsageLimitReached) {
		t.Fatalf("Complete() error = %v", err)
	}
	if calls != [3]int{1, 1, 0} {
		t.Fatalf("calls = %v, want one alternate only", calls)
	}
}

func TestPooledServiceSingleClientAndAllCoolingCompatibility(t *testing.T) {
	for _, policy := range []string{ClientPoolUnavailableFail, ClientPoolUnavailableFallbackFirst} {
		t.Run(policy, func(t *testing.T) {
			var calls int
			pool := newTestPooledService(t, policy, &bytes.Buffer{}, nil,
				PooledClientConfig{Label: "only", Service: poolFakeService{stream: func(context.Context, Request) (<-chan StreamEvent, error) {
					calls++
					return nil, poolQuotaError()
				}}},
			)

			_, firstErr := pool.Complete(context.Background(), Request{Model: "gpt-5.6-sol"})
			if !errors.Is(firstErr, ErrUsageLimitReached) || calls != 1 {
				t.Fatalf("first Complete() error = %v, calls = %d", firstErr, calls)
			}
			_, secondErr := pool.Complete(context.Background(), Request{Model: "gpt-5.6-sol"})
			if !errors.Is(secondErr, ErrUsageLimitReached) || calls != 1 {
				t.Fatalf("cooling Complete() error = %v, calls = %d", secondErr, calls)
			}

			_, fallbackErr := pool.Complete(context.Background(), Request{Model: "gpt-5.3-codex-spark", AllowCooling: true})
			if !errors.Is(fallbackErr, ErrUsageLimitReached) || calls != 2 {
				t.Fatalf("fallback Complete() error = %v, calls = %d", fallbackErr, calls)
			}
		})
	}
}

func TestPooledServiceAllCoolingPreservesStickyClientFailureClass(t *testing.T) {
	pool := newTestPooledService(t, ClientPoolUnavailableFail, &bytes.Buffer{}, nil,
		PooledClientConfig{Label: "rate-limited", Service: poolFakeService{stream: func(context.Context, Request) (<-chan StreamEvent, error) {
			t.Fatal("cooling rate-limited client must not receive an upstream call")
			return nil, nil
		}}},
		PooledClientConfig{Label: "quota-limited", Service: poolFakeService{stream: func(context.Context, Request) (<-chan StreamEvent, error) {
			t.Fatal("cooling quota-limited client must not receive an upstream call")
			return nil, nil
		}}},
	)
	rateLimitErr := NewError(ErrorKindUpstream, http.StatusTooManyRequests, "too many requests", errors.New("capacity"))
	pool.coolClient(0, rateLimitErr)
	pool.coolClient(1, poolQuotaError())

	_, err := pool.Complete(context.Background(), requestForPoolIndex(pool, 0))
	if errors.Is(err, ErrUsageLimitReached) || ClassifyFailure(err) != FailureRateLimit {
		t.Fatalf("rate-limit shard error = %v, class = %s", err, ClassifyFailure(err))
	}

	_, err = pool.Complete(context.Background(), requestForPoolIndex(pool, 1))
	if !errors.Is(err, ErrUsageLimitReached) || ClassifyFailure(err) != FailureQuota {
		t.Fatalf("quota shard error = %v, class = %s", err, ClassifyFailure(err))
	}
}

func TestPooledServiceHonorsRetryHint(t *testing.T) {
	now := time.Date(2026, 7, 21, 20, 0, 0, 0, time.UTC)
	pool := newTestPooledService(t, ClientPoolUnavailableFail, &bytes.Buffer{}, func() time.Time { return now },
		PooledClientConfig{Label: "only", Service: poolFakeService{stream: func(context.Context, Request) (<-chan StreamEvent, error) {
			return poolEvents(StreamEvent{Done: true}), nil
		}}},
	)
	want := now.Add(2 * time.Hour)
	err := NewError(ErrorKindUpstream, http.StatusTooManyRequests, "usage limit reached", ErrUsageLimitReached)
	withRetryHint(err, time.Minute, want)
	if got := pool.coolClient(0, err); !got.Equal(want) {
		t.Fatalf("cooldown until = %s, want %s", got, want)
	}
}

func TestPooledServiceRotatesRateLimit(t *testing.T) {
	var calls [2]int
	rateLimit := NewError(ErrorKindUpstream, http.StatusTooManyRequests, "too many requests", errors.New("capacity"))
	pool := newTestPooledService(t, ClientPoolUnavailableFail, &bytes.Buffer{}, nil,
		PooledClientConfig{Label: "client-a", Service: poolFakeService{stream: func(context.Context, Request) (<-chan StreamEvent, error) {
			calls[0]++
			return nil, rateLimit
		}}},
		PooledClientConfig{Label: "client-b", Service: poolFakeService{stream: func(context.Context, Request) (<-chan StreamEvent, error) {
			calls[1]++
			return poolEvents(StreamEvent{Delta: "ok"}), nil
		}}},
	)

	completion, err := pool.Complete(context.Background(), requestForPoolIndex(pool, 0))
	if err != nil || completion.Text != "ok" || calls != [2]int{1, 1} {
		t.Fatalf("Complete() = %#v, %v; calls = %v", completion, err, calls)
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

func newTestPooledService(t *testing.T, policy string, logs *bytes.Buffer, now func() time.Time, clients ...PooledClientConfig) *PooledService {
	t.Helper()
	pool, err := NewPooledService(PooledServiceConfig{
		Clients:           clients,
		UnavailablePolicy: policy,
		LogOutput:         logs,
		Now:               now,
	})
	if err != nil {
		t.Fatalf("NewPooledService() error = %v", err)
	}
	return pool
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

func requestForPoolIndex(pool *PooledService, want int) Request {
	req := Request{AffinityKey: "secret-affinity", AffinityKeyHash: "hash", AffinityKeyMode: "body:session_id"}
	for pool.selectIndex(req) != want {
		req.AffinityKey += "-next"
	}
	return req
}

func poolQuotaError() error {
	return NewError(ErrorKindUpstream, http.StatusTooManyRequests, "usage limit reached", fmt.Errorf("%w: quota", ErrUsageLimitReached))
}

func poolEvents(events ...StreamEvent) <-chan StreamEvent {
	out := make(chan StreamEvent, len(events))
	for _, event := range events {
		out <- event
	}
	close(out)
	return out
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
