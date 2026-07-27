package structured

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// DefaultIdempotencyEntries bounds the store. It caps both the in-process layer
// and, when one is configured, the durable backend behind it.
const DefaultIdempotencyEntries = 1024

// DefaultIdempotencyReservationTTL bounds a cross-process reservation. It
// mirrors the default structured max deadline: a pod that dies mid-request
// cannot wedge a key for longer than one request could ever have taken.
const DefaultIdempotencyReservationTTL = 5 * time.Minute

// idempotencyPollInterval is the first delay a waiter takes before re-checking
// a reservation held by another process. It matches the agent queue's
// distributed lock loop.
const idempotencyPollInterval = 10 * time.Millisecond

// idempotencyMaxPollInterval bounds the backoff. A duplicate waiting on a peer
// polls a shared filesystem, so a long upstream call must not turn into
// hundreds of stats per second per waiter; a quarter second stays far below
// STRUCTURED_MAX_DEADLINE while costing at most one extra poll of latency.
const idempotencyMaxPollInterval = 250 * time.Millisecond

// nextPollInterval doubles the wait and clamps it to the ceiling.
func nextPollInterval(current time.Duration) time.Duration {
	if current < idempotencyPollInterval {
		return idempotencyPollInterval
	}
	next := current * 2
	if next > idempotencyMaxPollInterval {
		return idempotencyMaxPollInterval
	}
	return next
}

// Idempotency outcome labels. They are a closed set so the metric that consumes
// them stays bounded.
const (
	// IdempotencyLocalHit is a replay served from this process's own memory.
	IdempotencyLocalHit = "local_hit"
	// IdempotencyStoreHit is a replay served from the durable backend — the
	// cross-pod and post-restart case.
	IdempotencyStoreHit = "store_hit"
	// IdempotencyMiss is a key that had to run upstream.
	IdempotencyMiss = "miss"
	// IdempotencyBackendError is a backend read/write that failed. It is
	// additive: the call still reports its own terminal outcome, because a
	// backend fault degrades to running fn and never becomes a new error class.
	IdempotencyBackendError = "backend_error"
)

// IdempotencyRecordVersion is the current durable record format. A record
// written with any other version is treated as a miss, so a rollback never
// replays a body it cannot fully understand.
const IdempotencyRecordVersion = 1

// IdempotencyRecord is the durable form of a stored success. Version guards the
// on-disk format so an old pod reading a newer record treats it as a miss
// instead of replaying a half-understood body.
type IdempotencyRecord struct {
	Version  int       `json:"version"`
	Response Response  `json:"response"`
	Stored   time.Time `json:"stored"`
	Expires  time.Time `json:"expires"`
}

// IdempotencyBackend is the durable layer behind the process-local store. It is
// deliberately small so a filesystem, and later a database or Redis, can satisfy
// it without changing IdempotencyStore.
//
// Contract:
//   - Load returns ok=false for a missing, corrupt, or expired record. A
//     corrupt record is a miss, never an error, so one bad file cannot wedge a
//     key.
//   - Store persists a success atomically: a reader either sees the whole
//     record or no record.
//   - Reserve is a best-effort cross-process single-flight. acquired=false means
//     another process is already running this key; the caller waits and retries.
//     release(committed) is safe to call exactly once and must not remove a
//     reservation this process no longer owns.
type IdempotencyBackend interface {
	Load(key string) (IdempotencyRecord, bool, error)
	Store(key string, record IdempotencyRecord) error
	Reserve(key string, until time.Time) (release func(committed bool), acquired bool, err error)
}

// KeyParts are the five scoping dimensions plus the caller-supplied key. Two
// requests share a stored response only when every dimension matches (AC6).
type KeyParts struct {
	// Caller is the tenant identity (tenant header, else hashed Authorization).
	Caller string
	// Operation is the caller-declared logical operation.
	Operation string
	// InputChecksum is a checksum of the canonicalized input.
	InputChecksum string
	// SchemaVersion is the caller's schema version.
	SchemaVersion string
	// ModelPolicyVersion is Policy.Version() at admission time.
	ModelPolicyVersion string
	// IdempotencyKey is the caller-supplied retry identity.
	IdempotencyKey string
}

// Key derives the storage key. Every component is length-prefixed so no
// concatenation of two components can be confused for another pair.
func (p KeyParts) Key() string {
	hash := sha256.New()
	for _, part := range []string{
		p.Caller,
		p.Operation,
		p.InputChecksum,
		p.SchemaVersion,
		p.ModelPolicyVersion,
		p.IdempotencyKey,
	} {
		_, _ = hash.Write([]byte(itoa(len(part))))
		_, _ = hash.Write([]byte{':'})
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0x1e})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// InputChecksum canonicalizes and hashes the request input.
func InputChecksum(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := [20]byte{}
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[index:])
}

