package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTokenSourceRefreshesExpiredTokenAndPersistsRotation(t *testing.T) {
	now := time.Date(2026, 7, 22, 7, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "auth.json")
	writeTokenSourceAuth(t, path, testJWT(now.Add(-time.Minute)), "old-refresh-secret", "preserve-me")

	newAccess := testJWT(now.Add(time.Hour))
	var calls atomic.Int64
	httpClient := testHTTPClient(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("refresh request = %s content-type=%q", r.Method, r.Header.Get("Content-Type"))
		}
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode refresh JSON: %v", err)
		}
		if len(payload) != 3 || payload["client_id"] != DefaultClientID || payload["grant_type"] != "refresh_token" || payload["refresh_token"] != "old-refresh-secret" {
			t.Fatalf("refresh JSON = %#v", payload)
		}
		return jsonResponse(http.StatusOK, map[string]string{
			"access_token": newAccess, "refresh_token": "new-refresh-secret", "id_token": "new-id-secret",
		}), nil
	})

	source := NewTokenSource(TokenSourceConfig{Path: path, HTTPClient: httpClient, Now: func() time.Time { return now }})
	creds, _, err := source.Credentials(context.Background())
	if err != nil {
		t.Fatalf("Credentials() error = %v", err)
	}
	if creds.AccessToken != newAccess || creds.RefreshToken != "new-refresh-secret" || creds.IDToken != "new-id-secret" {
		t.Fatalf("Credentials() = %#v", creds)
	}
	if calls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls.Load())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{newAccess, "new-refresh-secret", "new-id-secret", "preserve-me", `"last_refresh":"2026-07-22T07:00:00Z"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("persisted auth missing %q: %s", want, text)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("auth mode = %o, want 600", info.Mode().Perm())
	}
}

func TestTokenSourceRefreshIsSingleFlight(t *testing.T) {
	now := time.Date(2026, 7, 22, 7, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "auth.json")
	writeTokenSourceAuth(t, path, testJWT(now.Add(-time.Minute)), "one-use-refresh", "preserve")
	newAccess := testJWT(now.Add(time.Hour))

	var calls atomic.Int64
	release := make(chan struct{})
	httpClient := testHTTPClient(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		<-release
		return jsonResponse(http.StatusOK, map[string]string{"access_token": newAccess, "refresh_token": "rotated-refresh"}), nil
	})
	source := NewTokenSource(TokenSourceConfig{Path: path, HTTPClient: httpClient, Now: func() time.Time { return now }})

	const callers = 12
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			creds, _, err := source.Credentials(context.Background())
			if err == nil && creds.AccessToken != newAccess {
				err = fmt.Errorf("access token was not refreshed")
			}
			errs <- err
		}()
	}
	close(start)
	for calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Credentials() error = %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls.Load())
	}
}

func TestTokenSourceForceRefreshesOpaqueRejectedToken(t *testing.T) {
	now := time.Date(2026, 7, 22, 7, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "auth.json")
	writeTokenSourceAuth(t, path, "opaque-access", "refresh-secret", "preserve")
	httpClient := testHTTPClient(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, map[string]string{"access_token": "replacement-access"}), nil
	})
	source := NewTokenSource(TokenSourceConfig{Path: path, HTTPClient: httpClient, Now: func() time.Time { return now }})
	_, revision, err := source.Credentials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	creds, nextRevision, err := source.ForceRefresh(context.Background(), revision)
	if err != nil {
		t.Fatalf("ForceRefresh() error = %v", err)
	}
	if creds.AccessToken != "replacement-access" || nextRevision == revision {
		t.Fatalf("ForceRefresh() = %#v revision_changed=%t", creds, nextRevision != revision)
	}
}

func TestTokenSourceRefreshFailureIsSanitizedAndLeavesFile(t *testing.T) {
	now := time.Date(2026, 7, 22, 7, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "auth.json")
	writeTokenSourceAuth(t, path, testJWT(now.Add(-time.Minute)), "refresh-secret-do-not-leak", "preserve")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	httpClient := testHTTPClient(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"refresh-secret-do-not-leak"}}`)),
		}, nil
	})
	source := NewTokenSource(TokenSourceConfig{Path: path, HTTPClient: httpClient, Now: func() time.Time { return now }})
	_, _, err = source.Credentials(context.Background())
	if !errors.Is(err, ErrTokenRefreshFailed) {
		t.Fatalf("Credentials() error = %v, want ErrTokenRefreshFailed", err)
	}
	if strings.Contains(err.Error(), "refresh-secret-do-not-leak") {
		t.Fatalf("refresh error leaked secret: %v", err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != string(original) {
		t.Fatalf("failed refresh changed auth file: %s", after)
	}
	_, _, err = source.Credentials(context.Background())
	if !errors.Is(err, ErrTokenRefreshFailed) || calls.Load() != 1 {
		t.Fatalf("cached refresh failure = %v calls=%d, want one failed request", err, calls.Load())
	}
}

func TestTokenSourceExpiredWithoutRefreshTokenFailsClosed(t *testing.T) {
	now := time.Date(2026, 7, 22, 7, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte(fmt.Sprintf(`{"tokens":{"access_token":%q,"account_id":"acct"}}`, testJWT(now.Add(-time.Minute)))), 0o600); err != nil {
		t.Fatal(err)
	}
	source := NewTokenSource(TokenSourceConfig{Path: path, Now: func() time.Time { return now }})
	_, _, err := source.Credentials(context.Background())
	if !errors.Is(err, ErrRefreshTokenMissing) {
		t.Fatalf("Credentials() error = %v, want ErrRefreshTokenMissing", err)
	}
}

func TestTokenSourceRetriesFailedRevisionAfterBackoff(t *testing.T) {
	now := time.Date(2026, 7, 22, 7, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "auth.json")
	writeTokenSourceAuth(t, path, testJWT(now.Add(-time.Minute)), "refresh-secret", "preserve")
	var calls atomic.Int64
	httpClient := testHTTPClient(func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return jsonResponse(http.StatusServiceUnavailable, map[string]string{"error": "temporary"}), nil
		}
		return jsonResponse(http.StatusOK, map[string]string{"access_token": "recovered-access"}), nil
	})
	source := NewTokenSource(TokenSourceConfig{Path: path, HTTPClient: httpClient, Now: func() time.Time { return now }})

	if _, _, err := source.Credentials(context.Background()); !errors.Is(err, ErrTokenRefreshFailed) {
		t.Fatalf("first Credentials() error = %v", err)
	}
	now = now.Add(refreshFailureBackoff + time.Second)
	creds, _, err := source.Credentials(context.Background())
	if err != nil || creds.AccessToken != "recovered-access" {
		t.Fatalf("Credentials() after backoff = %#v, %v", creds, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("refresh calls = %d, want failed request plus recovery", calls.Load())
	}
}

func writeTokenSourceAuth(t *testing.T, path, accessToken, refreshToken, preserved string) {
	t.Helper()
	data := []byte(fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"access_token":%q,"refresh_token":%q,"id_token":"old-id","account_id":"acct"},"unknown":%q}`, accessToken, refreshToken, preserved))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func testHTTPClient(roundTrip roundTripFunc) *http.Client {
	return &http.Client{Transport: roundTrip}
}

func jsonResponse(status int, body any) *http.Response {
	data, _ := json.Marshal(body)
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(data))),
	}
}
