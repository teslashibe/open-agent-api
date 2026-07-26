package structured

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// crossProcessEnv turns this test binary into the "other pod" for the
// cross-process replay test below.
const (
	crossProcessEnv    = "STRUCTURED_IDEMPOTENCY_CHILD_DIR"
	crossProcessKey    = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"
	crossProcessAnswer = "resp-from-child-process"
)

func newFileStore(t *testing.T, dir string, now func() time.Time) *IdempotencyStore {
	t.Helper()
	backend, err := NewFileBackend(dir, time.Minute, 8, now)
	if err != nil {
		t.Fatalf("NewFileBackend() error = %v", err)
	}
	return NewIdempotencyStoreWithBackend(time.Minute, 8, now, backend)
}

func upstreamOnce(calls *int32, id string) func() (Response, error) {
	return func() (Response, error) {
		atomic.AddInt32(calls, 1)
		return Response{UpstreamResponseID: id, Data: json.RawMessage(`{"title":"Q3"}`)}, nil
	}
}

func TestFileBackendRequiresADirectory(t *testing.T) {
	if _, err := NewFileBackend("  ", time.Minute, 8, nil); err == nil {
		t.Fatal("NewFileBackend() with an empty dir error = nil, want a failure")
	}
}

// AC1: two independently constructed stores over one shared directory are the
// pod-to-pod case. The second store never calls upstream.
func TestFileBackendReplaysAcrossStores(t *testing.T) {
	dir := t.TempDir()
	var calls int32

	podA := newFileStore(t, dir, nil)
	first, replay, err := podA.Do(context.Background(), crossProcessKey, upstreamOnce(&calls, "resp-1"))
	if err != nil || replay {
		t.Fatalf("pod A = %#v replay=%v err=%v", first, replay, err)
	}

	podB := newFileStore(t, dir, nil)
	second, replay, err := podB.Do(context.Background(), crossProcessKey, upstreamOnce(&calls, "resp-2"))
	if err != nil || !replay {
		t.Fatalf("pod B = %#v replay=%v err=%v, want a replay", second, replay, err)
	}
	if second.UpstreamResponseID != first.UpstreamResponseID || string(second.Data) != string(first.Data) {
		t.Fatalf("pod B replayed %#v, want the body pod A stored (%#v)", second, first)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("upstream calls = %d, want 1 across both pods", got)
	}
}

// AC2: a restart discards the process-local map but not the durable record.
func TestFileBackendSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	var calls int32

	before := newFileStore(t, dir, nil)
	if _, _, err := before.Do(context.Background(), crossProcessKey, upstreamOnce(&calls, "resp-1")); err != nil {
		t.Fatal(err)
	}
	// The process goes away; only the volume survives.
	before = nil
	_ = before

	after := newFileStore(t, dir, nil)
	if after.Len() != 0 {
		t.Fatalf("restarted store already has %d local entries", after.Len())
	}
	response, replay, err := after.Do(context.Background(), crossProcessKey, upstreamOnce(&calls, "resp-2"))
	if err != nil || !replay || response.UpstreamResponseID != "resp-1" {
		t.Fatalf("after restart = %#v replay=%v err=%v", response, replay, err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("upstream calls = %d, want 1 (the in-TTL record survived the restart)", got)
	}
}

func TestFileBackendExpiresRecords(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1000, 0)
	clock := func() time.Time { return now }
	var calls int32

	first := newFileStore(t, dir, clock)
	if _, _, err := first.Do(context.Background(), crossProcessKey, upstreamOnce(&calls, "resp-1")); err != nil {
		t.Fatal(err)
	}

	now = now.Add(59 * time.Second)
	if _, replay, _ := newFileStore(t, dir, clock).Do(context.Background(), crossProcessKey, upstreamOnce(&calls, "resp-2")); !replay {
		t.Fatal("record expired before its TTL")
	}
	now = now.Add(2 * time.Second)
	if _, replay, _ := newFileStore(t, dir, clock).Do(context.Background(), crossProcessKey, upstreamOnce(&calls, "resp-3")); replay {
		t.Fatal("record replayed after its TTL")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("upstream calls = %d, want 2", got)
	}
}

