package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	metricspkg "github.com/teslashibe/open-agent-api/internal/metrics"
)

var (
	errAgentQueueFull    = errors.New("agent queue full")
	errAgentQueueTimeout = errors.New("agent queue timeout")
)

type agentQueue struct {
	enabled   bool
	max       int
	maxPerKey int
	limit     int
	timeout   time.Duration
	lockDir   string
	priority  bool
	now       func() time.Time
	logf      func(string, ...any)
	provider  string
	metrics   *metricspkg.Metrics

	mu        sync.Mutex
	active    int
	activeKey map[string]int
	waiters   []*agentQueueWaiter
	nextSeq   int64
}

type agentQueueWaiter struct {
	key      agentQueueKey
	class    turnClass
	priority int
	seq      int64
	ready    chan struct{}
}

const (
	agentQueuePriorityBatch       = 0
	agentQueuePriorityInteractive = 100
)

func newAgentQueue(enabled bool, maxActive int, maxActivePerKey int, limit int, timeout time.Duration, lockDir string, priority bool, now func() time.Time, logf func(string, ...any)) *agentQueue {
	return &agentQueue{
		enabled:   enabled,
		max:       maxActive,
		maxPerKey: maxActivePerKey,
		limit:     limit,
		timeout:   timeout,
		lockDir:   lockDir,
		priority:  priority,
		now:       now,
		logf:      logf,
		activeKey: map[string]int{},
	}
}

func (q *agentQueue) withMetrics(provider string, metrics *metricspkg.Metrics) *agentQueue {
	q.provider = provider
	q.metrics = metrics
	return q
}

func (q *agentQueue) acquire(ctx context.Context, requestID string, key agentQueueKey, class turnClass) (release func(), wait time.Duration, err error) {
	return q.acquireWithPriority(ctx, requestID, key, class, agentQueuePriorityBatch)
}

