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
	"time"

	metricspkg "github.com/teslashibe/codex-chat-api/internal/metrics"
)

const (
	ClientPoolUnavailableFail          = "fail"
	ClientPoolUnavailableFallbackFirst = "fallback_first"
	DefaultClientCooldown              = 5 * time.Minute
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
	cooldownDefault   time.Duration
	now               func() time.Time
	metrics           *metricspkg.Metrics

	mu        sync.Mutex
	cooldowns []clientCooldown
	inflight  map[string]int
	logMu     sync.Mutex
}

type clientCooldown struct {
	until time.Time
	class FailureClass
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
	CooldownDefault   time.Duration
	Now               func() time.Time
	Metrics           *metricspkg.Metrics
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
	if cfg.CooldownDefault <= 0 {
		cfg.CooldownDefault = DefaultClientCooldown
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &PooledService{
		clients:           clients,
		maxInflight:       cfg.MaxInflight,
		unavailablePolicy: cfg.UnavailablePolicy,
		logOutput:         cfg.LogOutput,
		cooldownDefault:   cfg.CooldownDefault,
		now:               cfg.Now,
		metrics:           cfg.Metrics,
		cooldowns:         make([]clientCooldown, len(clients)),
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
	return p.streamAttempt(ctx, req, index, false, release)
}

func (p *PooledService) streamAttempt(ctx context.Context, req Request, index int, retried bool, release func()) (<-chan StreamEvent, error) {
	attemptCtx, cancel := context.WithCancel(ctx)
	events, err := p.clients[index].service.Stream(attemptCtx, req)
	if err != nil {
		cancel()
		if p.cooldownEligible(err, PhaseConnect) {
			p.coolClient(index, err)
			release()
			if !retried {
				if alternate, inflight, altRelease, ok := p.acquireAlternate(req, index); ok {
					p.logSelection(req, alternate, false, true, inflight)
					return p.streamAttempt(ctx, req, alternate, true, altRelease)
				}
			}
			return nil, err
		}
		release()
		if !retried && p.shouldFallback(index, err) {
			if inflight, fbRelease, ok, _, _ := p.tryAcquireClient(req, 0, false); ok {
				p.logSelection(req, 0, true, false, inflight)
				return p.streamAttempt(ctx, req, 0, true, fbRelease)
			}
		}
		return nil, err
	}

	out := make(chan StreamEvent, 1)
	go p.forwardAttempt(ctx, cancel, req, index, events, out, retried, release)
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
	release func(),
) {
	defer close(out)
	defer cancel()

	first, ok := receivePoolEvent(ctx, events)
	if !ok {
		release()
		if err := ctx.Err(); err != nil {
			trySendContextError(out, err)
		}
		return
	}
	if first.Err != nil && p.cooldownEligible(first.Err, PhaseFirstEvent) {
		p.coolClient(index, first.Err)
		if !retried {
			if alternate, inflight, altRelease, available := p.acquireAlternate(req, index); available {
				cancel()
				release()
				p.logSelection(req, alternate, false, true, inflight)
				retryEvents, err := p.streamAttempt(ctx, req, alternate, true, altRelease)
				if err != nil {
					p.sendPoolEvent(ctx, out, StreamEvent{Err: err})
					return
				}
				p.forwardRemaining(ctx, out, retryEvents)
				return
			}
		}
	}

	defer release()
	if !p.sendPoolEvent(ctx, out, first) {
		return
	}
	if first.Err != nil || first.Done {
		return
	}
	p.forwardRemaining(ctx, out, events)
}

func (p *PooledService) forwardRemaining(ctx context.Context, out chan<- StreamEvent, events <-chan StreamEvent) {
	for {
		select {
		case <-ctx.Done():
			trySendContextError(out, ctx.Err())
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			if !p.sendPoolEvent(ctx, out, event) {
				return
			}
			if event.Err != nil || event.Done {
				return
			}
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

func trySendContextError(events chan<- StreamEvent, err error) {
	select {
	case events <- StreamEvent{Err: err}:
	default:
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
	class := ClassifyFailure(err)

	p.mu.Lock()
	if p.cooldowns[index].until.After(until) {
		until = p.cooldowns[index].until
	} else {
		p.cooldowns[index] = clientCooldown{until: until, class: class}
	}
	p.mu.Unlock()
	p.metrics.ObservePoolCooldown(p.clients[index].label, string(class))
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

// acquireAvailable leases the sticky client when it is healthy (not cooling and
// under the inflight cap). Otherwise it walks other clients in shard order.
// Prefer cooldown errors when every client is cooling; otherwise saturation.
func (p *PooledService) acquireAvailable(req Request, selected int) (int, int, func(), error) {
	sawEligible := false
	var selectedCooldownClass FailureClass
	for offset := range len(p.clients) {
		index := (selected + offset) % len(p.clients)
		inflight, release, acquired, class, cooling := p.tryAcquireClient(req, index, false)
		if cooling {
			if index == selected {
				selectedCooldownClass = class
			}
			continue
		}
		sawEligible = true
		if acquired {
			return index, inflight, release, nil
		}
	}
	if !sawEligible {
		if req.AllowCooling {
			inflight, release, acquired, _, _ := p.tryAcquireClient(req, selected, true)
			if acquired {
				return selected, inflight, release, nil
			}
			return 0, 0, nil, p.saturatedError(req)
		}
		return 0, 0, nil, allClientsCoolingError(selectedCooldownClass)
	}
	return 0, 0, nil, p.saturatedError(req)
}

func (p *PooledService) acquireAlternate(req Request, failed int) (int, int, func(), bool) {
	for offset := 1; offset < len(p.clients); offset++ {
		index := (failed + offset) % len(p.clients)
		inflight, release, acquired, _, _ := p.tryAcquireClient(req, index, false)
		if acquired {
			return index, inflight, release, true
		}
	}
	return 0, 0, nil, false
}

// tryAcquireClient checks cooldown eligibility and increments the inflight
// lease in one critical section. This prevents a concurrent failure from
// cooling a client between selection and lease acquisition.
func (p *PooledService) tryAcquireClient(req Request, index int, allowCooling bool) (int, func(), bool, FailureClass, bool) {
	label := p.clients[index].label
	now := p.now()
	p.mu.Lock()
	cooldown := p.cooldowns[index]
	if !cooldown.until.After(now) {
		p.cooldowns[index] = clientCooldown{}
	} else if !allowCooling {
		p.mu.Unlock()
		p.metrics.ObservePoolCooldownSkip(label, string(cooldown.class))
		return 0, nil, false, cooldown.class, true
	}
	current := p.inflight[label]
	if current >= p.maxInflight {
		p.mu.Unlock()
		p.logClientSaturated(req, index, current)
		return current, nil, false, "", false
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
	return current, release, true, "", false
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
	result := "normal"
	if fallback {
		result = "fallback"
	} else if rotated {
		result = "rotated"
	}
	p.metrics.ObservePoolSelection(p.clients[index].label, result)
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

func (p *PooledService) logCooldown(index int, until time.Time) {
	p.logf(
		"codex_client_cooldown label=%s until=%s\n",
		p.clients[index].label,
		until.UTC().Format(time.RFC3339),
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

func allClientsCoolingError(class FailureClass) error {
	// Preserve the sticky client's cooldown class. In particular, a capacity
	// 429 must not acquire the quota sentinel and trigger model overflow.
	if class == FailureQuota {
		return NewError(
			ErrorKindUpstream,
			http.StatusTooManyRequests,
			"usage limit reached",
			fmt.Errorf("%w: all codex clients are cooling", ErrUsageLimitReached),
		)
	}
	return NewError(
		ErrorKindUpstream,
		http.StatusTooManyRequests,
		"rate limit reached",
		errors.New("all codex clients are cooling"),
	)
}
