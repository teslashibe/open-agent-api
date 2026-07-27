package structured

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// DefaultIdempotencyEntries bounds the process-local store.
const DefaultIdempotencyEntries = 1024

// Idempotency outcome labels. They are a closed set so the metric that consumes
// them stays bounded.
const (
	IdempotencyLocalHit = "local_hit"
	IdempotencyMiss     = "miss"
	IdempotencyConflict = "conflict"
	IdempotencyCapacity = "capacity"
)

// ErrIdempotencyConflict is returned when a live key is reused with a different
// canonical request fingerprint. It is a contract error so the HTTP surface can
// map it straight onto a deterministic 409 without inventing a second
// vocabulary.
var ErrIdempotencyConflict = NewError(
	CodeIdempotencyConflict,
	"idempotency_key was already used with different request parameters",
)

// ErrIdempotencyCapacity is retryable back-pressure. A full store never evicts
// an unexpired binding or an in-flight single-flight merely to admit a new key.
var ErrIdempotencyCapacity = NewError(
	CodeUnavailable,
	"idempotency capacity is full; retry after existing entries expire",
)

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
	ready chan struct{}
	// fingerprint is set once, under s.mu, before the entry is published and is
	// never mutated. It lets an in-process hit — including a waiter on a call
	// that is still in flight — conflict on divergent parameters, so the binding
	fingerprint string
	response    Response
	err         error
	expires     time.Time
}

// IdempotencyStore is a bounded, TTL'd, single-flight response cache. Only
// successes are stored: a failed attempt must be retryable.
type IdempotencyStore struct {
	ttl        time.Duration
	maxEntries int
	now        func() time.Time

	mu      sync.Mutex
	entries map[string]*idempotencyEntry
	order   []string

	observeMu sync.RWMutex
	observe   func(result string)
}

// NewIdempotencyStore builds a memory-only store. A non-positive ttl or
// maxEntries falls back to the defaults; now defaults to time.Now.
func NewIdempotencyStore(ttl time.Duration, maxEntries int, now func() time.Time) *IdempotencyStore {
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
		ttl:        ttl,
		maxEntries: maxEntries,
		now:        now,
		entries:    map[string]*idempotencyEntry{},
	}
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
// fingerprint is the canonical identity of the inference being asked for (see
// Fingerprint). Reusing a live key with a different fingerprint returns
// ErrIdempotencyConflict before any reservation is taken and before fn runs, so
// a mismatched replay costs neither a wrong answer nor a second bill.
func (s *IdempotencyStore) Do(ctx context.Context, key, fingerprint string, fn func() (Response, error)) (Response, bool, error) {
	if s == nil || key == "" {
		response, err := fn()
		return response, false, err
	}

	for {
		s.mu.Lock()
		entry, ok := s.entries[key]
		if ok && !s.expiredLocked(entry) {
			stored := entry.fingerprint
			s.mu.Unlock()
			// Check before waiting: a duplicate with divergent parameters must
			// conflict rather than join the single-flight and be handed an
			// answer to a different question.
			if stored != fingerprint {
				s.observeResult(IdempotencyConflict)
				return Response{}, false, ErrIdempotencyConflict
			}
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
		s.removeExpiredLocked()
		if len(s.entries) >= s.maxEntries {
			s.mu.Unlock()
			s.observeResult(IdempotencyCapacity)
			return Response{}, false, ErrIdempotencyCapacity
		}
		entry = &idempotencyEntry{ready: make(chan struct{}), fingerprint: fingerprint, expires: s.now().Add(s.ttl)}
		s.insertLocked(key, entry)
		s.mu.Unlock()

		response, err := fn()
		s.observeResult(IdempotencyMiss)
		entry.response = response
		entry.err = err
		close(entry.ready)
		if err != nil {
			s.forget(key, entry)
		}
		return response, false, err
	}
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
}

func (s *IdempotencyStore) removeExpiredLocked() {
	for _, key := range append([]string(nil), s.order...) {
		if entry := s.entries[key]; entry != nil && s.expiredLocked(entry) {
			s.removeLocked(key)
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
