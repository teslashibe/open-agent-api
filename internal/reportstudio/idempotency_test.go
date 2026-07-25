package reportstudio

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testScope(input string) Scope {
	return Scope{
		Caller: "caller", Operation: Operation, InputChecksum: input,
		SchemaVersion: "schema.v1", SchemaChecksum: "schema-checksum-a",
		ModelPolicyVersion: "policy.v1", Model: "model",
	}
}

func TestStoreReplaysAndRejectsScopeConflict(t *testing.T) {
	store := NewStore(time.Minute, 10)
	var calls int
	first, replayed, err := store.Execute(context.Background(), "caller", "key", testScope("input-a"), func() (Success, error) {
		calls++
		return Success{RequestID: "first", Identity: Identity{ResponseID: "stable"}}, nil
	})
	if err != nil || replayed || first.Identity.ResponseID != "stable" {
		t.Fatalf("first = %#v replayed=%t err=%v", first, replayed, err)
	}
	second, replayed, err := store.Execute(context.Background(), "caller", "key", testScope("input-a"), func() (Success, error) {
		calls++
		return Success{}, nil
	})
	if err != nil || !replayed || !second.Identity.Replayed || calls != 1 {
		t.Fatalf("replay = %#v replayed=%t calls=%d err=%v", second, replayed, calls, err)
	}
	_, _, err = store.Execute(context.Background(), "caller", "key", testScope("input-b"), func() (Success, error) {
		return Success{}, nil
	})
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestStoreRejectsSameVersionWithDifferentSchemaContent(t *testing.T) {
	store := NewStore(time.Minute, 10)
	scope := testScope("input")
	if _, _, err := store.Execute(context.Background(), "caller", "key", scope, func() (Success, error) {
		return Success{Identity: Identity{ResponseID: "stable"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	scope.SchemaChecksum = "schema-checksum-b"
	if _, _, err := store.Execute(context.Background(), "caller", "key", scope, func() (Success, error) {
		return Success{}, nil
	}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("schema conflict error = %v", err)
	}
}

func TestStoreDoesNotStartWorkAfterDeadline(t *testing.T) {
	store := NewStore(time.Minute, 10)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	if _, _, err := store.Execute(ctx, "caller", "key", testScope("input"), func() (Success, error) {
		called = true
		return Success{}, nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("deadline error = %v", err)
	}
	if called {
		t.Fatal("expired request started idempotent work")
	}
}

func TestStoreCoalescesConcurrentWork(t *testing.T) {
	store := NewStore(time.Minute, 10)
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	fn := func() (Success, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return Success{Identity: Identity{ResponseID: "stable"}}, nil
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := store.Execute(context.Background(), "caller", "key", testScope("input"), fn)
			results <- err
		}()
	}
	<-started
	close(release)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1", got)
	}
}
