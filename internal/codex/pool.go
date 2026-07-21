package codex

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
)

const (
	ClientPoolUnavailableFail          = "fail"
	ClientPoolUnavailableFallbackFirst = "fallback_first"
	defaultClientMaxInflight           = 2
)

// ErrClientPoolSaturated marks requests rejected before an upstream call
// because every Codex client is already at its per-account inflight cap.
var ErrClientPoolSaturated = errors.New("codex client pool saturated")

type PooledService struct {
	clients           []pooledClient
	maxInflight       int
	unavailablePolicy string
	logOutput         io.Writer

	mu       sync.Mutex
	inflight map[string]int
	logMu    sync.Mutex
}

type pooledClient struct {
	label   string
	service Service
}

type PooledServiceConfig struct {
	Clients           []PooledClientConfig
	MaxInflight       int
	UnavailablePolicy string
	LogOutput         io.Writer
}

type PooledClientConfig struct {
	Label   string
	Service Service
}

func NewPooledService(cfg PooledServiceConfig) (*PooledService, error) {
	if len(cfg.Clients) == 0 {
		return nil, fmt.Errorf("at least one codex client is required")
	}
	clients := make([]pooledClient, 0, len(cfg.Clients))
	labels := map[string]bool{}
	for i, client := range cfg.Clients {
		if client.Service == nil {
			return nil, fmt.Errorf("codex client %d service is required", i)
		}
		label := client.Label
		if label == "" {
			label = fmt.Sprintf("client-%d", i)
		}
		if labels[label] {
			return nil, fmt.Errorf("duplicate codex client label %q", label)
		}
		labels[label] = true
		clients = append(clients, pooledClient{
			label:   label,
			service: client.Service,
		})
	}
	if cfg.MaxInflight == 0 {
		cfg.MaxInflight = defaultClientMaxInflight
	}
	if cfg.MaxInflight < 1 {
		return nil, fmt.Errorf("codex client max inflight must be at least 1")
	}
	if cfg.UnavailablePolicy == "" {
		cfg.UnavailablePolicy = ClientPoolUnavailableFail
	}
	switch cfg.UnavailablePolicy {
	case ClientPoolUnavailableFail, ClientPoolUnavailableFallbackFirst:
	default:
		return nil, fmt.Errorf("unsupported codex client pool unavailable policy %q", cfg.UnavailablePolicy)
	}
	if cfg.LogOutput == nil {
		cfg.LogOutput = os.Stdout
	}
	return &PooledService{
		clients:           clients,
		maxInflight:       cfg.MaxInflight,
		unavailablePolicy: cfg.UnavailablePolicy,
		logOutput:         cfg.LogOutput,
		inflight:          map[string]int{},
	}, nil
}

func (p *PooledService) Complete(ctx context.Context, req Request) (Completion, error) {
	events, err := p.Stream(ctx, req)
	if err != nil {
		return Completion{}, err
	}

	var completion Completion
	for event := range events {
		if event.Err != nil {
			return Completion{}, event.Err
		}
		if event.Delta != "" {
			completion.Text += event.Delta
		}
		if len(event.ToolCalls) > 0 {
			completion.ToolCalls = append(completion.ToolCalls, event.ToolCalls...)
		}
		if event.ToolCallDelta != nil {
			applyToolCallDelta(&completion.ToolCalls, *event.ToolCallDelta)
		}
		if event.ID != "" {
			completion.ID = event.ID
		}
		if event.Model != "" {
			completion.Model = event.Model
		}
		if event.Usage.TotalTokens != 0 || event.Usage.PromptTokens != 0 || event.Usage.CompletionTokens != 0 {
			completion.Usage = event.Usage
		}
	}
	completion.ToolCalls = compactToolCalls(completion.ToolCalls)
	return completion, nil
}

func (p *PooledService) Stream(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	selected := p.selectIndex(req)
	index, inflight, release, err := p.acquireAvailable(req, selected)
	if err != nil {
		return nil, err
	}
	p.logSelection(req, index, false, index != selected, inflight)
	events, err := p.clients[index].service.Stream(ctx, req)
	if err == nil {
		return p.withLease(ctx, events, release), nil
	}
	release()
	if !p.shouldFallback(index, err) {
		return nil, err
	}

	inflight, release, ok := p.acquireClient(req, 0)
	if !ok {
		return nil, p.saturatedError(req)
	}
	p.logSelection(req, 0, true, false, inflight)
	events, err = p.clients[0].service.Stream(ctx, req)
	if err != nil {
		release()
		return nil, err
	}
	return p.withLease(ctx, events, release), nil
}

