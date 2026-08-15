package server

import (
	"context"
	"io"
	"testing"
	"time"
)

func TestAgentQueuePriorityOrdersEligibleWaiters(t *testing.T) {
	q := newAgentQueue(true, 1, 1, 10, time.Second, "", true, time.Now, func(string, ...any) {})
	firstKey := newAgentQueueKey("test", "first")
	lowKey := newAgentQueueKey("test", "low")
	highKey := newAgentQueueKey("test", "high")

	releaseFirst, _, err := q.acquire(context.Background(), "first", firstKey, turnClassToolGenerating)
	if err != nil {
		t.Fatalf("first acquire error = %v", err)
	}

	lowDone := acquireQueueAsync(t, q, "low", lowKey, turnClassToolGenerating)
	waitQueueWaiters(t, q, 1)
	highDone := acquireQueueAsync(t, q, "high", highKey, turnClassToolResultContinuation)
	waitQueueWaiters(t, q, 2)

	releaseFirst()
	highRelease := waitQueueAcquire(t, highDone)
	select {
	case <-lowDone:
		t.Fatal("low-priority waiter acquired before high-priority waiter")
	case <-time.After(30 * time.Millisecond):
	}
	highRelease()
	lowRelease := waitQueueAcquire(t, lowDone)
	lowRelease()
}

func TestAgentQueueInteractivePriorityJumpsAheadOfBatch(t *testing.T) {
	q := newAgentQueue(true, 1, 1, 10, time.Second, "", true, time.Now, func(string, ...any) {})
	firstKey := newAgentQueueKey("test", "first")
	batchKey := newAgentQueueKey("test", "batch")
	interactiveKey := newAgentQueueKey("test", "interactive")

	releaseFirst, _, err := q.acquire(context.Background(), "first", firstKey, turnClassSimpleNoTool)
	if err != nil {
		t.Fatalf("first acquire error = %v", err)
	}

	batchDone := acquireQueueAsync(t, q, "batch", batchKey, turnClassSimpleNoTool)
	waitQueueWaiters(t, q, 1)
	interactiveDone := make(chan queueAcquireResult, 1)
	go func() {
		release, _, err := q.acquireWithPriority(context.Background(), "interactive", interactiveKey, turnClassSimpleNoTool, agentQueuePriorityInteractive)
		interactiveDone <- queueAcquireResult{release: release, err: err}
	}()
	waitQueueWaiters(t, q, 2)

	releaseFirst()
	interactiveRelease := waitQueueAcquire(t, interactiveDone)
	select {
	case <-batchDone:
		t.Fatal("batch waiter acquired before interactive waiter")
	case <-time.After(30 * time.Millisecond):
	}
	interactiveRelease()
	batchRelease := waitQueueAcquire(t, batchDone)
	batchRelease()
}

func TestAgentQueuePriorityDisabledKeepsFIFO(t *testing.T) {
	q := newAgentQueue(true, 1, 1, 10, time.Second, "", false, time.Now, func(string, ...any) {})
	firstKey := newAgentQueueKey("test", "first")
	lowKey := newAgentQueueKey("test", "low")
	highKey := newAgentQueueKey("test", "high")

	releaseFirst, _, err := q.acquire(context.Background(), "first", firstKey, turnClassToolGenerating)
	if err != nil {
		t.Fatalf("first acquire error = %v", err)
	}

	lowDone := acquireQueueAsync(t, q, "low", lowKey, turnClassToolGenerating)
	waitQueueWaiters(t, q, 1)
	highDone := acquireQueueAsync(t, q, "high", highKey, turnClassToolResultContinuation)
	waitQueueWaiters(t, q, 2)

	releaseFirst()
	lowRelease := waitQueueAcquire(t, lowDone)
	select {
	case <-highDone:
		t.Fatal("high-priority waiter bypassed FIFO while priority was disabled")
	case <-time.After(30 * time.Millisecond):
	}
	lowRelease()
	highRelease := waitQueueAcquire(t, highDone)
	highRelease()
}

