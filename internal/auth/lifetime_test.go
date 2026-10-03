package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Synthetic unsigned JWT-shaped values test lifetime only, not authentication.
func lifetimeJWT(claims string) string {
	return "synthetic." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".synthetic"
}

func lifetimeDirectory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func lifetimeProfile(t *testing.T, expiry time.Time, refresh bool) string {
	t.Helper()
	tokens := map[string]any{"access_token": lifetimeJWT(fmt.Sprintf(`{"exp":%d}`, expiry.Unix())), "account_id": "synthetic-private-account", "unknown_token": "preserve-token"}
	if refresh {
		tokens["refresh_token"] = "synthetic-private-refresh"
	}
	path := filepath.Join(lifetimeDirectory(t), "auth.json")
	writeJSON(t, path, map[string]any{"tokens": tokens, "unknown_top": "preserve-top"})
	return path
}

func lifetimeServer(t *testing.T, access string, expiresIn int64, before func()) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.ParseForm() != nil || r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "synthetic-private-refresh" {
			t.Error("unexpected request outside authentication refresh")
		}
		if before != nil {
			before()
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": access, "refresh_token": "synthetic-rotated-refresh", "expires_in": expiresIn})
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func TestEnsureValidUntilRefreshesBeforeOrdinaryGetWindow(t *testing.T) {
	now := time.Unix(2000000000, 0)
	path := lifetimeProfile(t, now.Add(90*time.Second), true)
	server, calls := lifetimeServer(t, lifetimeJWT(fmt.Sprintf(`{"exp":%d}`, now.Add(time.Hour).Unix())), 3600, nil)
	s := syntheticSource(path, server)
	s.now = func() time.Time { return now }
	if _, err := s.Get(context.Background()); err != nil || calls.Load() != 0 {
		t.Fatal("ordinary Get changed its existing 60-second refresh window")
	}
	deadline := now.Add(150 * time.Second)
	if err := s.EnsureValidUntil(context.Background(), deadline); err != nil || calls.Load() != 1 {
		t.Fatal("required lifetime did not trigger authentication-only renewal", err)
	}
	data, err := os.ReadFile(path)
	expiry, ok := knownAuthExpiry(data)
	if err != nil || !ok || !expiry.After(deadline) || !bytes.Contains(data, []byte("synthetic-rotated-refresh")) || !bytes.Contains(data, []byte("preserve-top")) || !bytes.Contains(data, []byte("preserve-token")) {
		t.Fatal("successful renewal was not durable or discarded unknown fields")
	}
	if err := s.EnsureValidUntil(context.Background(), deadline); err != nil || calls.Load() != 1 {
		t.Fatal("already sufficient persisted credentials rotated again")
	}
}

func TestEnsureValidUntilConservativeBoundaryAndMalformedExpiry(t *testing.T) {
	now := time.Unix(2000000000, 0)
	deadline := now.Add(150 * time.Second)
	for _, tc := range []struct {
		name      string
		claims    string
		persisted any
		want      bool
	}{
		{"strictly beyond", fmt.Sprintf(`{"exp":%d}`, deadline.Add(time.Second).Unix()), nil, true},
		{"exact boundary", fmt.Sprintf(`{"exp":%d}`, deadline.Unix()), nil, false},
		{"stale JWT despite later persisted expiry", fmt.Sprintf(`{"exp":%d}`, now.Add(-time.Second).Unix()), now.Add(time.Hour).Unix(), false},
		{"persisted expiry shortens JWT", fmt.Sprintf(`{"exp":%d}`, now.Add(time.Hour).Unix()), deadline.Unix(), false},
		{"persisted expiry cannot extend JWT", fmt.Sprintf(`{"exp":%d}`, deadline.Unix()), now.Add(time.Hour).Unix(), false},
		{"missing", `{}`, nil, false},
		{"null", `{"exp":null}`, nil, false},
		{"string", `{"exp":"2000003600"}`, nil, false},
		{"fraction", `{"exp":2000003600.5}`, nil, false},
		{"negative", `{"exp":-1}`, nil, false},
		{"duplicate", `{"exp":1,"exp":2000003600}`, nil, false},
		{"unrepresentable", `{"exp":9223372036854775807}`, nil, false},
		{"malformed persisted", `{"exp":2000003600}`, "2000003600", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(lifetimeDirectory(t), "auth.json")
			tokens := map[string]any{"access_token": lifetimeJWT(tc.claims), "account_id": "synthetic-private-account"}
			if tc.persisted != nil {
				tokens["expires_at"] = tc.persisted
			}
			writeJSON(t, path, map[string]any{"tokens": tokens})
			s := NewSource(path)
			s.now = func() time.Time { return now }
			if got := s.EnsureValidUntil(context.Background(), deadline) == nil; got != tc.want {
				t.Fatal("insufficient or ambiguous lifetime admitted")
			}
		})
	}
	for _, raw := range []string{`{"tokens":{},"tokens":{}}`, `{"tokens":`, `{} {}`, `{"tokens":{"access_token":"opaque","account_id":"synthetic","expires_at":2000003600}}`} {
		path := filepath.Join(lifetimeDirectory(t), "auth.json")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if err := NewSource(path).EnsureValidUntil(context.Background(), time.Now().Add(time.Minute)); err == nil {
			t.Fatal("unknown or malformed profile admitted")
		}
	}
}

