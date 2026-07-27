package structured

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// FileBackend persists idempotency records on a filesystem, so replay survives
// a restart and is shared by every replica mounting the same directory (a
// ReadWriteMany PVC in Kubernetes, a bind mount under Docker).
//
// It is deliberately dependency-free: no Redis, no database driver. The
// trade-off is documented in docs/issue-120-validation.md — replay after a
// completed request is exact, while cross-process single-flight is best-effort
// because a network filesystem can weaken atomic link/rename semantics.
type FileBackend struct {
	dir        string
	ttl        time.Duration
	maxEntries int
	now        func() time.Time
	owner      string

	reservations atomic.Uint64
	// claimAttempts counts entries into the reservation write path. It is test
	// observability for the held-lock fast path, not a metric: nothing outside
	// this package reads it.
	claimAttempts atomic.Uint64

	sweepMu       sync.Mutex
	writes        int
	sweepInterval int
}

const (
	fileRecordSuffix = ".json"
	fileLockSuffix   = ".lock"
	fileTempPattern  = ".tmp-*"
	// fileDirMode keeps records readable only by the gateway user: a record
	// holds the full extracted payload. Files are 0600 via os.CreateTemp.
	fileDirMode = 0o700
)

// fileSafeKey is the shape a key must have to be used verbatim as a filename.
// Keys produced by KeyParts.Key() are hex SHA-256 and always match; anything
// else is hashed first so a hand-built key can never escape the directory.
var fileSafeKey = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// NewFileBackend builds a durable backend rooted at dir. The directory is
// created lazily on first write, so construction cannot fail on a volume that
// is still being mounted.
func NewFileBackend(dir string, ttl time.Duration, maxEntries int, now func() time.Time) (*FileBackend, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, errors.New("structured idempotency dir is required for the file backend")
	}
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	if maxEntries <= 0 {
		maxEntries = DefaultIdempotencyEntries
	}
	if now == nil {
		now = time.Now
	}
	// Sweeping on every write is O(entries); sweeping only rarely lets a busy
	// shared volume overshoot the cap. Scale the interval to the cap so the
	// directory never exceeds maxEntries by more than one interval.
	interval := maxEntries / 8
	switch {
	case interval < 1:
		interval = 1
	case interval > 64:
		interval = 64
	}
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		hostname = "unknown"
	}
	return &FileBackend{
		dir:           dir,
		ttl:           ttl,
		maxEntries:    maxEntries,
		now:           now,
		owner:         fmt.Sprintf("%s-%d", hostname, os.Getpid()),
		sweepInterval: interval,
	}, nil
}

// Dir reports the backing directory. It exists for startup logging and tests.
func (b *FileBackend) Dir() string {
	if b == nil {
		return ""
	}
	return b.dir
}

// Load returns the stored record for key. A missing, unreadable, truncated,
// wrong-version, or expired record is a miss, and the bad file is unlinked so
// it cannot wedge the key forever.
func (b *FileBackend) Load(key string) (IdempotencyRecord, bool, error) {
	if b == nil {
		return IdempotencyRecord{}, false, nil
	}
	path := b.recordPath(key)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return IdempotencyRecord{}, false, nil
	}
	if err != nil {
		return IdempotencyRecord{}, false, fmt.Errorf("structured idempotency read: %w", err)
	}
	record, ok := decodeIdempotencyRecord(raw)
	if !ok {
		b.discard(path)
		return IdempotencyRecord{}, false, nil
	}
	if !record.Expires.After(b.now()) {
		b.discard(path)
		return IdempotencyRecord{}, false, nil
	}
	return record, true, nil
}

