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
	tokenExpirySlack      = 60 * time.Second
	tokenResponseMaxBytes = 1 << 20
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

	mu          sync.Mutex
	cache       Credentials // Last credentials returned, never used instead of reading disk.
	replaceFile func(string, string) error
}

func NewSource(path string) *Source {
	return &Source{
		path:        absoluteAuthPath(path),
		httpClient:  http.DefaultClient,
		now:         time.Now,
		tokenURL:    chatgptOAuthTokenURL,
		clientID:    chatgptOAuthClientID,
		replaceFile: os.Rename,
	}
}

// Get returns usable Codex credentials, refreshing when the access token is
// near expiry. Sources for the same profile serialize refresh and persistence
// within this process. Every call reads disk so removal and replacement take effect.
func (s *Source) Get(ctx context.Context) (Credentials, error) {
	return s.get(ctx, time.Time{})
}

// EnsureValidUntil renews through the same durable profile path as Get, without
// exposing credentials. The caller owns scheduling and exclusivity with work.
func (s *Source) EnsureValidUntil(ctx context.Context, deadline time.Time) error {
	if deadline.IsZero() {
		return errors.New("codex authentication requires a validity deadline")
	}
	_, err := s.get(ctx, deadline)
	return err
}

func (s *Source) get(ctx context.Context, deadline time.Time) (Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cache = Credentials{}
	unlock := lockAuthProfile(s.path)
	defer unlock()
	strict := !deadline.IsZero()
	var checkProfile func() error
	if strict {
		if err := ctx.Err(); err != nil {
			return Credentials{}, err
		}
		if !deadline.After(s.now()) {
			return Credentials{}, errors.New("codex authentication requires a future validity deadline")
		}
		identity, err := inspectAuthProfile(s.path)
		if err != nil {
			return Credentials{}, err
		}
		checkProfile = func() error { return identity.check(s.path) }
	}

	data, err := os.ReadFile(s.path)
	if err != nil {
		return Credentials{}, fmt.Errorf("load codex auth: %w", err)
	}
	creds, err := Parse(data)
	if err != nil {
		return Credentials{}, fmt.Errorf("parse codex auth: %w", err)
	}

	usable := creds.AccessToken != "" && !creds.expired(s.now())
	if strict {
		expiry, ok := knownAuthExpiry(data)
		if !ok {
			return Credentials{}, errors.New("codex credential expiry is unknown or malformed")
		}
		creds.Expiry = expiry
		usable = expiry.After(deadline)
		if err := checkProfile(); err != nil {
			return Credentials{}, err
		}
	}
	if usable {
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
	valid := true
	if strict {
		expiry, ok := knownTokenExpiry(refreshed.AccessToken, refreshed.Expiry)
		valid = ok && expiry.After(deadline)
		if ok {
			refreshed.Expiry = expiry
		}
	}
	if err := s.persistChecked(refreshed, data, checkProfile); err != nil {
		return Credentials{}, err
	}
	if strict {
		// A successful OAuth exchange may have rotated the refresh token even
		// when its new access token cannot cover the requested lifetime. Keep
		// that rotation durable, but never admit or cache unusable credentials.
		if err := ctx.Err(); err != nil {
			return Credentials{}, err
		}
		if !valid || !deadline.After(s.now()) {
			return Credentials{}, errors.New("renewed codex credentials do not cover the validity deadline")
		}
	}
	s.cache = refreshed
	return refreshed, nil
}

// ForceRefresh renews the rejected credentials after a websocket 401/403.
// If another Source already persisted different usable credentials, reuse them
// instead of rotating the same profile again.
func (s *Source) ForceRefresh(ctx context.Context) (Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	previous := s.cache
	s.cache = Credentials{}
	unlock := lockAuthProfile(s.path)
	defer unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		return Credentials{}, fmt.Errorf("load codex auth: %w", err)
	}
	creds, err := Parse(data)
	if err != nil {
		return Credentials{}, fmt.Errorf("parse codex auth: %w", err)
	}
	if previous.AccessToken != "" && creds != previous && !creds.expired(s.now()) {
		s.cache = creds
		return creds, nil
	}

	if creds.RefreshToken == "" {
		return Credentials{}, errors.New("codex refresh_token missing; run codex login")
	}
	refreshed, err := s.refresh(ctx, creds)
	if err != nil {
		s.cache = Credentials{}
		return Credentials{}, err
	}
	if err := s.persist(refreshed, data); err != nil {
		return Credentials{}, err
	}
	s.cache = refreshed
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

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, tokenResponseMaxBytes+1))
	if err != nil {
		return Credentials{}, fmt.Errorf("codex token refresh failed: status %d reason unreadable_response", resp.StatusCode)
	}

	if len(bodyBytes) > tokenResponseMaxBytes {
		return Credentials{}, fmt.Errorf("codex token refresh failed: status %d reason oversized_response", resp.StatusCode)
	}

	var body struct {
		AccessToken  string          `json:"access_token"`
		RefreshToken string          `json:"refresh_token"`
		IDToken      string          `json:"id_token"`
		ExpiresIn    int64           `json:"expires_in"`
		Error        json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		return Credentials{}, fmt.Errorf("codex token refresh failed: status %d reason invalid_response", resp.StatusCode)
	}
	hasError := len(body.Error) > 0 && string(body.Error) != "null"
	if resp.StatusCode == http.StatusOK && body.AccessToken == "" && !hasError {
		return Credentials{}, errors.New("codex token refresh failed: status 200 reason missing_access_token")
	}
	if resp.StatusCode != http.StatusOK || hasError {
		return Credentials{}, fmt.Errorf("codex token refresh failed: status %d reason %s", resp.StatusCode, refreshErrorReason(body.Error))
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

// refreshErrorReason accepts both OAuth string errors and structured provider
// errors. Never return descriptions or unknown codes: provider text may contain
// account details or credentials and this error propagates into application logs.
func refreshErrorReason(raw json.RawMessage) string {
	var code string
	if json.Unmarshal(raw, &code) != nil {
		var object struct {
			Code string `json:"code"`
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &object) != nil {
			return "unknown_error"
		}
		code = object.Code
		if code == "" {
			code = object.Type
		}
	}
	switch code {
	case "invalid_grant", "invalid_client", "invalid_request", "unauthorized_client",
		"unsupported_grant_type", "invalid_scope", "access_denied", "server_error",
		"temporarily_unavailable", "refresh_token_reused", "refresh_token_expired",
		"refresh_token_invalidated", "invalid_refresh_token":
		return code
	default:
		return "unknown_error"
	}
}

// persist writes refreshed tokens back into auth.json, preserving unknown
// fields. Credentials are returned only after the replacement succeeds.
func (s *Source) persist(creds Credentials, original []byte) error {
	return s.persistChecked(creds, original, nil)
}

func (s *Source) persistChecked(creds Credentials, original []byte, checkProfile func() error) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(original, &raw); err != nil {
		return errors.New("persist codex auth: invalid original JSON")
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
		return errors.New("persist codex auth: encode tokens failed")
	}
	raw["tokens"] = tokensRaw
	raw["last_refresh"], _ = json.Marshal(s.now().UTC().Format(time.RFC3339Nano))

	updated, err := json.Marshal(raw)
	if err != nil {
		return errors.New("persist codex auth: encode auth failed")
	}
	// Keep the file pretty enough for operators; ignore indent errors.
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, updated, "", "  "); err == nil {
		updated = append(pretty.Bytes(), '\n')
	}
	return replaceAuthFileChecked(s.path, original, updated, s.replaceFile, checkProfile)
}
