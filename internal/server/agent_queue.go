package server

import (
	"context"
	"errors"
	"sync"
	"time"
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
	now       func() time.Time
	logf      func(string, ...any)

	mu        sync.Mutex
	active    int
	activeKey map[string]int
	waiters   []*agentQueueWaiter
}

type agentQueueWaiter struct {
	key   agentQueueKey
	ready chan struct{}
}

func newAgentQueue(enabled bool, maxActive int, maxActivePerKey int, limit int, timeout time.Duration, now func() time.Time, logf func(string, ...any)) *agentQueue {
	return &agentQueue{
		enabled:   enabled,
		max:       maxActive,
		maxPerKey: maxActivePerKey,
		limit:     limit,
		timeout:   timeout,
		now:       now,
		logf:      logf,
		activeKey: map[string]int{},
	}
}

func (q *agentQueue) acquire(ctx context.Context, requestID string, key agentQueueKey) (func(), error) {
	if q == nil || !q.enabled {
		return func() {}, nil
	}
	key = key.withDefaults()

	start := q.now()
	q.mu.Lock()
	if q.canAcquireLocked(key) && len(q.waiters) == 0 {
		activeGlobal, activeKey := q.acquireLocked(key)
		q.mu.Unlock()
		q.logf("agent_queue_acquire request_id=%s key_mode=%s key_hash=%s wait_ms=0 active_global=%d active_key=%d\n", requestID, key.Mode, key.Hash, activeGlobal, activeKey)
		return q.releaseFunc(requestID, start, key), nil
	}
	if len(q.waiters) >= q.limit {
		q.mu.Unlock()
		q.logf("agent_queue_full request_id=%s key_mode=%s key_hash=%s limit=%d\n", requestID, key.Mode, key.Hash, q.limit)
		return nil, errAgentQueueFull
	}

	waiter := &agentQueueWaiter{key: key, ready: make(chan struct{})}
	q.waiters = append(q.waiters, waiter)
	position := len(q.waiters)
	q.advanceLocked()
	q.mu.Unlock()
	q.logf("agent_queue_wait request_id=%s key_mode=%s key_hash=%s position=%d\n", requestID, key.Mode, key.Hash, position)

	timer := time.NewTimer(q.timeout)
	defer timer.Stop()

	select {
	case <-waiter.ready:
		activeGlobal, activeKey := q.currentActive(key)
		q.logf("agent_queue_acquire request_id=%s key_mode=%s key_hash=%s wait_ms=%d active_global=%d active_key=%d\n", requestID, key.Mode, key.Hash, q.now().Sub(start).Milliseconds(), activeGlobal, activeKey)
		return q.releaseFunc(requestID, start, key), nil
	case <-timer.C:
		if q.removeWaiter(waiter) {
			q.logf("agent_queue_timeout request_id=%s key_mode=%s key_hash=%s wait_ms=%d\n", requestID, key.Mode, key.Hash, q.now().Sub(start).Milliseconds())
			return nil, errAgentQueueTimeout
		}
		activeGlobal, activeKey := q.currentActive(key)
		q.logf("agent_queue_acquire request_id=%s key_mode=%s key_hash=%s wait_ms=%d active_global=%d active_key=%d\n", requestID, key.Mode, key.Hash, q.now().Sub(start).Milliseconds(), activeGlobal, activeKey)
		return q.releaseFunc(requestID, start, key), nil
	case <-ctx.Done():
		if q.removeWaiter(waiter) {
			return nil, ctx.Err()
		}
		activeGlobal, activeKey := q.currentActive(key)
		q.logf("agent_queue_acquire request_id=%s key_mode=%s key_hash=%s wait_ms=%d active_global=%d active_key=%d\n", requestID, key.Mode, key.Hash, q.now().Sub(start).Milliseconds(), activeGlobal, activeKey)
		return q.releaseFunc(requestID, start, key), nil
	}
}

func (q *agentQueue) releaseFunc(requestID string, start time.Time, key agentQueueKey) func() {
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
			q.logf("agent_queue_release request_id=%s key_mode=%s key_hash=%s run_ms=%d active_global=%d active_key=%d\n", requestID, key.Mode, key.Hash, q.now().Sub(start).Milliseconds(), activeGlobal, activeKey)
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
		index := -1
		for i, waiter := range q.waiters {
			if q.canAcquireLocked(waiter.key) {
				index = i
				break
			}
		}
		if index == -1 {
			return
		}
		waiter := q.waiters[index]
		q.waiters = append(q.waiters[:index], q.waiters[index+1:]...)
		q.acquireLocked(waiter.key)
		close(waiter.ready)
	}
}

func (q *agentQueue) canAcquireLocked(key agentQueueKey) bool {
	return q.active < q.max && q.activeKey[key.Value] < q.maxPerKey
}

func (q *agentQueue) acquireLocked(key agentQueueKey) (int, int) {
	q.active++
	q.activeKey[key.Value]++
	return q.active, q.activeKey[key.Value]
}
