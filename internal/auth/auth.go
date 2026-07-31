package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// ChatGPT/Codex CLI public OAuth client. Identifies the app, not the user —
// same value embedded in the Codex CLI binary / id_token aud.
const (
	chatgptOAuthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	chatgptOAuthTokenURL = "https://auth.openai.com/oauth/token"

	// Refresh a little early so in-flight requests don't race expiry.
	tokenExpirySlack = 60 * time.Second
)

// Credentials are the fields required to dial the Codex websocket.
type Credentials struct {
	AccessToken  string
	AccountID    string
	RefreshToken string
	Expiry       time.Time
}

func (c Credentials) expired(now time.Time) bool {
	if c.Expiry.IsZero() {
		return false
	}
	return !now.Add(tokenExpirySlack).Before(c.Expiry)
}

type authFile struct {
	Tokens *tokens `json:"tokens"`
}

type tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id"`
	ExpiresAt    int64  `json:"expires_at"`
	IDToken      string `json:"id_token,omitempty"`
}

func Load(path string) (Credentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Credentials{}, fmt.Errorf("load codex auth: %w", err)
	}
	creds, err := Parse(data)
	if err != nil {
		return Credentials{}, fmt.Errorf("parse codex auth: %w", err)
	}
	return creds, nil
}

func Parse(data []byte) (Credentials, error) {
	var file authFile
	if err := json.Unmarshal(data, &file); err != nil {
		return Credentials{}, fmt.Errorf("invalid auth JSON: %w", err)
	}
	if file.Tokens == nil {
		return Credentials{}, errors.New("missing tokens")
	}
	if file.Tokens.AccessToken == "" {
		return Credentials{}, errors.New("missing tokens.access_token")
	}
	if file.Tokens.AccountID == "" {
		return Credentials{}, errors.New("missing tokens.account_id")
	}
	creds := Credentials{
		AccessToken:  file.Tokens.AccessToken,
		AccountID:    file.Tokens.AccountID,
		RefreshToken: file.Tokens.RefreshToken,
	}
	if file.Tokens.ExpiresAt > 0 {
		creds.Expiry = time.Unix(file.Tokens.ExpiresAt, 0)
	} else if exp, ok := jwtExpiry(file.Tokens.AccessToken); ok {
		creds.Expiry = exp
	}
	return creds, nil
}

func jwtExpiry(accessToken string) (time.Time, bool) {
	parts := strings.Split(accessToken, ".")
	if len(parts) < 2 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp <= 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.Exp, 0), true
}

// Source loads ~/.codex/auth.json, refreshes expired ChatGPT OAuth access
// tokens, and persists rotated credentials back to disk (Gemini parity).
type Source struct {
	path       string
	httpClient *http.Client
	now        func() time.Time
	tokenURL   string
	clientID   string

	mu    sync.Mutex
	cache Credentials
}

func NewSource(path string) *Source {
	return &Source{
		path:       path,
		httpClient: http.DefaultClient,
		now:        time.Now,
		tokenURL:   chatgptOAuthTokenURL,
		clientID:   chatgptOAuthClientID,
	}
}

// Get returns usable Codex credentials, refreshing when the access token is
// near expiry. Single-flight via the mutex.
func (s *Source) Get(ctx context.Context) (Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cache.AccessToken != "" && s.cache.AccountID != "" && !s.cache.expired(s.now()) {
		return s.cache, nil
	}

	data, err := os.ReadFile(s.path)
	if err != nil {
		return Credentials{}, fmt.Errorf("load codex auth: %w", err)
	}
	creds, err := Parse(data)
	if err != nil {
		return Credentials{}, fmt.Errorf("parse codex auth: %w", err)
	}

	if creds.AccessToken != "" && !creds.expired(s.now()) {
		s.cache = creds
		return creds, nil
	}
	if creds.RefreshToken == "" {
		return Credentials{}, errors.New("codex access token expired and no refresh_token available")
	}

	refreshed, err := s.refresh(ctx, creds)
	if err != nil {
		return Credentials{}, err
	}
	s.cache = refreshed
	s.persist(refreshed, data)
	return refreshed, nil
}

// ForceRefresh clears the cache and always hits the token endpoint. Used after
// an upstream websocket 401/403 when the access token still looks unexpired.
func (s *Source) ForceRefresh(ctx context.Context) (Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		return Credentials{}, fmt.Errorf("load codex auth: %w", err)
	}
	creds, err := Parse(data)
	if err != nil {
		return Credentials{}, fmt.Errorf("parse codex auth: %w", err)
	}
	if creds.RefreshToken == "" {
		return Credentials{}, errors.New("codex refresh_token missing; run codex login")
	}
	refreshed, err := s.refresh(ctx, creds)
	if err != nil {
		s.cache = Credentials{}
		return Credentials{}, err
	}
	s.cache = refreshed
	s.persist(refreshed, data)
	return refreshed, nil
}

func (s *Source) refresh(ctx context.Context, creds Credentials) (Credentials, error) {
	form := url.Values{}
	form.Set("client_id", s.clientID)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", creds.RefreshToken)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Credentials{}, fmt.Errorf("build codex token refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return Credentials{}, fmt.Errorf("refresh codex token: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Credentials{}, fmt.Errorf("read codex token refresh response: %w", err)
	}

	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		return Credentials{}, fmt.Errorf("decode codex token refresh response: %w", err)
	}
	if resp.StatusCode != http.StatusOK || body.AccessToken == "" {
		message := body.ErrorDesc
		if message == "" {
			message = body.Error
		}
		if message == "" {
			message = fmt.Sprintf("status %d", resp.StatusCode)
		}
		return Credentials{}, fmt.Errorf("codex token refresh failed: %s", message)
	}

	refreshed := creds
	refreshed.AccessToken = body.AccessToken
	if body.RefreshToken != "" {
		refreshed.RefreshToken = body.RefreshToken
	}
	if body.ExpiresIn > 0 {
		refreshed.Expiry = s.now().Add(time.Duration(body.ExpiresIn) * time.Second)
	} else if exp, ok := jwtExpiry(body.AccessToken); ok {
		refreshed.Expiry = exp
	}
	return refreshed, nil
}

// persist writes refreshed tokens back into auth.json, preserving unknown
// fields. Failures are non-fatal: the in-memory cache still carries the token.
func (s *Source) persist(creds Credentials, original []byte) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(original, &raw); err != nil {
		return
	}

	var tok map[string]json.RawMessage
	if err := json.Unmarshal(raw["tokens"], &tok); err != nil || tok == nil {
		tok = map[string]json.RawMessage{}
	}
	tok["access_token"], _ = json.Marshal(creds.AccessToken)
	tok["account_id"], _ = json.Marshal(creds.AccountID)
	if creds.RefreshToken != "" {
		tok["refresh_token"], _ = json.Marshal(creds.RefreshToken)
	}
	if !creds.Expiry.IsZero() {
		tok["expires_at"], _ = json.Marshal(creds.Expiry.Unix())
	}
	tokensRaw, err := json.Marshal(tok)
	if err != nil {
		return
	}
	raw["tokens"] = tokensRaw
	raw["last_refresh"], _ = json.Marshal(s.now().UTC().Format(time.RFC3339Nano))

	updated, err := json.Marshal(raw)
	if err != nil {
		return
	}
	// Keep the file pretty enough for operators; ignore indent errors.
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, updated, "", "  "); err == nil {
		updated = append(pretty.Bytes(), '\n')
	}
	_ = os.WriteFile(s.path, updated, 0o600)
}
