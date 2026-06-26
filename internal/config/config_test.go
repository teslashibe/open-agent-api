package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	unsetenv(t, "CODEX_CHAT_API_HOST")
	unsetenv(t, "HOST")
	unsetenv(t, "CODEX_CHAT_API_PORT")
	unsetenv(t, "PORT")
	unsetenv(t, "CODEX_HOME")
	unsetenv(t, "CODEX_AUTH_PATH")
	unsetenv(t, "CODEX_PROFILE_PATH")
	unsetenv(t, "CODEX_SCAFFOLD_PATH")
	unsetenv(t, "CODEX_WEBSOCKET_URL")
	unsetenv(t, "CODEX_TIMEOUT")
	chdir(t, t.TempDir())

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Host != DefaultHost {
		t.Fatalf("Host = %q, want %q", cfg.Host, DefaultHost)
	}
	if cfg.Port != DefaultPort {
		t.Fatalf("Port = %d, want %d", cfg.Port, DefaultPort)
	}
	if filepath.Base(cfg.CodexHome) != ".codex" {
		t.Fatalf("CodexHome = %q, want .codex suffix", cfg.CodexHome)
	}
	if cfg.AuthPath != filepath.Join(cfg.CodexHome, "auth.json") {
		t.Fatalf("AuthPath = %q, want auth.json under CodexHome", cfg.AuthPath)
	}
	if cfg.CodexProfilePath != "codex_profile.json" {
		t.Fatalf("CodexProfilePath = %q", cfg.CodexProfilePath)
	}
	if cfg.CodexScaffoldPath != "codex_scaffold.json" {
		t.Fatalf("CodexScaffoldPath = %q", cfg.CodexScaffoldPath)
	}
	if cfg.CodexWebsocketURL != DefaultCodexWebsocketURL {
		t.Fatalf("CodexWebsocketURL = %q", cfg.CodexWebsocketURL)
	}
	if cfg.CodexTimeout != DefaultCodexTimeout {
		t.Fatalf("CodexTimeout = %s", cfg.CodexTimeout)
	}
}

func TestLoadEnvironment(t *testing.T) {
	t.Setenv("CODEX_CHAT_API_HOST", "0.0.0.0")
	t.Setenv("CODEX_CHAT_API_PORT", "9090")
	t.Setenv("CODEX_HOME", "/tmp/codex-home")
	t.Setenv("CODEX_AUTH_PATH", "/tmp/auth.json")
	t.Setenv("CODEX_PROFILE_PATH", "/tmp/profile.json")
	t.Setenv("CODEX_SCAFFOLD_PATH", "/tmp/scaffold.json")
	t.Setenv("CODEX_WEBSOCKET_URL", "wss://example.test/codex")
	t.Setenv("CODEX_TIMEOUT", "3s")
	chdir(t, t.TempDir())

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Host != "0.0.0.0" || cfg.Port != 9090 {
		t.Fatalf("address = %s, want 0.0.0.0:9090", cfg.Addr())
	}
	if cfg.CodexHome != "/tmp/codex-home" {
		t.Fatalf("CodexHome = %q", cfg.CodexHome)
	}
	if cfg.AuthPath != "/tmp/auth.json" {
		t.Fatalf("AuthPath = %q", cfg.AuthPath)
	}
	if cfg.CodexProfilePath != "/tmp/profile.json" || cfg.CodexScaffoldPath != "/tmp/scaffold.json" {
		t.Fatalf("codex fixture paths = %q %q", cfg.CodexProfilePath, cfg.CodexScaffoldPath)
	}
	if cfg.CodexWebsocketURL != "wss://example.test/codex" || cfg.CodexTimeout != 3*time.Second {
		t.Fatalf("codex network config = %q %s", cfg.CodexWebsocketURL, cfg.CodexTimeout)
	}
}

func TestLoadFlagsOverrideEnvironment(t *testing.T) {
	t.Setenv("CODEX_CHAT_API_HOST", "0.0.0.0")
	t.Setenv("CODEX_CHAT_API_PORT", "9090")
	t.Setenv("CODEX_HOME", "/tmp/codex-home")
	t.Setenv("CODEX_AUTH_PATH", "/tmp/auth.json")
	chdir(t, t.TempDir())

	cfg, err := Load([]string{
		"--host", "127.0.0.2",
		"--port", "8089",
		"--codex-home", "/tmp/flag-codex",
		"--auth-path", "/tmp/flag-auth.json",
		"--codex-profile", "/tmp/flag-profile.json",
		"--codex-scaffold", "/tmp/flag-scaffold.json",
		"--codex-websocket-url", "wss://flag.test/codex",
		"--codex-timeout", "4s",
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Host != "127.0.0.2" || cfg.Port != 8089 {
		t.Fatalf("address = %s, want 127.0.0.2:8089", cfg.Addr())
	}
	if cfg.CodexHome != "/tmp/flag-codex" {
		t.Fatalf("CodexHome = %q", cfg.CodexHome)
	}
	if cfg.AuthPath != "/tmp/flag-auth.json" {
		t.Fatalf("AuthPath = %q", cfg.AuthPath)
	}
	if cfg.CodexProfilePath != "/tmp/flag-profile.json" || cfg.CodexScaffoldPath != "/tmp/flag-scaffold.json" {
		t.Fatalf("codex fixture paths = %q %q", cfg.CodexProfilePath, cfg.CodexScaffoldPath)
	}
	if cfg.CodexWebsocketURL != "wss://flag.test/codex" || cfg.CodexTimeout != 4*time.Second {
		t.Fatalf("codex network config = %q %s", cfg.CodexWebsocketURL, cfg.CodexTimeout)
	}
}

func TestLoadDotEnv(t *testing.T) {
	unsetenv(t, "CODEX_CHAT_API_HOST")
	unsetenv(t, "CODEX_CHAT_API_PORT")
	unsetenv(t, "CODEX_HOME")
	unsetenv(t, "CODEX_AUTH_PATH")
	dir := t.TempDir()
	chdir(t, dir)

	env := "CODEX_CHAT_API_HOST=192.0.2.1\nCODEX_CHAT_API_PORT=9091\nCODEX_HOME=/tmp/dot-codex\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Host != "192.0.2.1" || cfg.Port != 9091 {
		t.Fatalf("address = %s, want 192.0.2.1:9091", cfg.Addr())
	}
	if cfg.AuthPath != filepath.Join("/tmp/dot-codex", "auth.json") {
		t.Fatalf("AuthPath = %q", cfg.AuthPath)
	}
}

func TestLoadInvalidPort(t *testing.T) {
	t.Setenv("CODEX_CHAT_API_PORT", "70000")
	chdir(t, t.TempDir())

	if _, err := Load(nil); err == nil {
		t.Fatal("Load() error = nil, want invalid port error")
	}
}

func TestCodexHomeFlagUpdatesDefaultAuthPath(t *testing.T) {
	unsetenv(t, "CODEX_AUTH_PATH")
	chdir(t, t.TempDir())

	cfg, err := Load([]string{"--codex-home", "/tmp/flag-codex"})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.AuthPath != filepath.Join("/tmp/flag-codex", "auth.json") {
		t.Fatalf("AuthPath = %q", cfg.AuthPath)
	}
}

func unsetenv(t *testing.T, key string) {
	t.Helper()
	old, ok := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var err error
		if ok {
			err = os.Setenv(key, old)
		} else {
			err = os.Unsetenv(key)
		}
		if err != nil {
			t.Fatalf("restore %s: %v", key, err)
		}
	})
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(old); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})
}
