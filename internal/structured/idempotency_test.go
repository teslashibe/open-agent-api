package structured

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testFingerprint stands in for the canonical request fingerprint the server
// derives. Tests that exercise the binding itself use two distinct values; the
// rest use this one so every stored record is bound to something, exactly as it
// is in production.
const testFingerprint = "fingerprint-base"

func baseFingerprint() Fingerprint {
	return Fingerprint{
		Caller:             "tenant-a",
		Operation:          "summary",
		Model:              "gpt-5.6-sol",
		ResolvedModel:      "gpt-5.6-sol",
		ReasoningEffort:    "medium",
		Verbosity:          "low",
		SchemaVersion:      "v1",
		ModelPolicyVersion: "policy-1",
		InputChecksum:      InputChecksum("hello"),
		SchemaChecksum:     SchemaChecksum([]byte(`{"type":"object"}`)),
	}
}

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

	first, replay, err := store.Do(context.Background(), "k", testFingerprint, fn)
	if err != nil || replay || first.UpstreamResponseID != "resp-1" {
		t.Fatalf("first = %#v replay=%v err=%v", first, replay, err)
	}
	second, replay, err := store.Do(context.Background(), "k", testFingerprint, fn)
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

	if _, _, err := store.Do(context.Background(), "k", testFingerprint, fn); err == nil {
		t.Fatal("Do() hid the first failure")
	}
	response, replay, err := store.Do(context.Background(), "k", testFingerprint, fn)
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
			_, replay, err := store.Do(context.Background(), "k", testFingerprint, func() (Response, error) {
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

	if _, _, err := store.Do(context.Background(), "k", testFingerprint, fn); err != nil {
		t.Fatal(err)
	}
	now = now.Add(59 * time.Second)
	if _, replay, _ := store.Do(context.Background(), "k", testFingerprint, fn); !replay {
		t.Fatal("entry expired before its TTL")
	}
	now = now.Add(2 * time.Second)
	if _, replay, _ := store.Do(context.Background(), "k", testFingerprint, fn); replay {
		t.Fatal("entry replayed after its TTL")
	}
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls)
	}
}

func TestStoreRejectsCapacityWithoutEvictingLiveBindings(t *testing.T) {
	store := NewIdempotencyStore(time.Hour, 4, nil)
	for i := 0; i < 4; i++ {
		if _, _, err := store.Do(context.Background(), "k"+itoa(i), testFingerprint, func() (Response, error) { return Response{UpstreamResponseID: "resp"}, nil }); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := store.Do(context.Background(), "new", testFingerprint, func() (Response, error) { t.Fatal("capacity rejection called upstream"); return Response{}, nil }); !errors.Is(err, ErrIdempotencyCapacity) {
		t.Fatalf("capacity error = %v", err)
	}
	if store.Len() != 4 {
		t.Fatalf("entries = %d, want 4", store.Len())
	}
	for i := 0; i < 4; i++ {
		response, replay, err := store.Do(context.Background(), "k"+itoa(i), testFingerprint, func() (Response, error) { t.Fatal("live binding was evicted"); return Response{}, nil })
		if err != nil || !replay || response.UpstreamResponseID != "resp" {
			t.Fatalf("replay %d = %#v %v %v", i, response, replay, err)
		}
	}
}

func TestStoreWithoutKeyAlwaysRuns(t *testing.T) {
	store := NewIdempotencyStore(time.Minute, 4, nil)
	calls := 0
	for i := 0; i < 3; i++ {
		if _, replay, _ := store.Do(context.Background(), "", testFingerprint, func() (Response, error) {
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

// AC4: a waiter starts at the 10 ms floor so a fast peer is noticed promptly,
// then backs off toward a bounded ceiling so a long upstream call cannot turn
// every duplicate into a busy loop over a shared filesystem.

// A backend that fails every operation must degrade to running fn, never to a
// new error class.

// AC4: reusing a live key with a different fingerprint is a deterministic
// conflict. No upstream call, no stored record, and no replay of a response
// produced under different parameters.
func TestStoreConflictsOnADivergentFingerprint(t *testing.T) {
	store := NewIdempotencyStore(time.Minute, 8, nil)
	results := map[string]int{}
	store.WithObserver(func(result string) { results[result]++ })
	var calls int32

	if _, _, err := store.Do(context.Background(), "k", "fingerprint-a", upstreamOnce(&calls, "resp-1")); err != nil {
		t.Fatal(err)
	}

	response, replay, err := store.Do(context.Background(), "k", "fingerprint-b", upstreamOnce(&calls, "resp-2"))
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("divergent fingerprint = %#v replay=%v err=%v, want ErrIdempotencyConflict", response, replay, err)
	}
	if replay || response.UpstreamResponseID != "" {
		t.Fatalf("a conflict leaked a response: %#v replay=%v", response, replay)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("upstream calls = %d, want 1 (a conflict must not call upstream)", got)
	}
	if results[IdempotencyConflict] != 1 {
		t.Fatalf("outcomes = %v, want one conflict", results)
	}

	// The original binding survives the conflict: an identical retry still
	// replays without another bill.
	replayed, replay, err := store.Do(context.Background(), "k", "fingerprint-a", upstreamOnce(&calls, "resp-3"))
	if err != nil || !replay || replayed.UpstreamResponseID != "resp-1" {
		t.Fatalf("identical retry after a conflict = %#v replay=%v err=%v", replayed, replay, err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("upstream calls = %d, want 1", got)
	}
}

// A duplicate that arrives while a divergent call is still in flight must
// conflict rather than join the single-flight and be handed the answer to a
// different question.
func TestStoreConflictsWithAnInFlightDivergentCall(t *testing.T) {
	store := NewIdempotencyStore(time.Minute, 8, nil)
	release := make(chan struct{})
	started := make(chan struct{})
	var calls int32

	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		if _, _, err := store.Do(context.Background(), "k", "fingerprint-a", func() (Response, error) {
			atomic.AddInt32(&calls, 1)
			close(started)
			<-release
			return Response{UpstreamResponseID: "resp-leader"}, nil
		}); err != nil {
			t.Errorf("leader Do() error = %v", err)
		}
	}()

	<-started
	_, _, err := store.Do(context.Background(), "k", "fingerprint-b", upstreamOnce(&calls, "resp-divergent"))
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("in-flight divergent duplicate err = %v, want ErrIdempotencyConflict", err)
	}
	close(release)
	<-leaderDone

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("upstream calls = %d, want 1", got)
	}
}

// Without a key there is nothing to bind, so a fingerprint cannot conflict.
func TestStoreWithoutKeyNeverConflicts(t *testing.T) {
	store := NewIdempotencyStore(time.Minute, 4, nil)
	var calls int32
	for _, fingerprint := range []string{"fingerprint-a", "fingerprint-b"} {
		if _, _, err := store.Do(context.Background(), "", fingerprint, upstreamOnce(&calls, "resp")); err != nil {
			t.Fatalf("keyless Do(%s) error = %v", fingerprint, err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("upstream calls = %d, want 2", got)
	}
}

func upstreamOnce(calls *int32, id string) func() (Response, error) {
	return func() (Response, error) {
		atomic.AddInt32(calls, 1)
		return Response{UpstreamResponseID: id}, nil
	}
}
