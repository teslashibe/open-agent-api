package structured

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// DefaultIdempotencyEntries bounds the in-process store. The store is
// deliberately process-local: a multi-replica deployment replays per pod, not
// cluster-wide. That is documented in docs/issue-116-validation.md and must be
// revisited before any GitOps rollout assumes global semantics.
const DefaultIdempotencyEntries = 1024

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
type IdempotencyStore struct {
	ttl        time.Duration
	maxEntries int
	now        func() time.Time

	mu      sync.Mutex
	entries map[string]*idempotencyEntry
	order   []string
}

// NewIdempotencyStore builds a store. A non-positive ttl or maxEntries falls
// back to the defaults; now defaults to time.Now.
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

// Do runs fn at most once per live key. A caller that finds a completed entry
// gets the stored response with replay=true; a caller that arrives while fn is
// still running waits for it rather than issuing a second upstream call.
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
			return entry.response, true, nil
		}
		if ok {
			s.removeLocked(key)
		}
		entry = &idempotencyEntry{ready: make(chan struct{}), expires: s.now().Add(s.ttl)}
		s.insertLocked(key, entry)
		s.mu.Unlock()

		response, err := fn()
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