func TestAgentQueuePriorityDoesNotBypassSameKeyActive(t *testing.T) {
	q := newAgentQueue(true, 2, 1, 10, time.Second, "", true, time.Now, func(string, ...any) {})
	sameKey := newAgentQueueKey("test", "same")
	otherKey := newAgentQueueKey("test", "other")

	releaseFirst, _, err := q.acquire(context.Background(), "first", sameKey, turnClassToolGenerating)
	if err != nil {
		t.Fatalf("first acquire error = %v", err)
	}

	highSameDone := acquireQueueAsync(t, q, "high-same", sameKey, turnClassToolResultContinuation)
	waitQueueWaiters(t, q, 1)
	otherDone := acquireQueueAsync(t, q, "other", otherKey, turnClassToolGenerating)

	otherRelease := waitQueueAcquire(t, otherDone)
	select {
	case <-highSameDone:
		t.Fatal("same-key high-priority waiter acquired while key was already active")
	case <-time.After(30 * time.Millisecond):
	}
	otherRelease()
	releaseFirst()
	highSameRelease := waitQueueAcquire(t, highSameDone)
	highSameRelease()
}

func TestAgentQueueCanceledPriorityWaiterIsRemoved(t *testing.T) {
	q := newAgentQueue(true, 1, 1, 10, time.Second, "", true, time.Now, func(string, ...any) {})
	firstKey := newAgentQueueKey("test", "first")
	highKey := newAgentQueueKey("test", "high")
	lowKey := newAgentQueueKey("test", "low")

	releaseFirst, _, err := q.acquire(context.Background(), "first", firstKey, turnClassToolGenerating)
	if err != nil {
		t.Fatalf("first acquire error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	highDone := acquireQueueAsyncWithContext(t, q, ctx, "high", highKey, turnClassToolResultContinuation)
	waitQueueWaiters(t, q, 1)
	lowDone := acquireQueueAsync(t, q, "low", lowKey, turnClassToolGenerating)
	waitQueueWaiters(t, q, 2)

	cancel()
	result := waitQueueResult(t, highDone)
	if result.err == nil || result.release != nil {
		t.Fatalf("canceled acquire = release_set:%t err:%v, want cancellation error", result.release != nil, result.err)
	}
	waitQueueWaiters(t, q, 1)

	releaseFirst()
	lowRelease := waitQueueAcquire(t, lowDone)
	lowRelease()
}

func TestAgentQueueZeroTimeoutWaitsUntilCapacity(t *testing.T) {
	q := newAgentQueue(true, 1, 1, 10, 0, "", true, time.Now, func(string, ...any) {})
	firstKey := newAgentQueueKey("test", "first")
	secondKey := newAgentQueueKey("test", "second")

	releaseFirst, _, err := q.acquire(context.Background(), "first", firstKey, turnClassToolGenerating)
	if err != nil {
		t.Fatalf("first acquire error = %v", err)
	}
	secondDone := acquireQueueAsync(t, q, "second", secondKey, turnClassToolGenerating)
	waitQueueWaiters(t, q, 1)

	select {
	case result := <-secondDone:
		t.Fatalf("zero-timeout waiter returned before capacity: %v", result.err)
	case <-time.After(30 * time.Millisecond):
	}

	releaseFirst()
	secondRelease := waitQueueAcquire(t, secondDone)
	secondRelease()
}

type queueAcquireResult struct {
	release func()
	err     error
}

func acquireQueueAsync(t *testing.T, q *agentQueue, requestID string, key agentQueueKey, class turnClass) <-chan queueAcquireResult {
	t.Helper()
	return acquireQueueAsyncWithContext(t, q, context.Background(), requestID, key, class)
}

func acquireQueueAsyncWithContext(t *testing.T, q *agentQueue, ctx context.Context, requestID string, key agentQueueKey, class turnClass) <-chan queueAcquireResult {
	t.Helper()
	done := make(chan queueAcquireResult, 1)
	go func() {
		release, _, err := q.acquire(ctx, requestID, key, class)
		done <- queueAcquireResult{release: release, err: err}
	}()
	return done
}

func waitQueueAcquire(t *testing.T, done <-chan queueAcquireResult) func() {
	t.Helper()
	result := waitQueueResult(t, done)
	if result.err != nil {
		t.Fatalf("acquire error = %v", result.err)
	}
	if result.release == nil {
		t.Fatal("acquire returned nil release")
	}
	return result.release
}

func waitQueueResult(t *testing.T, done <-chan queueAcquireResult) queueAcquireResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for queue acquire")
		return queueAcquireResult{err: io.ErrUnexpectedEOF}
	}
}

func waitQueueWaiters(t *testing.T, q *agentQueue, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		q.mu.Lock()
		got := len(q.waiters)
		q.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	q.mu.Lock()
	got := len(q.waiters)
	q.mu.Unlock()
	t.Fatalf("waiters = %d, want %d", got, want)
}