type idempotencyEntry struct {
	ready    chan struct{}
	response Response
	err      error
	expires  time.Time
}

// IdempotencyStore is a bounded, TTL'd, single-flight response cache. Only
// successes are stored: a failed attempt must be retryable.
//
// With a backend attached the in-process map becomes a cache in front of a
// durable store, so a replay survives a restart and is shared by every replica
// pointed at the same backend.
type IdempotencyStore struct {
	ttl            time.Duration
	maxEntries     int
	now            func() time.Time
	backend        IdempotencyBackend
	reservationTTL time.Duration

	mu      sync.Mutex
	entries map[string]*idempotencyEntry
	order   []string

	observeMu sync.RWMutex
	observe   func(result string)
}

// NewIdempotencyStore builds a memory-only store. A non-positive ttl or
// maxEntries falls back to the defaults; now defaults to time.Now.
func NewIdempotencyStore(ttl time.Duration, maxEntries int, now func() time.Time) *IdempotencyStore {
	return NewIdempotencyStoreWithBackend(ttl, maxEntries, now, nil)
}

// NewIdempotencyStoreWithBackend builds a store that persists successes through
// backend. A nil backend is exactly NewIdempotencyStore.
func NewIdempotencyStoreWithBackend(ttl time.Duration, maxEntries int, now func() time.Time, backend IdempotencyBackend) *IdempotencyStore {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	if maxEntries <= 0 {
		maxEntries = DefaultIdempotencyEntries
	}
	if now == nil {
		now = time.Now
	}
	return &IdempotencyStore{
		ttl:            ttl,
		maxEntries:     maxEntries,
		now:            now,
		backend:        backend,
		reservationTTL: DefaultIdempotencyReservationTTL,
		entries:        map[string]*idempotencyEntry{},
	}
}

// WithReservationTTL bounds how long this store's cross-process reservations
// stay valid. Callers set it from the maximum request deadline so a crashed pod
// cannot hold a key for longer than a request could have run.
func (s *IdempotencyStore) WithReservationTTL(ttl time.Duration) *IdempotencyStore {
	if s != nil && ttl > 0 {
		s.reservationTTL = ttl
	}
	return s
}

// WithObserver registers the idempotency outcome recorder. It is separate from
// the constructor so internal/structured stays independent of the metrics
// package.
func (s *IdempotencyStore) WithObserver(observe func(result string)) *IdempotencyStore {
	if s == nil {
		return s
	}
	s.observeMu.Lock()
	s.observe = observe
	s.observeMu.Unlock()
	return s
}

func (s *IdempotencyStore) observeResult(result string) {
	if s == nil {
		return
	}
	s.observeMu.RLock()
	observe := s.observe
	s.observeMu.RUnlock()
	if observe != nil {
		observe(result)
	}
}

// Do runs fn at most once per live key. A caller that finds a completed entry
// gets the stored response with replay=true; a caller that arrives while fn is
// still running waits for it rather than issuing a second upstream call. With a
// backend configured that guarantee extends across processes: a local miss
// first asks the backend, and only the process holding the backend reservation
// calls upstream.
func (s *IdempotencyStore) Do(ctx context.Context, key string, fn func() (Response, error)) (Response, bool, error) {
	if s == nil || key == "" {
		response, err := fn()
		return response, false, err
	}

	for {
		s.mu.Lock()
		entry, ok := s.entries[key]
		if ok && !s.expiredLocked(entry) {
			s.mu.Unlock()
			select {
			case <-entry.ready:
			case <-ctx.Done():
				return Response{}, false, ctx.Err()
			}
			if entry.err != nil {
				// The in-flight attempt failed and was not stored. Retry this
				// caller's own attempt instead of replaying a failure.
				s.forget(key, entry)
				continue
			}
			s.observeResult(IdempotencyLocalHit)
			return entry.response, true, nil
		}
		if ok {
			s.removeLocked(key)
		}
		entry = &idempotencyEntry{ready: make(chan struct{}), expires: s.now().Add(s.ttl)}
		s.insertLocked(key, entry)
		s.mu.Unlock()

		// Every field written here is published to other goroutines by the
		// close below, which they only observe through entry.ready.
		response, replay, err := s.resolve(ctx, key, entry, fn)
		entry.response = response
		entry.err = err
		close(entry.ready)
		if err != nil {
			s.forget(key, entry)
		}
		return response, replay, err
	}
}