// acquireWithPriority admits work into the same non-preemptive slot pool while
// allowing trusted call sites to put interactive traffic ahead of queued batch
// work. Running work is never interrupted.
func (q *agentQueue) acquireWithPriority(ctx context.Context, requestID string, key agentQueueKey, class turnClass, basePriority int) (release func(), wait time.Duration, err error) {
	if q == nil {
		return func() {}, 0, nil
	}
	key = key.withDefaults()
	priority := basePriority + agentQueuePriority(class)

	start := q.now()
	defer func() {
		result := "acquired"
		switch {
		case errors.Is(err, errAgentQueueFull):
			result = "full"
		case errors.Is(err, errAgentQueueTimeout):
			result = "timeout"
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			result = "canceled"
		case err != nil:
			result = "error"
		}
		q.metrics.ObserveQueueWait(q.provider, result, q.now().Sub(start))
	}()
	if !q.enabled {
		release, err := q.acquireDistributedLock(ctx, requestID, start, key)
		return release, 0, err
	}

	q.mu.Lock()
	if q.canAcquireLocked(key) && len(q.waiters) == 0 {
		activeGlobal, activeKey := q.acquireLocked(key)
		q.mu.Unlock()
		q.logf("agent_queue_acquire request_id=%s key_mode=%s key_hash=%s turn_class=%s priority=%d wait_ms=0 active_global=%d active_key=%d\n", requestID, key.Mode, key.Hash, class, priority, activeGlobal, activeKey)
		release, err := q.releaseWithDistributedLock(ctx, requestID, start, key, class, priority)
		return release, 0, err
	}
	for len(q.waiters) >= q.limit {
		q.mu.Unlock()
		q.logf("agent_queue_soft_wait request_id=%s key_mode=%s key_hash=%s turn_class=%s priority=%d limit=%d\n", requestID, key.Mode, key.Hash, class, priority, q.limit)
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, q.now().Sub(start), ctx.Err()
		case <-timer.C:
		}
		if q.timeout > 0 && q.now().Sub(start) >= q.timeout {
			return nil, q.now().Sub(start), errAgentQueueTimeout
		}
		q.mu.Lock()
		if q.canAcquireLocked(key) && len(q.waiters) == 0 {
			activeGlobal, activeKey := q.acquireLocked(key)
			q.mu.Unlock()
			q.logf("agent_queue_acquire request_id=%s key_mode=%s key_hash=%s turn_class=%s priority=%d wait_ms=%d active_global=%d active_key=%d\n", requestID, key.Mode, key.Hash, class, priority, q.now().Sub(start).Milliseconds(), activeGlobal, activeKey)
			release, err := q.releaseWithDistributedLock(ctx, requestID, start, key, class, priority)
			return release, q.now().Sub(start), err
		}
	}

	q.nextSeq++
	waiter := &agentQueueWaiter{key: key, class: class, priority: priority, seq: q.nextSeq, ready: make(chan struct{})}
	q.waiters = append(q.waiters, waiter)
	position := len(q.waiters)
	q.advanceLocked()
	q.mu.Unlock()
	q.logf("agent_queue_wait request_id=%s key_mode=%s key_hash=%s turn_class=%s priority=%d position=%d\n", requestID, key.Mode, key.Hash, class, priority, position)

	var timer *time.Timer
	var timerC <-chan time.Time
	if q.timeout > 0 {
		timer = time.NewTimer(q.timeout)
		timerC = timer.C
		defer timer.Stop()
	}

	select {
	case <-waiter.ready:
		activeGlobal, activeKey := q.currentActive(key)
		wait := q.now().Sub(start)
		q.logf("agent_queue_acquire request_id=%s key_mode=%s key_hash=%s turn_class=%s priority=%d wait_ms=%d active_global=%d active_key=%d\n", requestID, key.Mode, key.Hash, class, priority, wait.Milliseconds(), activeGlobal, activeKey)
		release, err := q.releaseWithDistributedLock(ctx, requestID, start, key, class, priority)
		return release, wait, err
	case <-timerC:
		if q.removeWaiter(waiter) {
			wait := q.now().Sub(start)
			q.logf("agent_queue_timeout request_id=%s key_mode=%s key_hash=%s turn_class=%s priority=%d wait_ms=%d\n", requestID, key.Mode, key.Hash, class, priority, wait.Milliseconds())
			return nil, wait, errAgentQueueTimeout
		}
		activeGlobal, activeKey := q.currentActive(key)
		wait := q.now().Sub(start)
		q.logf("agent_queue_acquire request_id=%s key_mode=%s key_hash=%s turn_class=%s priority=%d wait_ms=%d active_global=%d active_key=%d\n", requestID, key.Mode, key.Hash, class, priority, wait.Milliseconds(), activeGlobal, activeKey)
		release, err := q.releaseWithDistributedLock(ctx, requestID, start, key, class, priority)
		return release, wait, err
	case <-ctx.Done():
		if q.removeWaiter(waiter) {
			return nil, q.now().Sub(start), ctx.Err()
		}
		activeGlobal, activeKey := q.currentActive(key)
		wait := q.now().Sub(start)
		q.logf("agent_queue_acquire request_id=%s key_mode=%s key_hash=%s turn_class=%s priority=%d wait_ms=%d active_global=%d active_key=%d\n", requestID, key.Mode, key.Hash, class, priority, wait.Milliseconds(), activeGlobal, activeKey)
		release, err := q.releaseWithDistributedLock(ctx, requestID, start, key, class, priority)
		return release, wait, err
	}
}

func (q *agentQueue) releaseWithDistributedLock(ctx context.Context, requestID string, start time.Time, key agentQueueKey, class turnClass, priority int) (func(), error) {
	releaseLocal := q.releaseFunc(requestID, start, key, class, priority)
	releaseDistributed, err := q.acquireDistributedLock(ctx, requestID, start, key)
	if err != nil {
		releaseLocal()
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			releaseDistributed()
			releaseLocal()
		})
	}, nil
}

