package codex

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

const (
	ClientPoolUnavailableFail          = "fail"
	ClientPoolUnavailableFallbackFirst = "fallback_first"
	DefaultClientCooldown              = 5 * time.Minute
)

type PooledService struct {
	clients           []pooledClient
	unavailablePolicy string
	logOutput         io.Writer
	cooldownDefault   time.Duration
	now               func() time.Time

	mu            sync.Mutex
	cooldownUntil []time.Time
	logMu         sync.Mutex
}

type pooledClient struct {
	label   string
	service Service
}

type PooledServiceConfig struct {
	Clients           []PooledClientConfig
	UnavailablePolicy string
	LogOutput         io.Writer
	CooldownDefault   time.Duration
	Now               func() time.Time
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
	for i, client := range cfg.Clients {
		if client.Service == nil {
			return nil, fmt.Errorf("codex client %d service is required", i)
		}
		label := client.Label
		if label == "" {
			label = fmt.Sprintf("client-%d", i)
		}
		clients = append(clients, pooledClient{
			label:   label,
			service: client.Service,
		})
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
	if cfg.CooldownDefault <= 0 {
		cfg.CooldownDefault = DefaultClientCooldown
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &PooledService{
		clients:           clients,
		unavailablePolicy: cfg.UnavailablePolicy,
		logOutput:         cfg.LogOutput,
		cooldownDefault:   cfg.CooldownDefault,
		now:               cfg.Now,
		cooldownUntil:     make([]time.Time, len(clients)),
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
	index, fallback, ok := p.selectAvailable(req)
	if !ok {
		return nil, allClientsCoolingError()
	}
	p.logSelection(req, index, fallback)
	return p.streamAttempt(ctx, req, index, false)
}

func (p *PooledService) streamAttempt(ctx context.Context, req Request, index int, retried bool) (<-chan StreamEvent, error) {
	attemptCtx, cancel := context.WithCancel(ctx)
	events, err := p.clients[index].service.Stream(attemptCtx, req)
	if err != nil {
		cancel()
		if p.cooldownEligible(err, PhaseConnect) {
			p.coolClient(index, err)
			if !retried {
				if alternate, ok := p.selectAlternate(index); ok {
					p.logSelection(req, alternate, "rotate")
					return p.streamAttempt(ctx, req, alternate, true)
				}
			}
			return nil, err
		}
		if !retried && p.shouldFallback(index, err) && p.clientAvailable(0) {
			p.logSelection(req, 0, "fallback_first")
			return p.streamAttempt(ctx, req, 0, true)
		}
		return nil, err
	}

	out := make(chan StreamEvent, 1)
	go p.forwardAttempt(ctx, cancel, req, index, events, out, retried)
	return out, nil
}

func (p *PooledService) forwardAttempt(
	ctx context.Context,
	cancel context.CancelFunc,
	req Request,
	index int,
	events <-chan StreamEvent,
	out chan<- StreamEvent,
	retried bool,
) {
	defer close(out)
	defer cancel()

	first, ok := receivePoolEvent(ctx, events)
	if !ok {
		return
	}
	if first.Err != nil && p.cooldownEligible(first.Err, PhaseFirstEvent) {
		p.coolClient(index, first.Err)
		if !retried {
			if alternate, available := p.selectAlternate(index); available {
				cancel()
				p.logSelection(req, alternate, "rotate")
				retryEvents, err := p.streamAttempt(ctx, req, alternate, true)
				if err != nil {
					p.sendPoolEvent(ctx, out, StreamEvent{Err: err})
					return
				}
				p.copyPoolEvents(ctx, out, retryEvents)
				return
			}
		}
	}
	if !p.sendPoolEvent(ctx, out, first) {
		return
	}
	p.copyPoolEvents(ctx, out, events)
}

func (p *PooledService) copyPoolEvents(ctx context.Context, out chan<- StreamEvent, events <-chan StreamEvent) {
	for event := range events {
		if !p.sendPoolEvent(ctx, out, event) {
			return
		}
	}
}

func (p *PooledService) sendPoolEvent(ctx context.Context, out chan<- StreamEvent, event StreamEvent) bool {
	select {
	case out <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func receivePoolEvent(ctx context.Context, events <-chan StreamEvent) (StreamEvent, bool) {
	select {
	case event, ok := <-events:
		return event, ok
	case <-ctx.Done():
		return StreamEvent{}, false
	}
}

func (p *PooledService) cooldownEligible(err error, phase FailurePhase) bool {
	class := ClassifyFailure(err)
	if class != FailureRateLimit && class != FailureQuota {
		return false
	}
	return MayRotateAccount(class, phase)
}

func (p *PooledService) coolClient(index int, err error) time.Time {
	now := p.now()
	until, ok := retryDeadline(err, now)
	if !ok {
		until = now.Add(p.cooldownDefault)
	}

	p.mu.Lock()
	if p.cooldownUntil[index].After(until) {
		until = p.cooldownUntil[index]
	} else {
		p.cooldownUntil[index] = until
	}
	p.mu.Unlock()
	p.logCooldown(index, until)
	return until
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

func (p *PooledService) selectAvailable(req Request) (int, string, bool) {
	base := p.selectIndex(req)
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.cooldownUntil[base].After(now) {
		return base, "none", true
	}
	for offset := 1; offset < len(p.clients); offset++ {
		index := (base + offset) % len(p.clients)
		if !p.cooldownUntil[index].After(now) {
			return index, "cooldown", true
		}
	}
	if req.AllowCooling {
		return base, "quota_fallback", true
	}
	return 0, "", false
}

func (p *PooledService) selectAlternate(failed int) (int, bool) {
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	for offset := 1; offset < len(p.clients); offset++ {
		index := (failed + offset) % len(p.clients)
		if !p.cooldownUntil[index].After(now) {
			return index, true
		}
	}
	return 0, false
}

func (p *PooledService) clientAvailable(index int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.cooldownUntil[index].After(p.now())
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

func (p *PooledService) logSelection(req Request, index int, fallback string) {
	if p.logOutput == nil {
		return
	}
	keyMode := req.AffinityKeyMode
	if keyMode == "" {
		keyMode = "none"
	}
	keyHash := req.AffinityKeyHash
	if keyHash == "" {
		keyHash = "none"
	}
	requestID := req.RequestID
	if requestID == "" {
		requestID = "none"
	}
	p.logMu.Lock()
	defer p.logMu.Unlock()
	fmt.Fprintf(
		p.logOutput,
		"codex_client_select request_id=%s key_mode=%s key_hash=%s shard=%d client_label=%s fallback=%s\n",
		requestID,
		keyMode,
		keyHash,
		index,
		p.clients[index].label,
		fallback,
	)
}

func (p *PooledService) logCooldown(index int, until time.Time) {
	if p.logOutput == nil {
		return
	}
	p.logMu.Lock()
	defer p.logMu.Unlock()
	fmt.Fprintf(
		p.logOutput,
		"codex_client_cooldown label=%s until=%s\n",
		p.clients[index].label,
		until.UTC().Format(time.RFC3339),
	)
}

func allClientsCoolingError() error {
	return NewError(
		ErrorKindUpstream,
		429,
		"usage limit reached",
		fmt.Errorf("%w: all codex clients are cooling", ErrUsageLimitReached),
	)
}
