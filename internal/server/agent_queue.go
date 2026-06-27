package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	lockDir   string
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

func newAgentQueue(enabled bool, maxActive int, maxActivePerKey int, limit int, timeout time.Duration, lockDir string, now func() time.Time, logf func(string, ...any)) *agentQueue {
	return &agentQueue{
		enabled:   enabled,
		max:       maxActive,
		maxPerKey: maxActivePerKey,
		limit:     limit,
		timeout:   timeout,
		lockDir:   lockDir,
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
		return q.releaseWithDistributedLock(ctx, requestID, start, key)
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
		return q.releaseWithDistributedLock(ctx, requestID, start, key)
	case <-timer.C:
		if q.removeWaiter(waiter) {
			q.logf("agent_queue_timeout request_id=%s key_mode=%s key_hash=%s wait_ms=%d\n", requestID, key.Mode, key.Hash, q.now().Sub(start).Milliseconds())
			return nil, errAgentQueueTimeout
		}
		activeGlobal, activeKey := q.currentActive(key)
		q.logf("agent_queue_acquire request_id=%s key_mode=%s key_hash=%s wait_ms=%d active_global=%d active_key=%d\n", requestID, key.Mode, key.Hash, q.now().Sub(start).Milliseconds(), activeGlobal, activeKey)
		return q.releaseWithDistributedLock(ctx, requestID, start, key)
	case <-ctx.Done():
		if q.removeWaiter(waiter) {
			return nil, ctx.Err()
		}
		activeGlobal, activeKey := q.currentActive(key)
		q.logf("agent_queue_acquire request_id=%s key_mode=%s key_hash=%s wait_ms=%d active_global=%d active_key=%d\n", requestID, key.Mode, key.Hash, q.now().Sub(start).Milliseconds(), activeGlobal, activeKey)
		return q.releaseWithDistributedLock(ctx, requestID, start, key)
	}
}

func (q *agentQueue) releaseWithDistributedLock(ctx context.Context, requestID string, start time.Time, key agentQueueKey) (func(), error) {
	releaseLocal := q.releaseFunc(requestID, start, key)
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