func (q *agentQueue) acquireDistributedLock(ctx context.Context, requestID string, start time.Time, key agentQueueKey) (func(), error) {
	if q.lockDir == "" {
		return func() {}, nil
	}
	if err := os.MkdirAll(q.lockDir, 0o700); err != nil {
		return nil, fmt.Errorf("agent queue lock dir: %w", err)
	}
	path := filepath.Join(q.lockDir, key.Hash+".lock")
	remaining := q.timeout
	if q.now != nil && q.timeout > 0 {
		if elapsed := q.now().Sub(start); elapsed > 0 {
			remaining -= elapsed
		}
	}
	if remaining <= 0 {
		remaining = q.timeout
	}
	lockStart := time.Now()
	for {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(file, "request_id=%s\nkey_mode=%s\nkey_hash=%s\n", requestID, key.Mode, key.Hash)
			_ = file.Close()
			q.logf("agent_queue_lock_acquire request_id=%s key_mode=%s key_hash=%s lock_wait_ms=%d\n", requestID, key.Mode, key.Hash, q.now().Sub(start).Milliseconds())
			var once sync.Once
			return func() {
				once.Do(func() {
					if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
						q.logf("agent_queue_lock_release_error request_id=%s key_mode=%s key_hash=%s err=%s\n", requestID, key.Mode, key.Hash, err)
						return
					}
					q.logf("agent_queue_lock_release request_id=%s key_mode=%s key_hash=%s\n", requestID, key.Mode, key.Hash)
				})
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("agent queue lock acquire: %w", err)
		}
		if remaining > 0 && time.Since(lockStart) >= remaining {
			q.logf("agent_queue_lock_timeout request_id=%s key_mode=%s key_hash=%s wait_ms=%d\n", requestID, key.Mode, key.Hash, q.now().Sub(start).Milliseconds())
			return nil, errAgentQueueTimeout
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (q *agentQueue) releaseFunc(requestID string, start time.Time, key agentQueueKey, class turnClass, priority int) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			q.mu.Lock()
			if q.active > 0 {
				q.active--
			}
			if q.activeKey[key.Value] > 0 {
				q.activeKey[key.Value]--
			}
			activeGlobal := q.active
			activeKey := q.activeKey[key.Value]
			q.advanceLocked()
			q.mu.Unlock()
			q.logf("agent_queue_release request_id=%s key_mode=%s key_hash=%s turn_class=%s priority=%d run_ms=%d active_global=%d active_key=%d\n", requestID, key.Mode, key.Hash, class, priority, q.now().Sub(start).Milliseconds(), activeGlobal, activeKey)
		})
	}
}

func (q *agentQueue) currentActive(key agentQueueKey) (int, int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.active, q.activeKey[key.Value]
}

func (q *agentQueue) removeWaiter(waiter *agentQueueWaiter) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, queued := range q.waiters {
		if queued == waiter {
			q.waiters = append(q.waiters[:i], q.waiters[i+1:]...)
			return true
		}
	}
	return false
}

func (q *agentQueue) advanceLocked() {
	for q.active < q.max {
		index := q.nextWaiterLocked()
		if index == -1 {
			return
		}
		waiter := q.waiters[index]
		q.waiters = append(q.waiters[:index], q.waiters[index+1:]...)
		q.acquireLocked(waiter.key)
		close(waiter.ready)
	}
}

func (q *agentQueue) nextWaiterLocked() int {
	index := -1
	for i, waiter := range q.waiters {
		if !q.canAcquireLocked(waiter.key) {
			continue
		}
		if index == -1 {
			index = i
			continue
		}
		if q.priority && waiter.priority > q.waiters[index].priority {
			index = i
		}
	}
	return index
}

func (q *agentQueue) canAcquireLocked(key agentQueueKey) bool {
	return q.active < q.max && q.activeKey[key.Value] < q.maxPerKey
}

func (q *agentQueue) acquireLocked(key agentQueueKey) (int, int) {
	q.active++
	q.activeKey[key.Value]++
	return q.active, q.activeKey[key.Value]
}