func (p *PooledService) acquireAvailable(req Request, selected int) (int, int, func(), error) {
	for offset := range len(p.clients) {
		index := (selected + offset) % len(p.clients)
		inflight, release, ok := p.acquireClient(req, index)
		if ok {
			return index, inflight, release, nil
		}
	}
	return 0, 0, nil, p.saturatedError(req)
}

func (p *PooledService) acquireClient(req Request, index int) (int, func(), bool) {
	label := p.clients[index].label
	p.mu.Lock()
	current := p.inflight[label]
	if current >= p.maxInflight {
		p.mu.Unlock()
		p.logClientSaturated(req, index, current)
		return current, nil, false
	}
	current++
	p.inflight[label] = current
	p.mu.Unlock()

	var once sync.Once
	release := func() {
		once.Do(func() {
			p.mu.Lock()
			if p.inflight[label] > 0 {
				p.inflight[label]--
			}
			remaining := p.inflight[label]
			p.mu.Unlock()
			p.logRelease(req, index, remaining)
		})
	}
	return current, release, true
}

func (p *PooledService) withLease(ctx context.Context, events <-chan StreamEvent, release func()) <-chan StreamEvent {
	out := make(chan StreamEvent, 1)
	go func() {
		defer close(out)
		defer release()
		for {
			select {
			case <-ctx.Done():
				trySendContextError(out, ctx.Err())
				return
			case event, ok := <-events:
				if !ok {
					return
				}
				select {
				case <-ctx.Done():
					trySendContextError(out, ctx.Err())
					return
				case out <- event:
				}
				if event.Err != nil || event.Done {
					return
				}
			}
		}
	}()
	return out
}

func trySendContextError(events chan<- StreamEvent, err error) {
	select {
	case events <- StreamEvent{Err: err}:
	default:
	}
}

func (p *PooledService) saturatedError(req Request) error {
	p.logf(
		"codex_client_pool_saturated request_id=%s max_inflight=%d clients=%d\n",
		requestID(req),
		p.maxInflight,
		len(p.clients),
	)
	return NewError(
		ErrorKindUpstream,
		http.StatusTooManyRequests,
		ErrClientPoolSaturated.Error(),
		ErrClientPoolSaturated,
	)
}

func (p *PooledService) shouldFallback(index int, err error) bool {
	if p.unavailablePolicy != ClientPoolUnavailableFallbackFirst || index == 0 {
		return false
	}
	if errors.Is(err, ErrClientUnavailable) {
		return true
	}
	if codexErr, ok := ErrorAs(err); ok {
		return codexErr.Kind == ErrorKindAuth || codexErr.Kind == ErrorKindUpstream
	}
	return false
}

func (p *PooledService) selectIndex(req Request) int {
	if len(p.clients) == 1 {
		return 0
	}
	key := req.AffinityKey
	if key == "" {
		key = req.AffinityKeyHash
	}
	if key == "" {
		key = "global"
	}
	sum := sha256.Sum256([]byte(key))
	value := binary.BigEndian.Uint64(sum[:8])
	return int(value % uint64(len(p.clients)))
}

func (p *PooledService) logSelection(req Request, index int, fallback bool, rotated bool, inflight int) {
	keyMode := req.AffinityKeyMode
	if keyMode == "" {
		keyMode = "none"
	}
	keyHash := req.AffinityKeyHash
	if keyHash == "" {
		keyHash = "none"
	}
	p.logf(
		"codex_client_select request_id=%s key_mode=%s key_hash=%s shard=%d client_label=%s inflight=%d fallback=%t rotated=%t\n",
		requestID(req),
		keyMode,
		keyHash,
		index,
		p.clients[index].label,
		inflight,
		fallback,
		rotated,
	)
}

func (p *PooledService) logClientSaturated(req Request, index int, inflight int) {
	p.logf(
		"codex_client_saturated request_id=%s shard=%d client_label=%s inflight=%d max_inflight=%d\n",
		requestID(req),
		index,
		p.clients[index].label,
		inflight,
		p.maxInflight,
	)
}

func (p *PooledService) logRelease(req Request, index int, inflight int) {
	p.logf(
		"codex_client_release request_id=%s shard=%d client_label=%s inflight=%d\n",
		requestID(req),
		index,
		p.clients[index].label,
		inflight,
	)
}

func (p *PooledService) logf(format string, args ...any) {
	if p.logOutput == nil {
		return
	}
	p.logMu.Lock()
	defer p.logMu.Unlock()
	_, _ = fmt.Fprintf(p.logOutput, format, args...)
}

func requestID(req Request) string {
	if req.RequestID == "" {
		return "none"
	}
	return req.RequestID
}