// resolve is the leader path for one key: consult the durable backend, take the
// cross-process reservation, and only then call upstream.
func (s *IdempotencyStore) resolve(ctx context.Context, key string, entry *idempotencyEntry, fn func() (Response, error)) (Response, bool, error) {
	if s.backend == nil {
		response, err := fn()
		s.observeResult(IdempotencyMiss)
		return response, false, err
	}

	replayFrom := func(record IdempotencyRecord) (Response, bool, error) {
		// Hydrate the local entry with the stored expiry so the two layers
		// agree on the TTL.
		entry.expires = record.Expires
		s.observeResult(IdempotencyStoreHit)
		return record.Response, true, nil
	}

	// wait is local to this call: every waiter starts at the floor, so a fast
	// peer is still noticed within 10 ms.
	wait := idempotencyPollInterval
	for {
		// A record written by another pod, or by this pod before a restart.
		if record, ok := s.load(key); ok {
			return replayFrom(record)
		}

		release, acquired, err := s.backend.Reserve(key, s.now().Add(s.reservationTTL))
		if err != nil {
			// A reservation we cannot take is not a failure the caller should
			// see: run without cross-process single-flight and still try to
			// publish the result.
			s.observeResult(IdempotencyBackendError)
			return s.runAndStore(key, func(bool) {}, fn)
		}
		if acquired {
			// The previous owner publishes its record and only then drops the
			// reservation, so re-check before calling upstream: winning the
			// reservation right after that release must still be a replay.
			if record, ok := s.load(key); ok {
				release(false)
				return replayFrom(record)
			}
			return s.runAndStore(key, release, fn)
		}

		// Another process owns this key. Wait for its record rather than
		// issuing a second upstream call; the caller's context bounds the wait
		// so a wedged peer degrades to a timeout, never a hang. The interval
		// backs off toward the ceiling so a long upstream call does not have
		// every duplicate hammering a shared filesystem.
		select {
		case <-ctx.Done():
			return Response{}, false, ctx.Err()
		case <-time.After(wait):
		}
		wait = nextPollInterval(wait)
	}
}

// runAndStore calls upstream once and persists only a success, so a failure
// stays retryable for every replica.
func (s *IdempotencyStore) runAndStore(key string, release func(committed bool), fn func() (Response, error)) (Response, bool, error) {
	response, err := fn()
	if err != nil {
		release(false)
		s.observeResult(IdempotencyMiss)
		return response, false, err
	}
	stored := s.now()
	if storeErr := s.backend.Store(key, IdempotencyRecord{
		Version:  IdempotencyRecordVersion,
		Response: response,
		Stored:   stored,
		Expires:  stored.Add(s.ttl),
	}); storeErr != nil {
		// The response is still correct and still replayable in this process;
		// only the durable copy was lost.
		s.observeResult(IdempotencyBackendError)
	}
	release(true)
	s.observeResult(IdempotencyMiss)
	return response, false, nil
}

// load reads the backend, mapping a backend fault onto a miss.
func (s *IdempotencyStore) load(key string) (IdempotencyRecord, bool) {
	record, ok, err := s.backend.Load(key)
	if err != nil {
		s.observeResult(IdempotencyBackendError)
		return IdempotencyRecord{}, false
	}
	if !ok {
		return IdempotencyRecord{}, false
	}
	if !record.Expires.After(s.now()) {
		return IdempotencyRecord{}, false
	}
	return record, true
}

func (s *IdempotencyStore) forget(key string, entry *idempotencyEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.entries[key]; ok && current == entry {
		s.removeLocked(key)
	}
}

func (s *IdempotencyStore) expiredLocked(entry *idempotencyEntry) bool {
	select {
	case <-entry.ready:
		return !entry.expires.After(s.now())
	default:
		// Still in flight: never treat it as expired, otherwise concurrent
		// duplicates would both call upstream.
		return false
	}
}

func (s *IdempotencyStore) insertLocked(key string, entry *idempotencyEntry) {
	s.entries[key] = entry
	s.order = append(s.order, key)
	for len(s.order) > s.maxEntries {
		oldest := s.order[0]
		s.order = s.order[1:]
		if oldest != key {
			delete(s.entries, oldest)
		}
	}
}

func (s *IdempotencyStore) removeLocked(key string) {
	delete(s.entries, key)
	for i, queued := range s.order {
		if queued == key {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
}

// Len reports the number of tracked entries. It exists for tests and metrics.
func (s *IdempotencyStore) Len() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}
