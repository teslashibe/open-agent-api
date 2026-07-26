package structured

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func baseKeyParts() KeyParts {
	return KeyParts{
		Caller:             "tenant-a",
		Operation:          "summary",
		InputChecksum:      InputChecksum("hello"),
		SchemaVersion:      "v1",
		ModelPolicyVersion: "policy-1",
		IdempotencyKey:     "idem-1",
	}
}

func TestKeyIsScopedByEveryDimension(t *testing.T) {
	base := baseKeyParts().Key()
	if base == "" {
		t.Fatal("Key() returned an empty string")
	}
	if base != baseKeyParts().Key() {
		t.Fatal("Key() is not deterministic")
	}

	for name, mutate := range map[string]func(*KeyParts){
		"caller":               func(p *KeyParts) { p.Caller = "tenant-b" },
		"operation":            func(p *KeyParts) { p.Operation = "extract" },
		"input checksum":       func(p *KeyParts) { p.InputChecksum = InputChecksum("goodbye") },
		"schema version":       func(p *KeyParts) { p.SchemaVersion = "v2" },
		"model policy version": func(p *KeyParts) { p.ModelPolicyVersion = "policy-2" },
		"idempotency key":      func(p *KeyParts) { p.IdempotencyKey = "idem-2" },
	} {
		t.Run(name, func(t *testing.T) {
			parts := baseKeyParts()
			mutate(&parts)
			if parts.Key() == base {
				t.Fatalf("changing %s did not change the idempotency key", name)
			}
		})
	}
}

// Length-prefixing means a value that "borrows" a character from the next
// component cannot collide with the original.
func TestKeyIsNotVulnerableToComponentSmuggling(t *testing.T) {
	left := KeyParts{Caller: "ab", Operation: "c"}.Key()
	right := KeyParts{Caller: "a", Operation: "bc"}.Key()
	if left == right {
		t.Fatal("component boundaries are ambiguous")
	}
}

func TestInputChecksumIsStable(t *testing.T) {
	if InputChecksum("hello") != InputChecksum("hello") {
		t.Fatal("InputChecksum() is not deterministic")
	}
	if InputChecksum("hello") == InputChecksum("hello ") {
		t.Fatal("InputChecksum() ignored trailing whitespace")
	}
}

func TestStoreReplaysStoredResponse(t *testing.T) {
	store := NewIdempotencyStore(time.Minute, 8, nil)
	calls := 0
	fn := func() (Response, error) {
		calls++
		return Response{UpstreamResponseID: "resp-1"}, nil
	}

	first, replay, err := store.Do(context.Background(), "k", fn)
	if err != nil || replay || first.UpstreamResponseID != "resp-1" {
		t.Fatalf("first = %#v replay=%v err=%v", first, replay, err)
	}
	second, replay, err := store.Do(context.Background(), "k", fn)
	if err != nil || !replay || second.UpstreamResponseID != "resp-1" {
		t.Fatalf("second = %#v replay=%v err=%v", second, replay, err)
	}
	if calls != 1 {
		t.Fatalf("upstream calls = %d, want 1", calls)
	}
}

func TestStoreDoesNotCacheFailures(t *testing.T) {
	store := NewIdempotencyStore(time.Minute, 8, nil)
	calls := 0
	fn := func() (Response, error) {
		calls++
		if calls == 1 {
			return Response{}, errors.New("upstream boom")
		}
		return Response{UpstreamResponseID: "resp-2"}, nil
	}

	if _, _, err := store.Do(context.Background(), "k", fn); err == nil {
		t.Fatal("Do() hid the first failure")
	}
	response, replay, err := store.Do(context.Background(), "k", fn)
	if err != nil || replay || response.UpstreamResponseID != "resp-2" {
		t.Fatalf("retry = %#v replay=%v err=%v", response, replay, err)
	}
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want 2 (a failure must be retryable)", calls)
	}
	if store.Len() != 1 {
		t.Fatalf("entries = %d, want only the stored success", store.Len())
	}
}

func TestStoreSingleFlightsConcurrentDuplicates(t *testing.T) {
	store := NewIdempotencyStore(time.Minute, 8, nil)
	release := make(chan struct{})
	var calls int32

	var wg sync.WaitGroup
	replays := make([]bool, 8)
	for i := range replays {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, replay, err := store.Do(context.Background(), "k", func() (Response, error) {
				atomic.AddInt32(&calls, 1)
				<-release
				return Response{UpstreamResponseID: "resp"}, nil
			})
			if err != nil {
				t.Errorf("Do() error = %v", err)
			}
			replays[index] = replay
		}(i)
	}
	// Give every goroutine a chance to reach the store before the work finishes.
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("upstream calls = %d, want 1", got)
	}
	leaders := 0
	for _, replay := range replays {
		if !replay {
			leaders++
		}
	}
	if leaders != 1 {
		t.Fatalf("non-replay responses = %d, want exactly 1", leaders)
	}
}

func TestStoreExpiresEntries(t *testing.T) {
	now := time.Unix(1000, 0)
	store := NewIdempotencyStore(time.Minute, 8, func() time.Time { return now })
	calls := 0
	fn := func() (Response, error) {
		calls++
		return Response{}, nil
	}

	if _, _, err := store.Do(context.Background(), "k", fn); err != nil {
		t.Fatal(err)
	}
	now = now.Add(59 * time.Second)
	if _, replay, _ := store.Do(context.Background(), "k", fn); !replay {
		t.Fatal("entry expired before its TTL")
	}
	now = now.Add(2 * time.Second)
	if _, replay, _ := store.Do(context.Background(), "k", fn); replay {
		t.Fatal("entry replayed after its TTL")
	}
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls)
	}
}

func TestStoreIsBounded(t *testing.T) {
	store := NewIdempotencyStore(time.Hour, 4, nil)
	for i := 0; i < 32; i++ {
		key := "k" + itoa(i)
		if _, _, err := store.Do(context.Background(), key, func() (Response, error) {
			return Response{}, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if store.Len() > 4 {
		t.Fatalf("entries = %d, want at most 4", store.Len())
	}
}

func TestStoreWithoutKeyAlwaysRuns(t *testing.T) {
	store := NewIdempotencyStore(time.Minute, 4, nil)
	calls := 0
	for i := 0; i < 3; i++ {
		if _, replay, _ := store.Do(context.Background(), "", func() (Response, error) {
			calls++
			return Response{}, nil
		}); replay {
			t.Fatal("keyless request reported a replay")
		}
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}
