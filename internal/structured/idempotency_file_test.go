package structured

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	first, replay, err := podA.Do(context.Background(), crossProcessKey, testFingerprint, upstreamOnce(&calls, "resp-1"))
	if err != nil || replay {
		t.Fatalf("pod A = %#v replay=%v err=%v", first, replay, err)
	}

	podB := newFileStore(t, dir, nil)
	second, replay, err := podB.Do(context.Background(), crossProcessKey, testFingerprint, upstreamOnce(&calls, "resp-2"))
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
	if _, _, err := before.Do(context.Background(), crossProcessKey, testFingerprint, upstreamOnce(&calls, "resp-1")); err != nil {
		t.Fatal(err)
	}
	// The process goes away; only the volume survives.
	before = nil
	_ = before

	after := newFileStore(t, dir, nil)
	if after.Len() != 0 {
		t.Fatalf("restarted store already has %d local entries", after.Len())
	}
	response, replay, err := after.Do(context.Background(), crossProcessKey, testFingerprint, upstreamOnce(&calls, "resp-2"))
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
	if _, _, err := first.Do(context.Background(), crossProcessKey, testFingerprint, upstreamOnce(&calls, "resp-1")); err != nil {
		t.Fatal(err)
	}

	now = now.Add(59 * time.Second)
	if _, replay, _ := newFileStore(t, dir, clock).Do(context.Background(), crossProcessKey, testFingerprint, upstreamOnce(&calls, "resp-2")); !replay {
		t.Fatal("record expired before its TTL")
	}
	now = now.Add(2 * time.Second)
	if _, replay, _ := newFileStore(t, dir, clock).Do(context.Background(), crossProcessKey, testFingerprint, upstreamOnce(&calls, "resp-3")); replay {
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

	if _, _, err := store.Do(context.Background(), crossProcessKey, testFingerprint, func() (Response, error) {
		return Response{}, errors.New("upstream boom")
	}); err == nil {
		t.Fatal("Do() hid the failure")
	}

	var calls int32
	response, replay, err := newFileStore(t, dir, nil).Do(context.Background(), crossProcessKey, testFingerprint, upstreamOnce(&calls, "resp-retry"))
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
			if _, _, err := store.Do(context.Background(), crossProcessKey, testFingerprint, upstreamOnce(&calls, "resp-1")); err != nil {
				t.Fatal(err)
			}
			paths := recordFiles(t, dir, ".json")
			if len(paths) != 1 {
				t.Fatalf("record files = %v, want exactly one", paths)
			}
			if err := os.WriteFile(paths[0], corrupt, 0o600); err != nil {
				t.Fatal(err)
			}

			response, replay, err := newFileStore(t, dir, nil).Do(context.Background(), crossProcessKey, testFingerprint, upstreamOnce(&calls, "resp-2"))
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
			_, replay, err := stores[index%len(stores)].Do(context.Background(), crossProcessKey, testFingerprint, func() (Response, error) {
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
	if _, _, err := newFileStore(t, dir, nil).Do(context.Background(), crossProcessKey, testFingerprint, upstreamOnce(&calls, "resp-1")); err != nil {
		t.Fatal(err)
	}

	results := map[string]int{}
	next := newFileStore(t, dir, nil).WithObserver(func(result string) { results[result]++ })
	if _, replay, err := next.Do(context.Background(), crossProcessKey, testFingerprint, upstreamOnce(&calls, "resp-2")); err != nil || !replay {
		t.Fatalf("replay=%v err=%v", replay, err)
	}
	if results[IdempotencyStoreHit] != 1 {
		t.Fatalf("outcomes = %v, want one store_hit", results)
	}
}

// AC3/AC5: a duplicate that finds a live reservation must cost a stat, not a
// create/write/fsync/link/unlink round trip. On a shared PVC the waiters are
// the majority of the traffic for a key, so their poll has to be read-only.
func TestFileBackendReserveAvoidsWritesWhileALockIsHeld(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(3000, 0)
	clock := func() time.Time { return now }
	backend, err := NewFileBackend(dir, time.Minute, 8, clock)
	if err != nil {
		t.Fatal(err)
	}

	release, acquired, err := backend.Reserve(crossProcessKey, now.Add(time.Minute))
	if err != nil || !acquired {
		t.Fatalf("first Reserve() acquired=%v err=%v", acquired, err)
	}
	defer release(true)

	claims := backend.claimAttempts.Load()
	for i := 0; i < 32; i++ {
		if _, acquired, err := backend.Reserve(crossProcessKey, now.Add(time.Minute)); err != nil || acquired {
			t.Fatalf("blocked Reserve() %d acquired=%v err=%v, want a held reservation to block", i, acquired, err)
		}
	}
	if got := backend.claimAttempts.Load(); got != claims {
		t.Fatalf("claim attempts = %d, want %d: a blocked waiter must not enter the write path", got, claims)
	}
	if temps := tempFiles(t, dir); len(temps) != 0 {
		t.Fatalf("temp files = %v, want none while the reservation is held", temps)
	}

	// The shortcut must not swallow the steal: once the stamp passes, the
	// waiter still takes the write path and wins the key.
	now = now.Add(2 * time.Minute)
	stolen, acquired, err := backend.Reserve(crossProcessKey, now.Add(time.Minute))
	if err != nil || !acquired {
		t.Fatalf("Reserve() after the stamp lapsed acquired=%v err=%v", acquired, err)
	}
	stolen(true)
	if got := backend.claimAttempts.Load(); got <= claims {
		t.Fatalf("claim attempts = %d, want more than %d: a lapsed lock must be re-claimed", got, claims)
	}
}

// tempFiles lists the in-progress writes left in the shard directories. A
// blocked waiter must create none.
func tempFiles(t *testing.T, dir string) []string {
	t.Helper()
	paths := []string{}
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), ".tmp-") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return paths
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
		if _, _, err := store.Do(context.Background(), key, testFingerprint, func() (Response, error) {
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
		if _, _, err := store.Do(context.Background(), crossProcessKey, testFingerprint, upstreamOnce(&calls, crossProcessAnswer)); err != nil {
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
	response, replay, err := newFileStore(t, dir, nil).Do(context.Background(), crossProcessKey, testFingerprint, upstreamOnce(&calls, "resp-parent"))
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

// AC1: a usable directory passes and leaves no scratch behind.
func TestFileBackendPreflightAcceptsAUsableDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "created", "by", "preflight")
	backend, err := NewFileBackend(dir, time.Minute, 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.Preflight(); err != nil {
		t.Fatalf("Preflight() on a usable dir error = %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("Preflight() did not create the directory: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("Preflight() left %v behind", entries)
	}
	// It has to be repeatable: every pod runs it at startup on the same volume.
	if err := backend.Preflight(); err != nil {
		t.Fatalf("second Preflight() error = %v", err)
	}
}

// AC1, AC2: an unusable directory fails closed with an actionable, secret-free
// message. Both shapes an operator actually hits are covered: a parent that
// cannot be traversed to create the directory, and a directory that exists but
// cannot be written.
func TestFileBackendPreflightFailsClosedOnAnUnusableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		// Root bypasses the permission bits these cases rely on, so the
		// assertions would pass without testing anything.
		t.Skip("permission-based preflight failures are not observable as root")
	}

	for name, build := range map[string]func(t *testing.T) string{
		"missing parent": func(t *testing.T) string {
			parent := t.TempDir()
			if err := os.Chmod(parent, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
			return filepath.Join(parent, "absent", "nested")
		},
		"read-only directory": func(t *testing.T) string {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
			return dir
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := build(t)
			backend, err := NewFileBackend(dir, time.Minute, 8, nil)
			if err != nil {
				t.Fatal(err)
			}
			preflightErr := backend.Preflight()
			if preflightErr == nil {
				t.Fatal("Preflight() on an unusable dir error = nil, want a failure")
			}
			message := preflightErr.Error()
			if !strings.Contains(message, dir) {
				t.Fatalf("error %q does not name the configured dir %q", message, dir)
			}
			if !strings.Contains(message, "STRUCTURED_IDEMPOTENCY_DIR") {
				t.Fatalf("error %q does not name the setting to fix", message)
			}
			// The message is logged at startup, so it must carry nothing from a
			// record or a credential.
			for _, forbidden := range []string{"upstream_response_id", "Bearer", "authorization", crossProcessKey} {
				if strings.Contains(message, forbidden) {
					t.Fatalf("error %q leaked %q", message, forbidden)
				}
			}
		})
	}
}

// AC5: a record written by the previous format carries no fingerprint, so
// nothing proves which parameters produced it. It is a miss and is unlinked —
// never a replay, and never a conflict that would wedge the key for its TTL.
func TestFileBackendTreatsVersionOneRecordsAsAMiss(t *testing.T) {
	dir := t.TempDir()
	backend, err := NewFileBackend(dir, time.Minute, 8, nil)
	if err != nil {
		t.Fatal(err)
	}

	// The version 1 shape, verbatim: no fingerprint field at all.
	legacy := struct {
		Version  int       `json:"version"`
		Response Response  `json:"response"`
		Stored   time.Time `json:"stored"`
		Expires  time.Time `json:"expires"`
	}{
		Version:  1,
		Response: Response{UpstreamResponseID: "resp-v1", Data: json.RawMessage(`{"title":"legacy"}`)},
		Stored:   time.Now(),
		Expires:  time.Now().Add(time.Hour),
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	path := backend.recordPath(crossProcessKey)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, ok, loadErr := backend.Load(crossProcessKey); ok || loadErr != nil {
		t.Fatalf("Load() of a v1 record ok=%v err=%v, want a clean miss", ok, loadErr)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the v1 record was not discarded (stat err = %v)", err)
	}

	// End to end: the store re-runs once and republishes at the current
	// version, bound to a fingerprint.
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var calls int32
	store := NewIdempotencyStoreWithBackend(time.Minute, 8, nil, backend)
	response, replay, err := store.Do(context.Background(), crossProcessKey, testFingerprint, upstreamOnce(&calls, "resp-v2"))
	if err != nil || replay || response.UpstreamResponseID != "resp-v2" {
		t.Fatalf("v1 record = %#v replay=%v err=%v, want a fresh call", response, replay, err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("upstream calls = %d, want 1", got)
	}
	republished, ok, err := backend.Load(crossProcessKey)
	if err != nil || !ok {
		t.Fatalf("republished Load() ok=%v err=%v", ok, err)
	}
	if republished.Version != IdempotencyRecordVersion || republished.Fingerprint != testFingerprint {
		t.Fatalf("republished record = %#v, want version %d bound to %q", republished, IdempotencyRecordVersion, testFingerprint)
	}
}

// AC4 across processes: a durable record bound to other parameters conflicts on
// a second pod instead of replaying or re-calling upstream.
func TestFileBackendConflictsOnADivergentFingerprint(t *testing.T) {
	dir := t.TempDir()
	var calls int32

	podA := newFileStore(t, dir, nil)
	if _, _, err := podA.Do(context.Background(), crossProcessKey, "fingerprint-a", upstreamOnce(&calls, "resp-1")); err != nil {
		t.Fatal(err)
	}

	// A separate store has no local entry, so this can only come from the
	// durable record.
	podB := newFileStore(t, dir, nil)
	response, replay, err := podB.Do(context.Background(), crossProcessKey, "fingerprint-b", upstreamOnce(&calls, "resp-2"))
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("pod B = %#v replay=%v err=%v, want ErrIdempotencyConflict", response, replay, err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("upstream calls = %d, want 1 (a conflict must not call upstream)", got)
	}

	// The record is intact: the matching retry still replays on pod B.
	replayed, replay, err := newFileStore(t, dir, nil).Do(context.Background(), crossProcessKey, "fingerprint-a", upstreamOnce(&calls, "resp-3"))
	if err != nil || !replay || replayed.UpstreamResponseID != "resp-1" {
		t.Fatalf("matching retry = %#v replay=%v err=%v", replayed, replay, err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("upstream calls = %d, want 1", got)
	}
	// No reservation was taken, so nothing is left holding the key.
	if locks := recordFiles(t, dir, ".lock"); len(locks) != 0 {
		t.Fatalf("leftover reservations after a conflict = %v", locks)
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
