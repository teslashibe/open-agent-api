package server

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestAgentQueueSharedLockAdmitsMaxPerKeyAcrossQueues(t *testing.T) {
	lockDir := t.TempDir()
	logf := func(string, ...any) {}
	q1 := newAgentQueue(true, 10, 3, 10, time.Second, lockDir, false, time.Now, logf)
	q2 := newAgentQueue(true, 10, 3, 10, time.Second, lockDir, false, time.Now, logf)
	key := newAgentQueueKey("structured:global", "default")

	var releases []func()
	for i, q := range []*agentQueue{q1, q2, q1} {
		release, _, err := q.acquire(context.Background(), "request", key, turnClassToolGenerating)
		if err != nil {
			t.Fatalf("acquire %d error = %v", i, err)
		}
		releases = append(releases, release)
	}

	acquiredFourth := make(chan func(), 1)
	errs := make(chan error, 1)
	go func() {
		release, _, err := q2.acquire(context.Background(), "request-4", key, turnClassToolGenerating)
		if err != nil {
			errs <- err
			return
		}
		acquiredFourth <- release
	}()

	select {
	case release := <-acquiredFourth:
		release()
		t.Fatal("fourth request acquired the key while all three shared slots were held")
	case err := <-errs:
		t.Fatalf("fourth acquire error = %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	releases[1]()
	select {
	case release := <-acquiredFourth:
		release()
	case err := <-errs:
		t.Fatalf("fourth acquire error after release = %v", err)
	case <-time.After(time.Second):
		t.Fatal("fourth request did not acquire the slot freed by the second")
	}
	releases[0]()
	releases[2]()

	entries, err := os.ReadDir(lockDir)
	if err != nil {
		t.Fatalf("read lock dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("lock files left after every release: %d", len(entries))
	}
}

func TestAgentQueueSharedLockHonorsLegacyLockFile(t *testing.T) {
	lockDir := t.TempDir()
	logf := func(string, ...any) {}
	key := newAgentQueueKey("structured:global", "default")
	// A pre-slot binary holds only "<hash>.lock"; it must still count as slot 0.
	legacy := agentQueueLockPath(lockDir, key.Hash, 0)
	if err := os.WriteFile(legacy, []byte("request_id=legacy\n"), 0o600); err != nil {
		t.Fatalf("write legacy lock: %v", err)
	}

	single := newAgentQueue(false, 10, 1, 10, 100*time.Millisecond, lockDir, false, time.Now, logf)
	if _, _, err := single.acquire(context.Background(), "request-single", key, turnClassToolGenerating); err != errAgentQueueTimeout {
		t.Fatalf("maxPerKey=1 acquire with legacy lock held error = %v, want timeout", err)
	}

	pair := newAgentQueue(false, 10, 2, 10, time.Second, lockDir, false, time.Now, logf)
	release, _, err := pair.acquire(context.Background(), "request-pair", key, turnClassToolGenerating)
	if err != nil {
		t.Fatalf("maxPerKey=2 acquire with legacy lock held error = %v", err)
	}
	if _, err := os.Stat(agentQueueLockPath(lockDir, key.Hash, 1)); err != nil {
		t.Fatalf("expected slot 1 lock file: %v", err)
	}
	release()
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("releasing slot 1 removed the legacy lock: %v", err)
	}
}