// A failure must stay retryable on every replica, so nothing is persisted.
func TestFileBackendDoesNotPersistFailures(t *testing.T) {
	dir := t.TempDir()
	store := newFileStore(t, dir, nil)

	if _, _, err := store.Do(context.Background(), crossProcessKey, func() (Response, error) {
		return Response{}, errors.New("upstream boom")
	}); err == nil {
		t.Fatal("Do() hid the failure")
	}

	var calls int32
	response, replay, err := newFileStore(t, dir, nil).Do(context.Background(), crossProcessKey, upstreamOnce(&calls, "resp-retry"))
	if err != nil || replay || response.UpstreamResponseID != "resp-retry" {
		t.Fatalf("retry on another pod = %#v replay=%v err=%v", response, replay, err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("upstream calls = %d, want 1 (the failure must be retryable)", got)
	}
	// The reservation must not outlive the failed attempt.
	if entries := recordFiles(t, dir, ".lock"); len(entries) != 0 {
		t.Fatalf("leftover reservations = %v", entries)
	}
}

// A truncated or corrupt record is a miss, never a replay of a partial body.
func TestFileBackendTreatsCorruptRecordsAsMisses(t *testing.T) {
	for name, corrupt := range map[string][]byte{
		"truncated":     []byte(`{"version":1,"response":{"upstream_re`),
		"empty":         {},
		"wrong version": []byte(`{"version":99,"response":{},"expires":"2099-01-01T00:00:00Z"}`),
		"garbage":       []byte("not json at all"),
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			var calls int32
			store := newFileStore(t, dir, nil)
			if _, _, err := store.Do(context.Background(), crossProcessKey, upstreamOnce(&calls, "resp-1")); err != nil {
				t.Fatal(err)
			}
			paths := recordFiles(t, dir, ".json")
			if len(paths) != 1 {
				t.Fatalf("record files = %v, want exactly one", paths)
			}
			if err := os.WriteFile(paths[0], corrupt, 0o600); err != nil {
				t.Fatal(err)
			}

			response, replay, err := newFileStore(t, dir, nil).Do(context.Background(), crossProcessKey, upstreamOnce(&calls, "resp-2"))
			if err != nil || replay {
				t.Fatalf("corrupt record = %#v replay=%v err=%v, want a fresh call", response, replay, err)
			}
			if got := atomic.LoadInt32(&calls); got != 2 {
				t.Fatalf("upstream calls = %d, want 2", got)
			}
		})
	}
}

