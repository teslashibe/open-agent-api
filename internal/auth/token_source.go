package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	DefaultTokenURL = "https://auth.openai.com/oauth/token"
	DefaultClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

	tokenExpirySlack      = 5 * time.Minute
	defaultRefreshTimeout = 5 * time.Second
	refreshFailureBackoff = 30 * time.Second
)

var (
	ErrRefreshTokenMissing = errors.New("codex refresh token unavailable")
	ErrTokenRefreshFailed  = errors.New("codex token refresh failed")
	ErrCredentialChanged   = errors.New("credential changed during token refresh")
	ErrCredentialPersist   = errors.New("persist refreshed codex credentials")
)

// TokenSourceConfig exposes only the seams needed by the Codex client and
// deterministic tests. ClientID and TokenURL default to the values used by the
// Codex CLI's public OAuth client.
type TokenSourceConfig struct {
	Path       string
	HTTPClient *http.Client
	Now        func() time.Time
	TokenURL   string
	ClientID   string
}

// TokenSource serializes refresh-token rotation for one auth.json. Refresh
// tokens are one-use credentials, so every caller must share this source.
type TokenSource struct {
	path       string
	httpClient *http.Client
	now        func() time.Time
	tokenURL   string
	clientID   string

	mu                sync.Mutex
	failedRevision    [sha256.Size]byte
	failedAt          time.Time
	failedErr         error
	hasFailedRevision bool
}

func NewTokenSource(cfg TokenSourceConfig) *TokenSource {
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultRefreshTimeout}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	tokenURL := cfg.TokenURL
	if tokenURL == "" {
		tokenURL = DefaultTokenURL
	}
	clientID := cfg.ClientID
	if clientID == "" {
		clientID = DefaultClientID
	}
	return &TokenSource{
		path:       cfg.Path,
		httpClient: httpClient,
		now:        now,
		tokenURL:   tokenURL,
		clientID:   clientID,
	}
}

// Credentials returns a usable credential revision, proactively refreshing a
// JWT that is expired or within the Codex CLI's five-minute refresh window.
func (t *TokenSource) Credentials(ctx context.Context) (Credentials, [sha256.Size]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	creds, revision, original, err := loadWithData(t.path)
	if err != nil {
		return Credentials{}, revision, err
	}
	if err := t.cachedFailure(revision); err != nil {
		return Credentials{}, revision, err
	}
	if !creds.expired(t.now()) {
		return creds, revision, nil
	}
	return t.refreshLocked(ctx, creds, revision, original)
}

// ForceRefresh refreshes after a WebSocket 401/403. If another caller or an
// operator already replaced the attempted revision, the newer usable revision
// wins and its refresh token is not consumed again.
func (t *TokenSource) ForceRefresh(ctx context.Context, attempted [sha256.Size]byte) (Credentials, [sha256.Size]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	creds, revision, original, err := loadWithData(t.path)
	if err != nil {
		return Credentials{}, revision, err
	}
	if err := t.cachedFailure(revision); err != nil {
		return Credentials{}, revision, err
	}
	if revision != attempted && !creds.expired(t.now()) {
		return creds, revision, nil
	}
	return t.refreshLocked(ctx, creds, revision, original)
}

func (c Credentials) expired(now time.Time) bool {
	return !c.Expiry.IsZero() && !now.Add(tokenExpirySlack).Before(c.Expiry)
}

func loadWithData(path string) (Credentials, [sha256.Size]byte, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Credentials{}, [sha256.Size]byte{}, nil, ErrCredentialUnreadable
	}
	revision := sha256.Sum256(data)
	creds, err := Parse(data)
	if err != nil {
		return Credentials{}, revision, nil, err
	}
	return creds, revision, data, nil
}

