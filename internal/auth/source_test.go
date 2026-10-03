package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSourceGetRefreshesExpiredAccessToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	fixedNow := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	initial := map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"access_token":  "stale-access",
			"refresh_token": "rt-initial",
			"account_id":    "acct_123",
			"expires_at":    fixedNow.Add(-time.Hour).Unix(),
		},
		"last_refresh": "2026-07-01T00:00:00Z",
	}
	writeJSON(t, path, initial)

	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if r.Form.Get("grant_type") != "refresh_token" {
			t.Fatalf("grant_type = %q", r.Form.Get("grant_type"))
		}
		if r.Form.Get("refresh_token") != "rt-initial" {
			t.Fatalf("refresh_token = %q", r.Form.Get("refresh_token"))
		}
		if r.Form.Get("client_id") != chatgptOAuthClientID {
			t.Fatalf("client_id = %q", r.Form.Get("client_id"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "fresh-access",
			"refresh_token": "rt-rotated",
			"expires_in":    3600,
			"token_type":    "Bearer",
		})
	}))
	t.Cleanup(server.Close)

	src := NewSource(path)
	src.httpClient = server.Client()
	src.tokenURL = server.URL
	src.now = func() time.Time { return fixedNow }

	creds, err := src.Get(context.Background())
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if creds.AccessToken != "fresh-access" {
		t.Fatalf("AccessToken = %q", creds.AccessToken)
	}
	if creds.RefreshToken != "rt-rotated" {
		t.Fatalf("RefreshToken = %q", creds.RefreshToken)
	}
	if hits != 1 {
		t.Fatalf("token endpoint hits = %d, want 1", hits)
	}

	// Second call reads the persisted usable token without another refresh.
	creds2, err := src.Get(context.Background())
	if err != nil {
		t.Fatalf("second Get() error = %v", err)
	}
	if creds2.AccessToken != "fresh-access" || hits != 1 {
		t.Fatalf("persisted credential reuse failed: hits=%d", hits)
	}

	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(persisted), "fresh-access") || !strings.Contains(string(persisted), "rt-rotated") {
		t.Fatalf("auth.json not persisted: %s", persisted)
	}
	var file struct {
		LastRefresh string `json:"last_refresh"`
		Tokens      struct {
			ExpiresAt int64 `json:"expires_at"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(persisted, &file); err != nil {
		t.Fatal(err)
	}
	if file.LastRefresh == "" || file.Tokens.ExpiresAt <= 0 {
		t.Fatalf("missing last_refresh/expires_at: %#v", file)
	}
}

func TestSourceGetFailsClosedWithoutRefreshToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	writeJSON(t, path, map[string]any{
		"tokens": map[string]any{
			"access_token": "stale-access",
			"account_id":   "acct_123",
			"expires_at":   time.Now().Add(-time.Hour).Unix(),
		},
	})
	src := NewSource(path)
	src.now = time.Now
	_, err := src.Get(context.Background())
	if err == nil {
		t.Fatal("Get() error = nil, want refresh failure")
	}
	if !strings.Contains(err.Error(), "no refresh_token") {
		t.Fatalf("error = %v", err)
	}
}

func TestSourceForceRefreshOnAuthFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	writeJSON(t, path, map[string]any{
		"tokens": map[string]any{
			"access_token":  "still-unexpired",
			"refresh_token": "rt-1",
			"account_id":    "acct_123",
			"expires_at":    time.Now().Add(2 * time.Hour).Unix(),
		},
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "forced-fresh",
			"refresh_token": "rt-2",
			"expires_in":    3600,
		})
	}))
	t.Cleanup(server.Close)

	src := NewSource(path)
	src.httpClient = server.Client()
	src.tokenURL = server.URL

	// Prime cache with unexpired token.
	if _, err := src.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	creds, err := src.ForceRefresh(context.Background())
	if err != nil {
		t.Fatalf("ForceRefresh() error = %v", err)
	}
	if creds.AccessToken != "forced-fresh" || creds.RefreshToken != "rt-2" {
		t.Fatalf("ForceRefresh() = %#v", creds)
	}
}

func TestSourceRefreshFailureSurfaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	writeJSON(t, path, map[string]any{
		"tokens": map[string]any{
			"access_token":  "stale",
			"refresh_token": "rt-bad",
			"account_id":    "acct_123",
			"expires_at":    time.Now().Add(-time.Minute).Unix(),
		},
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":             "invalid_grant",
			"error_description": "refresh_token_revoked",
		})
	}))
	t.Cleanup(server.Close)

	src := NewSource(path)
	src.httpClient = server.Client()
	src.tokenURL = server.URL
	_, err := src.Get(context.Background())
	if err == nil {
		t.Fatal("Get() error = nil")
	}
	if !strings.Contains(err.Error(), "reason invalid_grant") || strings.Contains(err.Error(), "refresh_token_revoked") {
		t.Fatalf("error = %v", err)
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSourceRejectionRemainsPrivateWithoutChangingCredentials(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprint("force=", force), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "auth.json")
			writeJSON(t, path, map[string]any{"tokens": map[string]any{"access_token": "synthetic-private-access", "refresh_token": "synthetic-private-refresh", "account_id": "synthetic-private-account", "expires_at": time.Now().Add(-time.Hour).Unix()}})
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			hits := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits++
				w.WriteHeader(401)
				fmt.Fprint(w, `{"error":{"code":"invalid_refresh_token","message":"synthetic-private-access synthetic-private-refresh synthetic-private-account"},"error_description":"synthetic-private-description"}`)
			}))
			defer server.Close()
			source := NewSource(path)
			source.httpClient = server.Client()
			source.tokenURL = server.URL
			if force {
				source.cache = Credentials{AccessToken: "synthetic-private-cached", AccountID: "synthetic-private-account"}
			}
			var got Credentials
			if force {
				got, err = source.ForceRefresh(context.Background())
			} else {
				got, err = source.Get(context.Background())
			}
			if got != (Credentials{}) || err == nil || err.Error() != "codex token refresh failed: status 401 reason invalid_refresh_token" {
				t.Fatal("unsafe rejection contract")
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil || !bytes.Equal(before, after) || hits != 1 || source.cache != (Credentials{}) {
				t.Fatal("rejection changed credentials, cached rejected token or repeated refresh")
			}
		})
	}
}
