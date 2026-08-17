package codex

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	metricspkg "github.com/teslashibe/open-agent-api/internal/metrics"
)

const (
	ClientPoolUnavailableFail          = "fail"
	ClientPoolUnavailableFallbackFirst = "fallback_first"
	DefaultClientCooldown              = 5 * time.Minute
	// DefaultClientCooldownMax caps how long a usage_limit / rate-limit
	// reset hint can pin an account. Codex sometimes returns a far-future
	// resets_at (weekly window); trusting that blindly leaves a single-client
	// pool refusing all traffic for hours even after quota has recovered.
	// Cap the cooldown and re-probe periodically instead.
	DefaultClientCooldownMax = 15 * time.Minute
	defaultClientMaxInflight = 2
	defaultSoftPinCapacity   = 10_000
	defaultSoftPinTTL        = 24 * time.Hour
	initialLoadBalanceGap    = 2
	unpinReasonAuth          = "auth"
	unpinReasonCooldown      = "cooldown"
	unpinReasonTransport     = "transport"
	unpinReasonUnavailable   = "unavailable"
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
	cooldownMax       time.Duration
	now               func() time.Time
	metrics           *metricspkg.Metrics

	mu              sync.Mutex
	cooldowns       []clientCooldown
	inflight        map[string]int
	softPins        map[string]*list.Element
	softPinLRU      *list.List
	tentativePins   map[string]*tentativePin
	softPinCapacity int
	softPinTTL      time.Duration
	logMu           sync.Mutex
}

type clientCooldown struct {
	until time.Time
	class FailureClass
}

type softPin struct {
	key       string
	index     int
	expiresAt time.Time
}

type tentativePin struct {
	key   string
	index int
	refs  int
}

type pendingUnpin struct {
	key    string
	from   int
	reason string
}

type acquireCooldownSkip struct {
	index int
	class FailureClass
}

type acquireSaturatedClient struct {
	index    int
	inflight int
}

