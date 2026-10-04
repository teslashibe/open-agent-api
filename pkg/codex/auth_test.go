package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type authOnlyTransport struct {
	target *url.URL
	calls  atomic.Int32
}

func (t *authOnlyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	if r.Method != http.MethodPost || r.URL.String() != "https://auth.openai.com/oauth/token" {
		return nil, errors.New("synthetic-private-unexpected-endpoint")
	}
	clone := r.Clone(r.Context())
	clone.URL.Scheme, clone.URL.Host = t.target.Scheme, t.target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

func TestPublicAuthenticationOnlyRenewal(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "auth.json")
	jwt := func(expiry time.Time) string {
		return "synthetic." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, expiry.Unix()))) + ".synthetic"
	}
	initial, _ := json.Marshal(map[string]any{"tokens": map[string]any{"access_token": jwt(time.Now().Add(90 * time.Second)), "refresh_token": "synthetic-private-refresh", "account_id": "synthetic-private-account"}})
	if err := os.WriteFile(path, initial, 0600); err != nil {
		t.Fatal(err)
	}
	var reject atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ParseForm() != nil || r.Form.Get("grant_type") != "refresh_token" {
			t.Error("not an authentication refresh")
		}
		if reject.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"code":"invalid_refresh_token","message":"synthetic-private-account synthetic-private-refresh"}}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": jwt(time.Now().Add(time.Hour)), "refresh_token": "synthetic-rotated-refresh", "expires_in": 3600})
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	transport := &authOnlyTransport{target: endpoint}
	previous := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: transport}
	defer func() { http.DefaultClient = previous }()
	a, err := NewAuthentication(path)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(150 * time.Second)
	if err := a.EnsureValidUntil(context.Background(), deadline); err != nil {
		t.Fatal(err)
	}
	if err := a.EnsureValidUntil(context.Background(), deadline); err != nil || transport.calls.Load() != 1 {
		t.Fatal("sufficient profile renewed again")
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "synthetic-rotated-refresh") {
		t.Fatal("public success preceded durable credentials")
	}
	reject.Store(true)
	if err := os.WriteFile(path, initial, 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.EnsureValidUntil(context.Background(), deadline); err != ErrAuthenticationUnavailable || strings.Contains(err.Error(), "synthetic-private") {
		t.Fatal("provider rejection exposed private account details")
	}
	data, err = os.ReadFile(path)
	if err != nil || string(data) != string(initial) || transport.calls.Load() != 2 {
		t.Fatal("rejected refresh changed profile or repeated OAuth exchange")
	}
}

func TestPublicAuthenticationPathsCancellationAndPrivateErrors(t *testing.T) {
	for _, path := range []string{"", "relative/auth.json", "/synthetic\nprivate/auth.json", "/synthetic\x00private/auth.json"} {
		if _, err := NewAuthentication(path); !errors.Is(err, ErrAuthenticationUnavailable) {
			t.Fatal("invalid path admitted")
		}
	}
	path := filepath.Join(t.TempDir(), "synthetic-private-profile", "auth.json")
	a, err := NewAuthentication(path)
	if err != nil {
		t.Fatal("constructor read a missing profile")
	}
	if err := a.EnsureValidUntil(context.Background(), time.Now().Add(time.Minute)); err != ErrAuthenticationUnavailable || strings.Contains(err.Error(), "synthetic-private") {
		t.Fatal("private profile error exposed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.EnsureValidUntil(ctx, time.Now().Add(time.Minute)); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation was lost")
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if err := a.EnsureValidUntil(ctx, time.Now().Add(time.Minute)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("context deadline was lost")
	}
	var missing *Authentication
	if missing.EnsureValidUntil(context.Background(), time.Now().Add(time.Minute)) != ErrAuthenticationUnavailable || a.EnsureValidUntil(nil, time.Now()) != ErrAuthenticationUnavailable {
		t.Fatal("invalid wrapper admitted")
	}
}