// Store writes a record atomically: a concurrent reader sees either the whole
// record or no record at all, never a half-written body.
func (b *FileBackend) Store(key string, record IdempotencyRecord) error {
	if b == nil {
		return nil
	}
	if record.Version == 0 {
		record.Version = IdempotencyRecordVersion
	}
	if record.Stored.IsZero() {
		record.Stored = b.now()
	}
	if record.Expires.IsZero() {
		record.Expires = record.Stored.Add(b.ttl)
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("structured idempotency encode: %w", err)
	}

	path := b.recordPath(key)
	shard := filepath.Dir(path)
	if err := os.MkdirAll(shard, fileDirMode); err != nil {
		return fmt.Errorf("structured idempotency dir: %w", err)
	}
	temp, err := os.CreateTemp(shard, fileTempPattern)
	if err != nil {
		return fmt.Errorf("structured idempotency temp: %w", err)
	}
	tempName := temp.Name()
	if err := writeAndSync(temp, raw); err != nil {
		_ = os.Remove(tempName)
		return fmt.Errorf("structured idempotency write: %w", err)
	}
	// os.CreateTemp already opened the file 0600.
	if err := os.Rename(tempName, path); err != nil {
		_ = os.Remove(tempName)
		return fmt.Errorf("structured idempotency publish: %w", err)
	}
	b.maybeSweep()
	return nil
}

func writeAndSync(file *os.File, raw []byte) error {
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return err
	}
	// Sync before the rename so a crashed pod leaves either the old record or
	// the new one, never an empty file under the real name.
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// Reserve claims cross-process single-flight for key. A reservation whose
// stamp has passed is stolen, so a pod that died mid-request cannot wedge the
// key for longer than the reservation window.
func (b *FileBackend) Reserve(key string, until time.Time) (func(committed bool), bool, error) {
	noop := func(bool) {}
	if b == nil {
		return noop, true, nil
	}
	path := b.lockPath(key)
	if err := os.MkdirAll(filepath.Dir(path), fileDirMode); err != nil {
		return noop, false, fmt.Errorf("structured idempotency dir: %w", err)
	}
	if until.IsZero() {
		until = b.now().Add(DefaultIdempotencyReservationTTL)
	}
	token := fmt.Sprintf("%s-%d", b.owner, b.reservations.Add(1))

	// Two attempts: claim, and if a lapsed reservation is in the way, steal it
	// and claim again. A third party winning the steal simply means we wait.
	for attempt := 0; attempt < 2; attempt++ {
		// A duplicate polling a peer's in-flight call is the common case on a
		// shared volume, and it needs no writes at all. Probe first so a held
		// reservation costs one stat instead of a create/write/fsync/link/unlink
		// round trip per poll. The probe is advisory: os.Link inside claimLock
		// stays the only authority, so losing this race is still a clean
		// acquired=false.
		if held, lapsed := b.lockHeld(path); held {
			if !lapsed {
				return noop, false, nil
			}
			_ = os.Remove(path)
		}
		claimed, err := b.claimLock(path, token, until)
		if err != nil {
			return noop, false, fmt.Errorf("structured idempotency reserve: %w", err)
		}
		if claimed {
			var once sync.Once
			return func(bool) {
				once.Do(func() { b.releaseLock(path, token) })
			}, true, nil
		}
		if !b.lockLapsed(path) {
			return noop, false, nil
		}
		_ = os.Remove(path)
	}
	return noop, false, nil
}

// lockHeld is the read-only half of Reserve. exists=false means no reservation
// file was there to begin with, so the caller falls through to claimLock; a
// reservation that exists is only stealable once lockLapsed agrees.
func (b *FileBackend) lockHeld(path string) (exists, lapsed bool) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, false
		}
		// A lock we cannot stat is handled by the claim path, which reports the
		// real error rather than guessing here.
		return false, false
	}
	return true, b.lockLapsed(path)
}