func TestEnsureValidUntilKeepsInsufficientRotationButNeverAdmitsIt(t *testing.T) {
	now := time.Unix(2000000000, 0)
	deadline := now.Add(150 * time.Second)
	for _, tc := range []struct {
		name, access string
		expires      int64
	}{
		{"short JWT despite expires_in", lifetimeJWT(fmt.Sprintf(`{"exp":%d}`, deadline.Unix())), 3600},
		{"short expires_in despite JWT", lifetimeJWT(fmt.Sprintf(`{"exp":%d}`, now.Add(time.Hour).Unix())), 150},
		{"unknown JWT", "synthetic-opaque-token", 3600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := lifetimeProfile(t, now.Add(90*time.Second), true)
			server, calls := lifetimeServer(t, tc.access, tc.expires, nil)
			s := syntheticSource(path, server)
			s.now = func() time.Time { return now }
			if err := s.EnsureValidUntil(context.Background(), deadline); err == nil || calls.Load() != 1 || s.cache != (Credentials{}) {
				t.Fatal("insufficient renewed credential admitted or cached")
			}
			data, err := os.ReadFile(path)
			if err != nil || !bytes.Contains(data, []byte("synthetic-rotated-refresh")) {
				t.Fatal("successful OAuth rotation lost when lifetime was insufficient")
			}
		})
	}
}

func TestEnsureValidUntilSourcesSerializeAndPersistBeforeSuccess(t *testing.T) {
	now := time.Unix(2000000000, 0)
	path := lifetimeProfile(t, now.Add(90*time.Second), true)
	server, calls := lifetimeServer(t, lifetimeJWT(fmt.Sprintf(`{"exp":%d}`, now.Add(time.Hour).Unix())), 3600, nil)
	sources := []*Source{syntheticSource(path, server), syntheticSource(filepath.Join(filepath.Dir(path), ".", "auth.json"), server)}
	for _, s := range sources {
		s.now = func() time.Time { return now }
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(s *Source) {
			defer wg.Done()
			<-start
			if err := s.EnsureValidUntil(context.Background(), now.Add(150*time.Second)); err != nil {
				t.Error(err)
			}
			data, err := os.ReadFile(path)
			expiry, ok := knownAuthExpiry(data)
			if err != nil || !ok || !expiry.After(now.Add(150*time.Second)) {
				t.Error("success preceded durable sufficient lifetime")
			}
		}(sources[i%len(sources)])
	}
	close(start)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("same-process callers rotated the same profile more than once")
	}
}

