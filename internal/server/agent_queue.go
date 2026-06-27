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
	enabled bool
	max     int
	limit   int
	timeout time.Duration
	now     func() time.Time
	logf    func(string, ...any)

	mu      sync.Mutex
	active  int
	waiters []*agentQueueWaiter
}

type agentQueueWaiter struct {
	ready chan struct{}
}

func newAgentQueue(enabled bool, maxActive int, limit int, timeout time.Duration, now func() time.Time, logf func(string, ...any)) *agentQueue {
	return &agentQueue{
		enabled: enabled,
		max:     maxActive,
		limit:   limit,
		timeout: timeout,
		now:     now,
		logf:    logf,
	}
}

func (q *agentQueue) acquire(ctx context.Context, requestID string) (func(), error) {
	if q == nil || !q.enabled {
		return func() {}, nil
	}

	start := q.now()
	q.mu.Lock()
	if q.active < q.max && len(q.waiters) == 0 {
		q.active++
		active := q.active
		q.mu.Unlock()
		q.logf("agent_queue_acquire request_id=%s wait_ms=0 active=%d\n", requestID, active)
		return q.releaseFunc(requestID, start), nil
	}
	if len(q.waiters) >= q.limit {
		q.mu.Unlock()
		q.logf("agent_queue_full request_id=%s limit=%d\n", requestID, q.limit)
		return nil, errAgentQueueFull
	}

	waiter := &agentQueueWaiter{ready: make(chan struct{})}
	q.waiters = append(q.waiters, waiter)
	position := len(q.waiters)
	q.mu.Unlock()
	q.logf("agent_queue_wait request_id=%s position=%d\n", requestID, position)

	timer := time.NewTimer(q.timeout)
	defer timer.Stop()

	select {
	case <-waiter.ready:
		active := q.currentActive()
		q.logf("agent_queue_acquire request_id=%s wait_ms=%d active=%d\n", requestID, q.now().Sub(start).Milliseconds(), active)
		return q.releaseFunc(requestID, start), nil
	case <-timer.C:
		if q.removeWaiter(waiter) {
			q.logf("agent_queue_timeout request_id=%s wait_ms=%d\n", requestID, q.now().Sub(start).Milliseconds())
			return nil, errAgentQueueTimeout
		}
		active := q.currentActive()
		q.logf("agent_queue_acquire request_id=%s wait_ms=%d active=%d\n", requestID, q.now().Sub(start).Milliseconds(), active)
		return q.releaseFunc(requestID, start), nil
	case <-ctx.Done():
		if q.removeWaiter(waiter) {
			return nil, ctx.Err()
		}
		active := q.currentActive()
		q.logf("agent_queue_acquire request_id=%s wait_ms=%d active=%d\n", requestID, q.now().Sub(start).Milliseconds(), active)
		return q.releaseFunc(requestID, start), nil
	}
}

func (q *agentQueue) releaseFunc(requestID string, start time.Time) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			q.mu.Lock()
			if q.active > 0 {
				q.active--
			}
			q.advanceLocked()
			q.mu.Unlock()
			q.logf("agent_queue_release request_id=%s run_ms=%d\n", requestID, q.now().Sub(start).Milliseconds())
		})
	}
}

func (q *agentQueue) currentActive() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.active
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
	for q.active < q.max && len(q.waiters) > 0 {
		waiter := q.waiters[0]
		q.waiters = q.waiters[1:]
		q.active++
		close(waiter.ready)
	}
}