// claimLock publishes a fully written reservation in one atomic step. Creating
// the file and then writing it would let a peer observe an empty lock in
// between and mistake an active reservation for an abandoned one, so the
// content is written to a temp file and linked into place instead.
func (b *FileBackend) claimLock(path, token string, until time.Time) (bool, error) {
	b.claimAttempts.Add(1)
	temp, err := os.CreateTemp(filepath.Dir(path), fileTempPattern)
	if err != nil {
		return false, err
	}
	name := temp.Name()
	defer func() { _ = os.Remove(name) }()
	content := fmt.Sprintf("owner=%s\nexpires=%s\n", token, until.UTC().Format(time.RFC3339Nano))
	if err := writeAndSync(temp, []byte(content)); err != nil {
		return false, err
	}
	if err := os.Link(name, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// releaseLock removes the reservation only while this process still owns it. A
// reservation that was stolen after lapsing belongs to someone else now.
func (b *FileBackend) releaseLock(path, token string) {
	if owner, ok := readLockField(path, "owner="); ok && owner != token {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return
	}
}

// lockLapsed reports whether an existing reservation may be stolen. An
// unreadable or unparseable stamp counts as lapsed: a lock we cannot reason
// about must not block the key indefinitely.
func (b *FileBackend) lockLapsed(path string) bool {
	raw, ok := readLockField(path, "expires=")
	if !ok {
		// A reservation whose stamp cannot be read is only stealable once it
		// is older than any request could have been, so a lock written by a
		// future format is never yanked out from under a live peer.
		info, err := os.Stat(path)
		if err != nil {
			return true
		}
		return b.now().Sub(info.ModTime()) > DefaultIdempotencyReservationTTL
	}
	expires, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return true
	}
	return !expires.After(b.now())
}

func readLockField(path, prefix string) (string, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if value, found := strings.CutPrefix(strings.TrimSpace(line), prefix); found {
			return value, true
		}
	}
	return "", false
}

func decodeIdempotencyRecord(raw []byte) (IdempotencyRecord, bool) {
	var record IdempotencyRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return IdempotencyRecord{}, false
	}
	if record.Version != IdempotencyRecordVersion || record.Expires.IsZero() {
		return IdempotencyRecord{}, false
	}
	return record, true
}

// discard unlinks a record or reservation. A file another replica removed
// first is the same outcome, so the error is deliberately ignored.
func (b *FileBackend) discard(path string) {
	_ = os.Remove(path)
}

// recordPath shards by the first two characters of the key so a busy directory
// never holds every record in one flat listing.
func (b *FileBackend) recordPath(key string) string {
	name := fileKeyName(key)
	return filepath.Join(b.dir, name[:2], name+fileRecordSuffix)
}

func (b *FileBackend) lockPath(key string) string {
	name := fileKeyName(key)
	return filepath.Join(b.dir, name[:2], name+fileLockSuffix)
}

// fileKeyName makes a key safe and long enough to shard. Real keys are already
// hex SHA-256 and pass through untouched.
func fileKeyName(key string) string {
	if len(key) >= 2 && fileSafeKey.MatchString(key) {
		return key
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// maybeSweep bounds the directory on the write path. Doing it here rather than
// on read means a shared volume cannot grow without limit while every caller is
// missing the cache.
func (b *FileBackend) maybeSweep() {
	b.sweepMu.Lock()
	b.writes++
	due := b.writes >= b.sweepInterval
	if due {
		b.writes = 0
	}
	b.sweepMu.Unlock()
	if due {
		b.sweep()
	}
}

type sweepEntry struct {
	path   string
	stored time.Time
}

// sweep drops expired records, lapsed reservations, and stale temp files, then
// trims the oldest records back to maxEntries. Every removal is idempotent, so
// two replicas sweeping the same volume simply agree.
func (b *FileBackend) sweep() {
	now := b.now()
	live := make([]sweepEntry, 0, b.maxEntries)
	_ = filepath.WalkDir(b.dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			// A directory that vanished under a concurrent sweep is fine.
			return nil //nolint:nilerr // a partial sweep is better than none
		}
		switch {
		case strings.HasSuffix(path, fileLockSuffix):
			if b.lockLapsed(path) {
				b.discard(path)
			}
		case strings.HasSuffix(path, fileRecordSuffix):
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil
			}
			record, ok := decodeIdempotencyRecord(raw)
			if !ok || !record.Expires.After(now) {
				b.discard(path)
				return nil
			}
			live = append(live, sweepEntry{path: path, stored: record.Stored})
		default:
			// Temp files are only orphaned by a crash mid-write.
			if info, statErr := entry.Info(); statErr == nil && now.Sub(info.ModTime()) > b.ttl {
				b.discard(path)
			}
		}
		return nil
	})

	if len(live) <= b.maxEntries {
		return
	}
	sort.Slice(live, func(i, j int) bool { return live[i].stored.Before(live[j].stored) })
	for _, entry := range live[:len(live)-b.maxEntries] {
		b.discard(entry.path)
	}
}