func TestEnsureValidUntilPersistenceFailureAndExternalReplacement(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(fmt.Sprint(external), func(t *testing.T) {
			now := time.Unix(2000000000, 0)
			path := lifetimeProfile(t, now.Add(90*time.Second), true)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			replacement := []byte(`{"tokens":{"access_token":"synthetic-external-login","account_id":"synthetic-other"}}`)
			server, _ := lifetimeServer(t, lifetimeJWT(fmt.Sprintf(`{"exp":%d}`, now.Add(time.Hour).Unix())), 3600, func() {
				if external {
					if err := os.WriteFile(path, replacement, 0600); err != nil {
						t.Error(err)
					}
				}
			})
			s := syntheticSource(path, server)
			s.now = func() time.Time { return now }
			if !external {
				s.replaceFile = func(_, _ string) error { return errors.New("synthetic-private-denial") }
			}
			if err := s.EnsureValidUntil(context.Background(), now.Add(150*time.Second)); err == nil || s.cache != (Credentials{}) {
				t.Fatal("failed persistence admitted credentials")
			}
			after, err := os.ReadFile(path)
			want := before
			if external {
				want = replacement
			}
			if err != nil || !bytes.Equal(after, want) {
				t.Fatal("failed renewal overwrote original or observed replacement")
			}
			assertNoAuthCheckpoints(t, path)
		})
	}
}

func TestEnsureValidUntilObservesReplacementRemovalAndCancellation(t *testing.T) {
	now := time.Unix(2000000000, 0)
	path := lifetimeProfile(t, now.Add(time.Hour), false)
	s := NewSource(path)
	s.now = func() time.Time { return now }
	deadline := now.Add(150 * time.Second)
	if err := s.EnsureValidUntil(context.Background(), deadline); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"tokens":{"access_token":"opaque","account_id":"synthetic"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureValidUntil(context.Background(), deadline); err == nil || s.cache != (Credentials{}) {
		t.Fatal("replacement ignored in favor of stale cache")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureValidUntil(context.Background(), deadline); err == nil {
		t.Fatal("removed profile reused")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.EnsureValidUntil(ctx, deadline); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled refresh was not rejected before file access")
	}
	for _, invalid := range []time.Time{{}, now} {
		if err := s.EnsureValidUntil(context.Background(), invalid); err == nil {
			t.Fatal("missing or elapsed validity deadline accepted")
		}
	}
}

func TestEnsureValidUntilCancelledAfterRotationKeepsDurableCredentials(t *testing.T) {
	now := time.Unix(2000000000, 0)
	path := lifetimeProfile(t, now.Add(90*time.Second), true)
	server, _ := lifetimeServer(t, lifetimeJWT(fmt.Sprintf(`{"exp":%d}`, now.Add(time.Hour).Unix())), 3600, nil)
	s := syntheticSource(path, server)
	s.now = func() time.Time { return now }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.replaceFile = func(from, to string) error { cancel(); return os.Rename(from, to) }
	if err := s.EnsureValidUntil(ctx, now.Add(150*time.Second)); !errors.Is(err, context.Canceled) || s.cache != (Credentials{}) {
		t.Fatal("cancelled renewal admitted credentials")
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(data, []byte("synthetic-rotated-refresh")) {
		t.Fatal("cancellation lost completed OAuth rotation")
	}
}

func TestEnsureValidUntilCancelsInFlightOAuthWithoutChangingProfile(t *testing.T) {
	now := time.Unix(2000000000, 0)
	path := lifetimeProfile(t, now.Add(90*time.Second), true)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	s := syntheticSource(path, server)
	s.now = func() time.Time { return now }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.EnsureValidUntil(ctx, now.Add(150*time.Second)) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("synthetic OAuth request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || s.cache != (Credentials{}) {
			t.Fatal("cancelled OAuth returned admission or retained cache")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OAuth request did not stop after owner cancellation")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("cancelled incomplete OAuth exchange changed the profile")
	}
}