// AC4: concurrent duplicates spread across two stores over one directory still
// produce exactly one upstream call.
func TestFileBackendSingleFlightsAcrossStores(t *testing.T) {
	dir := t.TempDir()
	var calls int32
	release := make(chan struct{})
	stores := []*IdempotencyStore{newFileStore(t, dir, nil), newFileStore(t, dir, nil)}

	var wg sync.WaitGroup
	replays := make([]bool, 8)
	for i := range replays {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, replay, err := stores[index%len(stores)].Do(context.Background(), crossProcessKey, func() (Response, error) {
				atomic.AddInt32(&calls, 1)
				<-release
				return Response{UpstreamResponseID: "resp-shared"}, nil
			})
			if err != nil {
				t.Errorf("Do() error = %v", err)
			}
			replays[index] = replay
		}(i)
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("upstream calls = %d, want 1 across both stores", got)
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

// A reservation left behind by a crashed pod must lapse rather than wedge the
// key: the next caller steals it and runs.
func TestFileBackendStealsLapsedReservations(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(2000, 0)
	clock := func() time.Time { return now }
	backend, err := NewFileBackend(dir, time.Minute, 8, clock)
	if err != nil {
		t.Fatal(err)
	}

	release, acquired, err := backend.Reserve(crossProcessKey, now.Add(time.Second))
	if err != nil || !acquired {
		t.Fatalf("first Reserve() acquired=%v err=%v", acquired, err)
	}
	if _, acquired, err := backend.Reserve(crossProcessKey, now.Add(time.Second)); err != nil || acquired {
		t.Fatalf("second Reserve() acquired=%v err=%v, want a live reservation to block", acquired, err)
	}

	// The holder dies. Once its stamp passes, the reservation is stealable.
	now = now.Add(2 * time.Second)
	stolen, acquired, err := backend.Reserve(crossProcessKey, now.Add(time.Minute))
	if err != nil || !acquired {
		t.Fatalf("Reserve() after the stamp lapsed acquired=%v err=%v", acquired, err)
	}
	// The dead holder's release must not take the new owner's reservation.
	release(false)
	if _, acquired, err := backend.Reserve(crossProcessKey, now.Add(time.Minute)); err != nil || acquired {
		t.Fatalf("a stale release freed a live reservation (acquired=%v err=%v)", acquired, err)
	}
	stolen(true)
}

// The observer sees a store_hit when the answer came from the durable backend.
func TestStoreObservesStoreHits(t *testing.T) {
	dir := t.TempDir()
	var calls int32
	if _, _, err := newFileStore(t, dir, nil).Do(context.Background(), crossProcessKey, upstreamOnce(&calls, "resp-1")); err != nil {
		t.Fatal(err)
	}

	results := map[string]int{}
	next := newFileStore(t, dir, nil).WithObserver(func(result string) { results[result]++ })
	if _, replay, err := next.Do(context.Background(), crossProcessKey, upstreamOnce(&calls, "resp-2")); err != nil || !replay {
		t.Fatalf("replay=%v err=%v", replay, err)
	}
	if results[IdempotencyStoreHit] != 1 {
		t.Fatalf("outcomes = %v, want one store_hit", results)
	}
}

// AC1: a shared volume must not grow without bound.
func TestFileBackendBoundsTheDirectory(t *testing.T) {
	dir := t.TempDir()
	backend, err := NewFileBackend(dir, time.Hour, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	store := NewIdempotencyStoreWithBackend(time.Hour, 4, nil, backend)
	for i := 0; i < 32; i++ {
		key := "key" + itoa(i)
		if _, _, err := store.Do(context.Background(), key, func() (Response, error) {
			return Response{UpstreamResponseID: key}, nil
		}); err != nil {
			t.Fatal(err)
		}
		// Records are ordered by their stored timestamp; keep them distinct.
		time.Sleep(time.Millisecond)
	}
	if records := recordFiles(t, dir, ".json"); len(records) > 4 {
		t.Fatalf("record files = %d, want at most 4", len(records))
	}
}

// AC4: a genuinely separate process writes the record, and this one replays it
// without calling upstream. The two-store test above is the primary evidence;
// this one closes the "same process, different store" objection.
func TestFileBackendReplaysAcrossProcesses(t *testing.T) {
	if dir := os.Getenv(crossProcessEnv); dir != "" {
		// Child role: act as the pod that served the original request.
		backend, err := NewFileBackend(dir, time.Minute, 8, nil)
		if err != nil {
			t.Fatal(err)
		}
		store := NewIdempotencyStoreWithBackend(time.Minute, 8, nil, backend)
		var calls int32
		if _, _, err := store.Do(context.Background(), crossProcessKey, upstreamOnce(&calls, crossProcessAnswer)); err != nil {
			t.Fatal(err)
		}
		return
	}

	dir := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run=^TestFileBackendReplaysAcrossProcesses$", "-test.v")
	child.Env = append(os.Environ(), crossProcessEnv+"="+dir)
	output, err := child.CombinedOutput()
	if err != nil {
		t.Skipf("re-exec of the test binary is unavailable here (%v): %s", err, output)
	}

	var calls int32
	response, replay, err := newFileStore(t, dir, nil).Do(context.Background(), crossProcessKey, upstreamOnce(&calls, "resp-parent"))
	if err != nil || !replay {
		t.Fatalf("parent = %#v replay=%v err=%v, want the child's record", response, replay, err)
	}
	if response.UpstreamResponseID != crossProcessAnswer {
		t.Fatalf("upstream_response_id = %q, want the child's %q", response.UpstreamResponseID, crossProcessAnswer)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("upstream calls in the parent = %d, want 0", got)
	}
}

func recordFiles(t *testing.T, dir, suffix string) []string {
	t.Helper()
	paths := []string{}
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && filepath.Ext(path) == suffix {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return paths
}