func (t *TokenSource) refreshLocked(ctx context.Context, creds Credentials, revision [sha256.Size]byte, original []byte) (Credentials, [sha256.Size]byte, error) {
	if creds.RefreshToken == "" {
		return t.recordFailure(revision, fmt.Errorf("%w: %w", ErrTokenRefreshFailed, ErrRefreshTokenMissing))
	}

	body, err := json.Marshal(struct {
		ClientID     string `json:"client_id"`
		GrantType    string `json:"grant_type"`
		RefreshToken string `json:"refresh_token"`
	}{
		ClientID:     t.clientID,
		GrantType:    "refresh_token",
		RefreshToken: creds.RefreshToken,
	})
	if err != nil {
		return t.recordFailure(revision, fmt.Errorf("%w: encode request", ErrTokenRefreshFailed))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.tokenURL, bytes.NewReader(body))
	if err != nil {
		return t.recordFailure(revision, fmt.Errorf("%w: build request", ErrTokenRefreshFailed))
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Credentials{}, revision, ctx.Err()
		}
		return t.recordFailure(revision, fmt.Errorf("%w: request failed", ErrTokenRefreshFailed))
	}
	defer resp.Body.Close()

	var refreshed struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	if err := decoder.Decode(&refreshed); err != nil {
		return t.recordFailure(revision, fmt.Errorf("%w: invalid response", ErrTokenRefreshFailed))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || refreshed.AccessToken == "" {
		return t.recordFailure(revision, fmt.Errorf("%w: status %d", ErrTokenRefreshFailed, resp.StatusCode))
	}

	current, currentRevision, _, err := loadWithData(t.path)
	if err != nil {
		return t.recordFailure(revision, err)
	}
	if currentRevision != revision {
		if !current.expired(t.now()) {
			return current, currentRevision, nil
		}
		return t.recordFailure(currentRevision, ErrCredentialChanged)
	}

	updated, err := updateCredentialJSON(original, refreshed.AccessToken, refreshed.RefreshToken, refreshed.IDToken, t.now())
	if err != nil {
		return t.recordFailure(revision, fmt.Errorf("%w: encode", ErrCredentialPersist))
	}
	if err := atomicWriteFile(t.path, updated, 0o600); err != nil {
		return t.recordFailure(revision, ErrCredentialPersist)
	}
	parsed, err := Parse(updated)
	if err != nil {
		return t.recordFailure(revision, ErrCredentialPersist)
	}
	t.clearFailure()
	return parsed, sha256.Sum256(updated), nil
}

func (t *TokenSource) cachedFailure(revision [sha256.Size]byte) error {
	if !t.hasFailedRevision {
		return nil
	}
	if revision != t.failedRevision || !t.now().Before(t.failedAt.Add(refreshFailureBackoff)) {
		t.clearFailure()
		return nil
	}
	return t.failedErr
}

func (t *TokenSource) recordFailure(revision [sha256.Size]byte, err error) (Credentials, [sha256.Size]byte, error) {
	t.failedRevision = revision
	t.failedAt = t.now()
	t.failedErr = err
	t.hasFailedRevision = true
	return Credentials{}, revision, err
}

func (t *TokenSource) clearFailure() {
	t.failedRevision = [sha256.Size]byte{}
	t.failedAt = time.Time{}
	t.failedErr = nil
	t.hasFailedRevision = false
}

func updateCredentialJSON(original []byte, accessToken, refreshToken, idToken string, now time.Time) ([]byte, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(original, &raw); err != nil {
		return nil, err
	}
	var rawTokens map[string]json.RawMessage
	if err := json.Unmarshal(raw["tokens"], &rawTokens); err != nil {
		return nil, err
	}
	rawTokens["access_token"], _ = json.Marshal(accessToken)
	if refreshToken != "" {
		rawTokens["refresh_token"], _ = json.Marshal(refreshToken)
	}
	if idToken != "" {
		rawTokens["id_token"], _ = json.Marshal(idToken)
	}
	tokensJSON, err := json.Marshal(rawTokens)
	if err != nil {
		return nil, err
	}
	raw["tokens"] = tokensJSON
	raw["last_refresh"], _ = json.Marshal(now.UTC().Format(time.RFC3339Nano))
	return json.Marshal(raw)
}

func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".auth.json-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	keep = true
	if dirHandle, err := os.Open(dir); err == nil {
		_ = dirHandle.Sync()
		_ = dirHandle.Close()
	}
	return nil
}
