package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// OAuth clients used by Google's consumer CLIs. These values are public and
// shipped inside the binaries; they identify the app, not the user.
const (
	geminiCLIOAuthClientID     = "681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com"
	geminiCLIOAuthClientSecret = "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"

	// Antigravity CLI (agy) OAuth client — required for gemini-3.* / gateway models.
	antigravityOAuthClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	antigravityOAuthClientSecret = "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"

	oauthTokenURL = "https://oauth2.googleapis.com/token"

	// Refresh a little early so in-flight requests don't race expiry.
	tokenExpirySlack = 30 * time.Second
)

type Credentials struct {
	AccessToken  string
	RefreshToken string
	Expiry       time.Time
}

func (c Credentials) expired(now time.Time) bool {
	if c.Expiry.IsZero() {
		return false
	}
	return !now.Add(tokenExpirySlack).Before(c.Expiry)
}

type oauthCredsFile struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type,omitempty"`
	Scope        string `json:"scope,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
	ExpiryDate   int64  `json:"expiry_date,omitempty"`
}

func ParseCredentials(data []byte) (Credentials, error) {
	var file oauthCredsFile
	if err := json.Unmarshal(data, &file); err != nil {
		return Credentials{}, fmt.Errorf("invalid gemini oauth JSON: %w", err)
	}
	if file.AccessToken == "" && file.RefreshToken == "" {
		return Credentials{}, errors.New("missing access_token and refresh_token")
	}
	creds := Credentials{
		AccessToken:  file.AccessToken,
		RefreshToken: file.RefreshToken,
	}
	if file.ExpiryDate > 0 {
		creds.Expiry = time.UnixMilli(file.ExpiryDate)
	}
	return creds, nil
}

// tokenSource loads oauth_creds.json (Gemini CLI or Antigravity), refreshes
// expired access tokens via the matching Google OAuth client, and persists
// refreshed tokens back to disk.
type tokenSource struct {
	path         string
	httpClient   *http.Client
	now          func() time.Time
	tokenURL     string
	clientID     string
	clientSecret string

	mu    sync.Mutex
	cache Credentials
}

func newTokenSource(path string, httpClient *http.Client, now func() time.Time) *tokenSource {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if now == nil {
		now = time.Now
	}
	clientID, clientSecret := geminiCLIOAuthClientID, geminiCLIOAuthClientSecret
	if isAntigravityAuthPath(path) {
		clientID, clientSecret = antigravityOAuthClientID, antigravityOAuthClientSecret
	}
	return &tokenSource{
		path:         path,
		httpClient:   httpClient,
		now:          now,
		tokenURL:     oauthTokenURL,
		clientID:     clientID,
		clientSecret: clientSecret,
	}
}

func isAntigravityAuthPath(path string) bool {
	return strings.Contains(strings.ToLower(filepath.Base(path)), "antigravity")
}

func (t *tokenSource) Token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.cache.AccessToken != "" && !t.cache.expired(t.now()) {
		return t.cache.AccessToken, nil
	}

	data, err := os.ReadFile(t.path)
	if err != nil {
		return "", fmt.Errorf("load gemini auth: %w", err)
	}
	creds, err := ParseCredentials(data)
	if err != nil {
		return "", fmt.Errorf("parse gemini auth: %w", err)
	}

	if creds.AccessToken != "" && !creds.expired(t.now()) {
		t.cache = creds
		return creds.AccessToken, nil
	}
	if creds.RefreshToken == "" {
		return "", errors.New("gemini access token expired and no refresh_token available")
	}

	refreshed, err := t.refresh(ctx, creds)
	if err != nil {
		return "", err
	}
	t.cache = refreshed
	t.persist(refreshed, data)
	return refreshed.AccessToken, nil
}

func (t *tokenSource) refresh(ctx context.Context, creds Credentials) (Credentials, error) {
	form := url.Values{}
	form.Set("client_id", t.clientID)
	form.Set("client_secret", t.clientSecret)
	form.Set("refresh_token", creds.RefreshToken)
	form.Set("grant_type", "refresh_token")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Credentials{}, fmt.Errorf("build gemini token refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return Credentials{}, fmt.Errorf("refresh gemini token: %w", err)
	}
	defer resp.Body.Close()

	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Credentials{}, fmt.Errorf("decode gemini token refresh response: %w", err)
	}
	if resp.StatusCode != http.StatusOK || body.AccessToken == "" {
		message := body.ErrorDesc
		if message == "" {
			message = body.Error
		}
		if message == "" {
			message = fmt.Sprintf("status %d", resp.StatusCode)
		}
		return Credentials{}, fmt.Errorf("gemini token refresh failed: %s", message)
	}

	refreshed := creds
	refreshed.AccessToken = body.AccessToken
	if body.ExpiresIn > 0 {
		refreshed.Expiry = t.now().Add(time.Duration(body.ExpiresIn) * time.Second)
	}
	return refreshed, nil
}

// persist writes the refreshed token back to oauth_creds.json, preserving any
// fields we don't model. Failures are non-fatal: the in-memory cache still
// carries the fresh token.
func (t *tokenSource) persist(creds Credentials, original []byte) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(original, &raw); err != nil {
		return
	}
	raw["access_token"], _ = json.Marshal(creds.AccessToken)
	if !creds.Expiry.IsZero() {
		raw["expiry_date"], _ = json.Marshal(creds.Expiry.UnixMilli())
	}
	updated, err := json.Marshal(raw)
	if err != nil {
		return
	}
	_ = os.WriteFile(t.path, updated, 0o600)
}
