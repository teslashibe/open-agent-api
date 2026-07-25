package reportstudio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
)

var ErrIdempotencyConflict = errors.New("idempotency key reused with different request scope")
var ErrIdempotencyStoreFull = errors.New("idempotency store is full")

type Store struct {
	mu       sync.Mutex
	entries  map[string]*storeEntry
	ttl      time.Duration
	capacity int
	now      func() time.Time
}

type storeEntry struct {
	fingerprint string
	done        chan struct{}
	result      Success
	ready       bool
	err         error
	expires     time.Time
}

func NewStore(ttl time.Duration, capacity int) *Store {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	if capacity < 1 {
		capacity = 1
	}
	return &Store{entries: map[string]*storeEntry{}, ttl: ttl, capacity: capacity, now: time.Now}
}

func (s *Store) Execute(
	ctx context.Context,
	caller, idempotencyKey string,
	scope Scope,
	fn func() (Success, error),
) (Success, bool, error) {
	if err := ctx.Err(); err != nil {
		return Success{}, false, err
	}
	token := digest(caller, idempotencyKey)
	fingerprint := ScopeFingerprint(scope)
	for {
		s.mu.Lock()
		s.evictExpiredLocked()
		if entry, ok := s.entries[token]; ok {
			if entry.fingerprint != fingerprint {
				s.mu.Unlock()
				return Success{}, false, ErrIdempotencyConflict
			}
			if entry.ready {
				result := entry.result
				err := entry.err
				s.mu.Unlock()
				if err != nil {
					return Success{}, true, err
				}
				result.Identity.Replayed = true
				return result, true, nil
			}
			done := entry.done
			s.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return Success{}, false, ctx.Err()
			}
		}
		if len(s.entries) >= s.capacity {
			s.evictOldestReadyLocked()
			if len(s.entries) >= s.capacity {
				s.mu.Unlock()
				return Success{}, false, ErrIdempotencyStoreFull
			}
		}
		entry := &storeEntry{fingerprint: fingerprint, done: make(chan struct{})}
		s.entries[token] = entry
		s.mu.Unlock()

		result, err := fn()
		s.mu.Lock()
		if err == nil {
			entry.result = result
			entry.expires = s.now().Add(s.ttl)
		} else {
			entry.err = err
			entry.expires = s.now().Add(time.Second)
		}
		entry.ready = true
		close(entry.done)
		s.mu.Unlock()
		return result, false, err
	}
}

func ScopeFingerprint(scope Scope) string {
	return digest(
		scope.Caller,
		scope.Operation,
		scope.InputChecksum,
		scope.SchemaVersion,
		scope.SchemaChecksum,
		scope.ModelPolicyVersion,
		scope.Model,
	)
}

func ResponseID(scope Scope, idempotencyKey string) string {
	return "rsi-" + digest(ScopeFingerprint(scope), idempotencyKey)[:32]
}

func Checksum(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func digest(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func (s *Store) evictExpiredLocked() {
	now := s.now()
	for key, entry := range s.entries {
		if entry.ready && !entry.expires.After(now) {
			delete(s.entries, key)
		}
	}
}

func (s *Store) evictOldestReadyLocked() {
	var oldestKey string
	var oldest time.Time
	for key, entry := range s.entries {
		if !entry.ready {
			continue
		}
		if oldestKey == "" || entry.expires.Before(oldest) {
			oldestKey = key
			oldest = entry.expires
		}
	}
	if oldestKey != "" {
		delete(s.entries, oldestKey)
	}
}
