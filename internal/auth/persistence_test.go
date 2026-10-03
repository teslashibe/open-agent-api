package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func syntheticAuth(t *testing.T, expired bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	expiry := time.Now().Add(time.Hour)
	if expired {
		expiry = time.Now().Add(-time.Hour)
	}
	writeJSON(t, path, map[string]any{
		"auth_mode": "chatgpt", "unknown_top": "preserve-top",
		"tokens": map[string]any{
			"access_token": "synthetic-old-access", "refresh_token": "synthetic-old-refresh",
			"account_id": "synthetic-account", "expires_at": expiry.Unix(),
			"id_token": "synthetic-id", "unknown_token": "preserve-token",
		},
	})
	return path
}

func syntheticRefreshServer(t *testing.T, before func()) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if before != nil {
			before()
		}
		fmt.Fprint(w, `{"access_token":"synthetic-fresh-access","refresh_token":"synthetic-fresh-refresh","expires_in":3600}`)
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func syntheticSource(path string, server *httptest.Server) *Source {
	s := NewSource(path)
	s.httpClient = server.Client()
	s.tokenURL = server.URL
	return s
}

func TestPersistenceFailureNeverReturnsOrCachesRotatedCredentials(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprint(force), func(t *testing.T) {
			path := syntheticAuth(t, !force)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			server, _ := syntheticRefreshServer(t, nil)
			s := syntheticSource(path, server)
			if force {
				if _, err := s.Get(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			s.replaceFile = func(_, _ string) error { return errors.New("synthetic-private-denial") }
			var got Credentials
			if force {
				got, err = s.ForceRefresh(context.Background())
			} else {
				got, err = s.Get(context.Background())
			}
			if err == nil || got != (Credentials{}) || s.cache != (Credentials{}) {
				t.Fatalf("failed persistence admitted credentials: err=%v", err)
			}
			if strings.Contains(err.Error(), "synthetic-") {
				t.Fatal("persistence error leaked private context")
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil || !bytes.Equal(before, after) {
				t.Fatal("failed replacement modified original auth")
			}
			assertNoAuthCheckpoints(t, path)
		})
	}
}

func TestGetObservesRemovedAndReplacedProfile(t *testing.T) {
	path := syntheticAuth(t, false)
	s := NewSource(path)
	if _, err := s.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, path, map[string]any{"tokens": map[string]any{
		"access_token": "synthetic-replacement", "account_id": "synthetic-new-account",
		"expires_at": time.Now().Add(time.Hour).Unix(),
	}})
	got, err := s.Get(context.Background())
	if err != nil || got.AccessToken != "synthetic-replacement" || got.AccountID != "synthetic-new-account" {
		t.Fatal("Get did not observe replaced profile")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	got, err = s.Get(context.Background())
	if err == nil || got != (Credentials{}) || s.cache != (Credentials{}) {
		t.Fatal("Get reused removed profile credentials")
	}
}

func TestSameProfileSourcesSerializeRefresh(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprint(force), func(t *testing.T) {
			path := syntheticAuth(t, !force)
			server, calls := syntheticRefreshServer(t, func() { time.Sleep(30 * time.Millisecond) })
			sources := []*Source{
				syntheticSource(path, server),
				syntheticSource(filepath.Join(filepath.Dir(path), ".", "auth.json"), server),
			}
			if force {
				for _, s := range sources {
					if _, err := s.Get(context.Background()); err != nil {
						t.Fatal(err)
					}
				}
			}
			start := make(chan struct{})
			var wg sync.WaitGroup
			for _, s := range sources {
				wg.Add(1)
				go func(s *Source) {
					defer wg.Done()
					<-start
					var got Credentials
					var err error
					if force {
						got, err = s.ForceRefresh(context.Background())
					} else {
						got, err = s.Get(context.Background())
					}
					if err != nil || got.AccessToken != "synthetic-fresh-access" {
						t.Errorf("shared profile refresh failed: %v", err)
					}
				}(s)
			}
			close(start)
			wg.Wait()
			if calls.Load() != 1 {
				t.Fatalf("same profile rotated %d times, want 1", calls.Load())
			}
		})
	}
}

func TestRefreshRejectsObservedExternalProfileReplacement(t *testing.T) {
	path := syntheticAuth(t, true)
	replacement := []byte(`{"tokens":{"access_token":"synthetic-login","account_id":"synthetic-other-account"}}`)
	server, _ := syntheticRefreshServer(t, func() {
		if err := os.WriteFile(path, replacement, 0o600); err != nil {
			t.Error(err)
		}
	})
	s := syntheticSource(path, server)
	got, err := s.Get(context.Background())
	if err == nil || got != (Credentials{}) || s.cache != (Credentials{}) {
		t.Fatal("refresh admitted superseded credentials")
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil || !bytes.Equal(after, replacement) {
		t.Fatal("refresh overwrote observed external login")
	}
	assertNoAuthCheckpoints(t, path)
}

func TestAuthCheckpointIsPrivateAndPreservesUnknownFields(t *testing.T) {
	path := syntheticAuth(t, true)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	server, _ := syntheticRefreshServer(t, nil)
	s := syntheticSource(path, server)
	s.replaceFile = func(from, to string) error {
		info, err := os.Stat(from)
		if err != nil {
			return err
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Error("credential checkpoint is not private")
		}
		if _, err := Load(from); err != nil {
			t.Error("checkpoint does not contain complete credentials")
		}
		return os.Rename(from, to)
	}
	if _, err := s.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]any
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	tokens := file["tokens"].(map[string]any)
	if file["unknown_top"] != "preserve-top" || tokens["unknown_token"] != "preserve-token" || tokens["id_token"] != "synthetic-id" {
		t.Fatal("refresh discarded unknown auth fields")
	}
	info, err := os.Stat(path)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatal("replacement auth permissions are not private")
	}
	assertNoAuthCheckpoints(t, path)
}

func TestAuthCrashBeforeReplacementPreservesOriginal(t *testing.T) {
	if path := os.Getenv("SYNTHETIC_AUTH_CRASH_PATH"); path != "" {
		original, err := os.ReadFile(path)
		if err != nil {
			os.Exit(21)
		}
		_ = replaceAuthFile(path, original, []byte(`{"synthetic":"complete-checkpoint"}`), func(_, _ string) error {
			os.Exit(23)
			return nil
		})
		os.Exit(22)
	}
	path := syntheticAuth(t, false)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestAuthCrashBeforeReplacementPreservesOriginal$")
	cmd.Env = []string{"SYNTHETIC_AUTH_CRASH_PATH=" + path}
	err = cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 23 {
		t.Fatalf("synthetic crash did not reach replacement boundary: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("crash before replacement damaged original auth")
	}
	checkpoints, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".codex-auth-*.tmp"))
	if err != nil || len(checkpoints) != 1 {
		t.Fatal("crash fixture did not leave one complete private checkpoint")
	}
}

func assertNoAuthCheckpoints(t *testing.T, path string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".codex-auth-*.tmp"))
	if err != nil || len(files) != 0 {
		t.Fatal("finished persistence left temporary credentials behind")
	}
}