type poolAcquisition struct {
	index     int
	preferred int
	inflight  int
	release   func()
	unpin     *pendingUnpin
	tentative *tentativePin
	pinned    bool
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
	// CooldownMax caps retry/reset hints. Zero means DefaultClientCooldownMax.
	// Negative disables the cap (honor upstream resets_at fully).
	CooldownMax time.Duration
	Now         func() time.Time
	Metrics     *metricspkg.Metrics
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
	if cfg.CooldownMax == 0 {
		cfg.CooldownMax = DefaultClientCooldownMax
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Metrics == nil {
		cfg.Metrics = metricspkg.New(false)
	}
	return &PooledService{
		clients:           clients,
		maxInflight:       cfg.MaxInflight,
		unavailablePolicy: cfg.UnavailablePolicy,
		logOutput:         cfg.LogOutput,
		cooldownDefault:   cfg.CooldownDefault,
		cooldownMax:       cfg.CooldownMax,
		now:               cfg.Now,
		metrics:           cfg.Metrics,
		cooldowns:         make([]clientCooldown, len(clients)),
		inflight:          map[string]int{},
		softPins:          map[string]*list.Element{},
		softPinLRU:        list.New(),
		tentativePins:     map[string]*tentativePin{},
		softPinCapacity:   defaultSoftPinCapacity,
		softPinTTL:        defaultSoftPinTTL,
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
	acquisition, err := p.acquireAvailable(req)
	if err != nil && shouldWaitForAcquire(req, err) {
		acquisition, err = p.acquireAvailableWait(ctx, req)
	}
	if err != nil {
		return nil, err
	}
	refreshPin := -1
	if acquisition.pinned && acquisition.index == acquisition.preferred {
		refreshPin = acquisition.preferred
	}
	p.logSelection(req, acquisition.index, false, acquisition.index != acquisition.preferred, acquisition.pinned && acquisition.index == acquisition.preferred, acquisition.inflight)
	return p.streamAttempt(ctx, req, acquisition.index, false, acquisition.release, acquisition.unpin, refreshPin, acquisition.tentative)
}

// shouldWaitForAcquire waits on pool saturation for all traffic, and on
// broader cooldown/quota pressure for extraction turns (Report Studio).
func shouldWaitForAcquire(req Request, err error) bool {
	if !waitableAcquireError(err) {
		return false
	}
	if req.Extraction {
		return true
	}
	return errors.Is(err, ErrClientPoolSaturated)
}

// acquireAvailableWait absorbs temporary pool saturation and cooldown instead
// of returning 429 to the durable extraction worker.
func (p *PooledService) acquireAvailableWait(ctx context.Context, req Request) (poolAcquisition, error) {
	backoff := 50 * time.Millisecond
	waiting := false
	defer func() {
		if waiting {
			p.metrics.DecCodexQueueDepth()
		}
	}()
	for {
		acquisition, err := p.acquireAvailable(req)
		if err == nil {
			return acquisition, nil
		}
		if !waitableAcquireError(err) {
			return poolAcquisition{}, err
		}
		if !waiting {
			p.metrics.IncCodexQueueDepth()
			waiting = true
		}
		wait := backoff
		if until, ok := p.earliestCooldownExpiry(); ok {
			if remaining := until.Sub(p.now()); remaining > 0 {
				wait = remaining
				if wait > 5*time.Second {
					wait = 5 * time.Second
				}
			}
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return poolAcquisition{}, ctx.Err()
		case <-timer.C:
		}
		if backoff < time.Second {
			backoff *= 2
		}
	}
}

func waitableAcquireError(err error) bool {
	if errors.Is(err, ErrClientPoolSaturated) {
		return true
	}
	switch ClassifyFailure(err) {
	case FailureRateLimit, FailureQuota, FailureTransient:
		return true
	default:
		return false
	}
}

func (p *PooledService) earliestCooldownExpiry() (time.Time, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	var earliest time.Time
	found := false
	for _, cooldown := range p.cooldowns {
		if cooldown.until.After(now) && (!found || cooldown.until.Before(earliest)) {
			earliest = cooldown.until
			found = true
		}
	}
	return earliest, found
}

func (p *PooledService) streamAttempt(ctx context.Context, req Request, index int, retried bool, release func(), unpin *pendingUnpin, refreshPin int, tentative *tentativePin) (<-chan StreamEvent, error) {
	attemptCtx, cancel := context.WithCancel(ctx)
	events, err := p.clients[index].service.Stream(attemptCtx, req)
	if err != nil {
		cancel()
		if reason, unhealthy := p.unpinReason(err, PhaseConnect); unhealthy {
			if reason == unpinReasonCooldown {
				p.coolClient(index, err)
			}
			release()
			if !retried {
				if alternate, inflight, altRelease, ok := p.acquireAlternate(req, index); ok {
					unpin = firstPendingUnpin(req, unpin, index, reason)
					p.moveTentative(tentative, index, alternate)
					p.logSelection(req, alternate, false, true, false, inflight)
					return p.streamAttempt(ctx, req, alternate, true, altRelease, unpin, refreshPin, tentative)
				}
			}
			p.releaseTentative(tentative)
			return nil, err
		}
		release()
		if !retried && p.shouldFallback(index, err) {
			p.releaseTentative(tentative)
			if inflight, fbRelease, ok, _, _ := p.tryAcquireClient(req, 0, false); ok {
				p.logSelection(req, 0, true, false, false, inflight)
				return p.streamAttempt(ctx, req, 0, true, fbRelease, unpin, refreshPin, nil)
			}
			return nil, err
		}
		p.releaseTentative(tentative)
		return nil, err
	}

	out := make(chan StreamEvent, 1)
	go p.forwardAttempt(ctx, cancel, req, index, events, out, retried, release, unpin, refreshPin, tentative)
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
	unpin *pendingUnpin,
	refreshPin int,
	tentative *tentativePin,
) {
	defer close(out)
	defer cancel()
	tentativePending := tentative != nil
	defer func() {
		if tentativePending {
			p.releaseTentative(tentative)
		}
	}()

	first, ok := receivePoolEvent(ctx, events)
	if !ok {
		release()
		if tentativePending {
			p.releaseTentative(tentative)
			tentativePending = false
		}
		if err := ctx.Err(); err != nil {
			trySendContextError(out, err)
		}
		return
	}
	// Upstream may emit ID/model metadata before any user-visible content.
	// Hold it until the first substantive event so a transport failure remains
	// safely retryable when Cursor has received no text or tool call.
	var pendingMetadata StreamEvent
	for first.Err == nil && !first.Done && !hasEmittedContent(first) {
		mergeStreamMetadata(&pendingMetadata, first)
		first, ok = receivePoolEvent(ctx, events)
		if !ok {
			if err := ctx.Err(); err != nil {
				release()
				if tentativePending {
					p.releaseTentative(tentative)
					tentativePending = false
				}
				trySendContextError(out, err)
				return
			}
			p.recordSuccessfulTentativeSelection(req, index, unpin, refreshPin, tentative)
			tentativePending = false
			release()
			p.sendPoolEvent(ctx, out, pendingMetadata)
			return
		}
	}
	mergeStreamMetadata(&first, pendingMetadata)
	reason, unhealthy := p.unpinReason(first.Err, PhaseFirstEvent)
	if first.Err != nil {
		p.logf(
			"codex_first_event_retry_check request_id=%s shard=%d client_label=%s retried=%t eligible=%t reason=%s failure_class=%s\n",
			requestID(req),
			index,
			p.clients[index].label,
			retried,
			unhealthy,
			reason,
			ClassifyFailure(first.Err),
		)
	}
	if first.Err != nil && unhealthy {
		if reason == unpinReasonCooldown {
			p.coolClient(index, first.Err)
		}
		if !retried {
			if alternate, inflight, altRelease, available := p.acquireAlternate(req, index); available {
				p.logTransportRetry(req, index, "alternate", "acquired", alternate, inflight, "", false)
				cancel()
				release()
				unpin = firstPendingUnpin(req, unpin, index, reason)
				p.moveTentative(tentative, index, alternate)
				p.logSelection(req, alternate, false, true, false, inflight)
				tentativePending = false
				retryEvents, err := p.streamAttempt(ctx, req, alternate, true, altRelease, unpin, refreshPin, tentative)
				if err != nil {
					p.sendPoolEvent(ctx, out, StreamEvent{Err: err})
					return
				}
				p.forwardRemaining(ctx, out, retryEvents, nil, nil)
				return
			}
			p.logTransportRetry(req, index, "alternate", "unavailable", -1, 0, "", false)
			if reason == unpinReasonTransport {
				cancel()
				release()
				tentativePending = false
				inflight, retryRelease, available, cooldownClass, cooling := p.tryAcquireClient(req, index, false)
				if !available {
					blockedBy := "saturated"
					if cooling {
						blockedBy = "cooldown"
					}
					p.logTransportRetry(req, index, "same_client", blockedBy, index, inflight, cooldownClass, cooling)
					p.releaseTentative(tentative)
					p.sendPoolEvent(ctx, out, first)
					return
				}
				p.logTransportRetry(req, index, "same_client", "acquired", index, inflight, "", false)
				p.logSelection(req, index, false, true, false, inflight)
				retryEvents, err := p.streamAttempt(ctx, req, index, true, retryRelease, unpin, refreshPin, tentative)
				if err != nil {
					p.sendPoolEvent(ctx, out, StreamEvent{Err: err})
					return
				}
				p.forwardRemaining(ctx, out, retryEvents, nil, nil)
				return
			}
		}
	}
	if first.Err != nil && reason == unpinReasonTransport {
		p.logTransportRetry(req, index, "none", "not_retried", -1, 0, "", false)
	}

	defer release()
	if first.Err != nil {
		if tentativePending {
			p.releaseTentative(tentative)
			tentativePending = false
		}
		p.sendPoolEvent(ctx, out, first)
		return
	}
	if first.Done {
		// Commit affinity before the terminal event can release a queued turn.
		p.recordSuccessfulTentativeSelection(req, index, unpin, refreshPin, tentative)
		tentativePending = false
		p.sendPoolEvent(ctx, out, first)
		return
	}
	if !p.sendPoolEvent(ctx, out, first) {
		return
	}
	p.forwardRemaining(ctx, out, events, func() {
		p.recordSuccessfulTentativeSelection(req, index, unpin, refreshPin, tentative)
		tentativePending = false
	}, func() {
		p.releaseTentative(tentative)
		tentativePending = false
	})
}

func hasEmittedContent(event StreamEvent) bool {
	return event.Delta != "" ||
		event.ReasoningDelta != "" ||
		len(event.ToolCalls) > 0 ||
		event.ToolCallDelta != nil
}

func mergeStreamMetadata(dst *StreamEvent, src StreamEvent) {
	if dst.ID == "" {
		dst.ID = src.ID
	}
	if dst.Model == "" {
		dst.Model = src.Model
	}
	if dst.Usage.TotalTokens == 0 && dst.Usage.PromptTokens == 0 && dst.Usage.CompletionTokens == 0 {
		dst.Usage = src.Usage
	}
}

func (p *PooledService) logTransportRetry(req Request, from int, target, result string, to, inflight int, cooldownClass FailureClass, cooling bool) {
	p.logf(
		"codex_transport_retry request_id=%s from_shard=%d from_client=%s target=%s result=%s to_shard=%d inflight=%d cooldown=%t cooldown_class=%s\n",
		requestID(req),
		from,
		p.clients[from].label,
		target,
		result,
		to,
		inflight,
		cooling,
		cooldownClass,
	)
}

func (p *PooledService) forwardRemaining(ctx context.Context, out chan<- StreamEvent, events <-chan StreamEvent, success func(), failure func()) {
	for {
		select {
		case <-ctx.Done():
			if failure != nil {
				failure()
			}
			trySendContextError(out, ctx.Err())
			return
		case event, ok := <-events:
			if !ok {
				if success != nil {
					success()
				}
				return
			}
			if event.Err != nil {
				if failure != nil {
					failure()
				}
				p.sendPoolEvent(ctx, out, event)
				return
			}
			if event.Done {
				// Commit affinity before the terminal event can release a queued turn.
				if success != nil {
					success()
				}
				p.sendPoolEvent(ctx, out, event)
				return
			}
			if !p.sendPoolEvent(ctx, out, event) {
				if failure != nil {
					failure()
				}
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

func (p *PooledService) unpinReason(err error, phase FailurePhase) (string, bool) {
	if err == nil {
		return "", false
	}
	if p.cooldownEligible(err, phase) {
		return unpinReasonCooldown, true
	}
	class := ClassifyFailure(err)
	if !MayRotateAccount(class, phase) {
		return "", false
	}
	if errors.Is(err, ErrClientUnavailable) {
		return unpinReasonUnavailable, true
	}
	if class == FailureAuth {
		return unpinReasonAuth, true
	}
	// A TLS record failure before the first upstream event is safe to retry:
	// no assistant content or tool call has reached the caller.
	if phase == PhaseFirstEvent && strings.Contains(err.Error(), "tls: bad record MAC") {
		return unpinReasonTransport, true
	}
	return "", false
}

func firstPendingUnpin(req Request, current *pendingUnpin, from int, reason string) *pendingUnpin {
	if current != nil {
		return current
	}
	return &pendingUnpin{
		key:    affinityKey(req),
		from:   from,
		reason: reason,
	}
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
	}
	// Cap far-future resets_at so a recovered quota is re-probed. Without
	// this, a single-client pool can stay dark until process restart.
	if p.cooldownMax > 0 {
		maxUntil := now.Add(p.cooldownMax)
		if until.After(maxUntil) {
			until = maxUntil
		}
	}
	p.cooldowns[index] = clientCooldown{until: until, class: class}
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

// acquireAvailable resolves affinity, checks health and capacity, and leases a
// client in one critical section. Explicit new keys get a tentative placement
// immediately so concurrent sibling turns cannot choose different accounts.
func (p *PooledService) acquireAvailable(req Request) (poolAcquisition, error) {
	now := p.now()
	var cooldownSkips []acquireCooldownSkip
	var saturated []acquireSaturatedClient

	p.mu.Lock()
	owner := p.selectIndex(req)
	preferred := owner
	explicitAffinity := hasExplicitAffinity(req)
	var tentative *tentativePin
	pinned := false
	if explicitAffinity {
		key := affinityKey(req)
		if element := p.softPins[key]; element != nil {
			pin := element.Value.(*softPin)
			if pin.expiresAt.After(now) {
				preferred = pin.index
				pinned = true
				p.softPinLRU.MoveToFront(element)
			} else {
				p.removeSoftPinLocked(element)
			}
		}
		if !pinned {
			tentative = p.tentativePins[key]
			if tentative != nil {
				preferred = tentative.index
			}
		}
	}

	sawEligible := false
	preferredEligible := false
	preferredInflight := 0
	sticky := pinned || tentative != nil
	candidate := -1
	candidateInflight := p.maxInflight
	var preferredCooldownClass FailureClass
	preferredCooling := false
	for offset := range len(p.clients) {
		index := (preferred + offset) % len(p.clients)
		cooldown := p.cooldowns[index]
		if !cooldown.until.After(now) {
			p.cooldowns[index] = clientCooldown{}
		} else {
			if index == preferred {
				preferredCooldownClass = cooldown.class
				preferredCooling = true
			}
			if !sticky || !preferredEligible || index == preferred {
				cooldownSkips = append(cooldownSkips, acquireCooldownSkip{index: index, class: cooldown.class})
			}
			continue
		}

		sawEligible = true
		label := p.clients[index].label
		current := p.inflight[label]
		if current >= p.maxInflight {
			if !sticky || !preferredEligible || index == preferred {
				saturated = append(saturated, acquireSaturatedClient{index: index, inflight: current})
			}
			continue
		}
		if index == preferred {
			preferredEligible = true
			preferredInflight = current
		}
		if candidate == -1 || current < candidateInflight {
			candidate = index
			candidateInflight = current
		}
	}

	if candidate != -1 {
		if preferredEligible && (sticky || preferredInflight-candidateInflight < initialLoadBalanceGap) {
			candidate = preferred
		}
		label := p.clients[candidate].label
		p.inflight[label]++
		inflight := p.inflight[label]
		p.metrics.SetCodexClientInflight(label, inflight)
		if explicitAffinity && !pinned {
			key := affinityKey(req)
			if tentative == nil {
				tentative = &tentativePin{key: key, index: candidate}
				p.tentativePins[key] = tentative
			} else if preferredCooling && candidate != preferred {
				tentative.index = candidate
			}
			tentative.refs++
		}
		p.mu.Unlock()
		p.observeAcquireSkips(req, cooldownSkips, saturated)
		var unpin *pendingUnpin
		if preferredCooling && candidate != preferred {
			unpin = firstPendingUnpin(req, nil, preferred, unpinReasonCooldown)
		}
		return poolAcquisition{
			index:     candidate,
			preferred: preferred,
			inflight:  inflight,
			release:   p.releaseClient(req, candidate, label),
			unpin:     unpin,
			tentative: tentative,
			pinned:    pinned,
		}, nil
	}

	if !sawEligible && req.AllowCooling {
		label := p.clients[preferred].label
		current := p.inflight[label]
		if current < p.maxInflight {
			current++
			p.inflight[label] = current
			p.metrics.SetCodexClientInflight(label, current)
			if explicitAffinity && !pinned {
				key := affinityKey(req)
				if tentative == nil {
					tentative = &tentativePin{key: key, index: preferred}
					p.tentativePins[key] = tentative
				}
				tentative.refs++
			}
			p.mu.Unlock()
			p.observeAcquireSkips(req, cooldownSkips, saturated)
			return poolAcquisition{
				index:     preferred,
				preferred: preferred,
				inflight:  current,
				release:   p.releaseClient(req, preferred, label),
				tentative: tentative,
				pinned:    pinned,
			}, nil
		}
		saturated = append(saturated, acquireSaturatedClient{index: preferred, inflight: current})
	}
	p.mu.Unlock()
	p.observeAcquireSkips(req, cooldownSkips, saturated)
	if !sawEligible && !req.AllowCooling {
		return poolAcquisition{}, allClientsCoolingError(preferredCooldownClass)
	}
	return poolAcquisition{}, p.saturatedError(req)
}

func (p *PooledService) observeAcquireSkips(req Request, cooldowns []acquireCooldownSkip, saturated []acquireSaturatedClient) {
	for _, cooldown := range cooldowns {
		p.metrics.ObservePoolCooldownSkip(p.clients[cooldown.index].label, string(cooldown.class))
	}
	for _, client := range saturated {
		p.logClientSaturated(req, client.index, client.inflight)
	}
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
	p.metrics.SetCodexClientInflight(label, current)
	p.mu.Unlock()

	return current, p.releaseClient(req, index, label), true, "", false
}

func (p *PooledService) releaseClient(req Request, index int, label string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			if p.inflight[label] > 0 {
				p.inflight[label]--
			}
			remaining := p.inflight[label]
			p.metrics.SetCodexClientInflight(label, remaining)
			p.mu.Unlock()
			p.logRelease(req, index, remaining)
		})
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

func (p *PooledService) selectIndex(req Request) int {
	if len(p.clients) == 1 {
		return 0
	}
	key := affinityKey(req)
	sum := sha256.Sum256([]byte(key))
	value := binary.BigEndian.Uint64(sum[:8])
	return int(value % uint64(len(p.clients)))
}

func (p *PooledService) preferredIndex(req Request) (int, bool) {
	selected := p.selectIndex(req)
	if len(p.clients) == 1 || !hasExplicitAffinity(req) {
		return selected, false
	}
	key := affinityKey(req)
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	element, ok := p.softPins[key]
	if !ok {
		return selected, false
	}
	pin := element.Value.(*softPin)
	if !pin.expiresAt.After(now) {
		p.removeSoftPinLocked(element)
		return selected, false
	}
	p.softPinLRU.MoveToFront(element)
	return pin.index, true
}

func (p *PooledService) recordSuccessfulTentativeSelection(req Request, index int, unpin *pendingUnpin, refreshPin int, tentative *tentativePin) {
	if p.promoteTentative(tentative, index) {
		if unpin != nil && unpin.from != index {
			p.logUnpin(req, unpin.from, index, unpin.reason)
		}
		return
	}
	if tentative != nil {
		refreshPin = index
	}
	p.recordSuccessfulSelection(req, index, unpin, refreshPin)
}

func (p *PooledService) recordSuccessfulSelection(req Request, index int, unpin *pendingUnpin, refreshPin int) {
	if len(p.clients) == 1 || !hasExplicitAffinity(req) {
		return
	}
	key := affinityKey(req)
	now := p.now()
	shouldLog := false
	p.mu.Lock()
	element := p.softPins[key]
	if element != nil && !element.Value.(*softPin).expiresAt.After(now) {
		p.removeSoftPinLocked(element)
		element = nil
	}
	if unpin != nil && unpin.key == key && unpin.from != index {
		if element == nil || element.Value.(*softPin).index == unpin.from {
			p.setSoftPinLocked(key, index, now)
			shouldLog = true
		} else if element.Value.(*softPin).index == index {
			p.setSoftPinLocked(key, index, now)
		}
	} else if refreshPin == index && (element == nil || element.Value.(*softPin).index == index) {
		p.setSoftPinLocked(key, index, now)
	}
	p.mu.Unlock()
	if shouldLog {
		p.logUnpin(req, unpin.from, index, unpin.reason)
	}
}

func (p *PooledService) promoteTentative(tentative *tentativePin, index int) bool {
	if tentative == nil {
		return false
	}
	now := p.now()
	p.mu.Lock()
	if p.tentativePins[tentative.key] != tentative {
		p.mu.Unlock()
		return false
	}
	delete(p.tentativePins, tentative.key)
	p.setSoftPinLocked(tentative.key, index, now)
	p.mu.Unlock()
	return true
}

func (p *PooledService) releaseTentative(tentative *tentativePin) {
	if tentative == nil {
		return
	}
	p.mu.Lock()
	if p.tentativePins[tentative.key] == tentative {
		tentative.refs--
		if tentative.refs <= 0 {
			delete(p.tentativePins, tentative.key)
		}
	}
	p.mu.Unlock()
}

func (p *PooledService) moveTentative(tentative *tentativePin, from int, to int) {
	if tentative == nil || from == to {
		return
	}
	p.mu.Lock()
	if p.tentativePins[tentative.key] == tentative && tentative.index == from {
		tentative.index = to
	}
	p.mu.Unlock()
}

func (p *PooledService) setSoftPinLocked(key string, index int, now time.Time) {
	if element := p.softPins[key]; element != nil {
		pin := element.Value.(*softPin)
		pin.index = index
		pin.expiresAt = now.Add(p.softPinTTL)
		p.softPinLRU.MoveToFront(element)
		return
	}
	element := p.softPinLRU.PushFront(&softPin{
		key:       key,
		index:     index,
		expiresAt: now.Add(p.softPinTTL),
	})
	p.softPins[key] = element
	for len(p.softPins) > p.softPinCapacity {
		p.removeSoftPinLocked(p.softPinLRU.Back())
	}
}

func (p *PooledService) removeSoftPinLocked(element *list.Element) {
	if element == nil {
		return
	}
	pin := element.Value.(*softPin)
	delete(p.softPins, pin.key)
	p.softPinLRU.Remove(element)
}

func affinityKey(req Request) string {
	if req.AffinityKey != "" {
		return req.AffinityKey
	}
	if req.AffinityKeyHash != "" {
		return req.AffinityKeyHash
	}
	return "global"
}

func hasExplicitAffinity(req Request) bool {
	return req.AffinityKey != "" || req.AffinityKeyHash != ""
}

func (p *PooledService) logSelection(req Request, index int, fallback bool, rotated bool, pinned bool, inflight int) {
	result := "normal"
	if fallback {
		result = "fallback"
	} else if rotated {
		result = "rotated"
	} else if pinned {
		result = "pinned"
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

func (p *PooledService) logUnpin(req Request, from int, to int, reason string) {
	keyHash := req.AffinityKeyHash
	if keyHash == "" {
		keyHash = "none"
	}
	p.logf(
		"codex_client_unpin key_hash=%s from=%s to=%s reason=%s\n",
		keyHash,
		p.clients[from].label,
		p.clients[to].label,
		reason,
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
