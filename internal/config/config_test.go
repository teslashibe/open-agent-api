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
	unsetenv(t, "CODEX_LOG_BODY_SHAPE")
	unsetenv(t, "CODEX_LOG_REQUEST_IDENTITY")
	unsetenv(t, "CODEX_AGENT_QUEUE_ENABLED")
	unsetenv(t, "CODEX_AGENT_MAX_ACTIVE")
	unsetenv(t, "CODEX_AGENT_MAX_ACTIVE_PER_KEY")
	unsetenv(t, "CODEX_AGENT_QUEUE_KEY_MODE")
	unsetenv(t, "CODEX_AGENT_QUEUE_LIMIT")
	unsetenv(t, "CODEX_AGENT_QUEUE_TIMEOUT")
	unsetenv(t, "CODEX_CONTEXT_MANAGEMENT_ENABLED")
	unsetenv(t, "CODEX_CONTEXT_MAX_BYTES")
	unsetenv(t, "CODEX_CONTEXT_MAX_MESSAGES")
	unsetenv(t, "CODEX_CONTEXT_RECENT_MESSAGES")
	unsetenv(t, "CODEX_CONTEXT_TOOL_OUTPUT_MAX_BYTES")
	unsetenv(t, "CODEX_CONTEXT_COMPACTED_TOOL_OUTPUT_MAX_BYTES")
	unsetenv(t, "CODEX_DEGENERATE_TURN_RETRY")
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
	if cfg.LogBodyShape {
		t.Fatal("LogBodyShape = true, want false")
	}
	if cfg.LogRequestIdentity {
		t.Fatal("LogRequestIdentity = true, want false")
	}
	if cfg.AgentQueueEnabled != DefaultAgentQueueEnabled {
		t.Fatalf("AgentQueueEnabled = %t, want %t", cfg.AgentQueueEnabled, DefaultAgentQueueEnabled)
	}
	if cfg.AgentMaxActive != DefaultAgentMaxActive {
		t.Fatalf("AgentMaxActive = %d, want %d", cfg.AgentMaxActive, DefaultAgentMaxActive)
	}
	if cfg.AgentMaxActivePerKey != DefaultAgentMaxActivePerKey {
		t.Fatalf("AgentMaxActivePerKey = %d, want %d", cfg.AgentMaxActivePerKey, DefaultAgentMaxActivePerKey)
	}
	if cfg.AgentQueueKeyMode != DefaultAgentQueueKeyMode {
		t.Fatalf("AgentQueueKeyMode = %q, want %q", cfg.AgentQueueKeyMode, DefaultAgentQueueKeyMode)
	}
	if cfg.AgentQueueLimit != DefaultAgentQueueLimit {
		t.Fatalf("AgentQueueLimit = %d, want %d", cfg.AgentQueueLimit, DefaultAgentQueueLimit)
	}
	if cfg.AgentQueueTimeout != DefaultAgentQueueTimeout {
		t.Fatalf("AgentQueueTimeout = %s, want %s", cfg.AgentQueueTimeout, DefaultAgentQueueTimeout)
	}
	if cfg.ContextManagementEnabled {
		t.Fatal("ContextManagementEnabled = true, want false")
	}
	if cfg.ContextMaxBytes != DefaultContextMaxBytes ||
		cfg.ContextMaxMessages != DefaultContextMaxMessages ||
		cfg.ContextRecentMessages != DefaultContextRecentMessages ||
		cfg.ContextToolOutputMaxBytes != DefaultContextToolOutputMaxBytes ||
		cfg.ContextCompactedToolOutputMaxBytes != DefaultContextCompactedToolOutputMaxBytes {
		t.Fatalf("context config = enabled:%t max_bytes:%d max_messages:%d recent:%d tool_max:%d compacted_max:%d",
			cfg.ContextManagementEnabled,
			cfg.ContextMaxBytes,
			cfg.ContextMaxMessages,
			cfg.ContextRecentMessages,
			cfg.ContextToolOutputMaxBytes,
			cfg.ContextCompactedToolOutputMaxBytes,
		)
	}
	if cfg.DegenerateTurnRetryEnabled != DefaultDegenerateTurnRetryEnabled {
		t.Fatalf("DegenerateTurnRetryEnabled = %t, want %t", cfg.DegenerateTurnRetryEnabled, DefaultDegenerateTurnRetryEnabled)
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
	t.Setenv("CODEX_LOG_BODY_SHAPE", "true")
	t.Setenv("CODEX_LOG_REQUEST_IDENTITY", "true")
	t.Setenv("CODEX_AGENT_QUEUE_ENABLED", "false")
	t.Setenv("CODEX_AGENT_MAX_ACTIVE", "2")
	t.Setenv("CODEX_AGENT_MAX_ACTIVE_PER_KEY", "2")
	t.Setenv("CODEX_AGENT_QUEUE_KEY_MODE", "header:x-cursor-session-id")
	t.Setenv("CODEX_AGENT_QUEUE_LIMIT", "7")
	t.Setenv("CODEX_AGENT_QUEUE_TIMEOUT", "9s")
	t.Setenv("CODEX_CONTEXT_MANAGEMENT_ENABLED", "true")
	t.Setenv("CODEX_CONTEXT_MAX_BYTES", "12345")
	t.Setenv("CODEX_CONTEXT_MAX_MESSAGES", "77")
	t.Setenv("CODEX_CONTEXT_RECENT_MESSAGES", "9")
	t.Setenv("CODEX_CONTEXT_TOOL_OUTPUT_MAX_BYTES", "456")
	t.Setenv("CODEX_CONTEXT_COMPACTED_TOOL_OUTPUT_MAX_BYTES", "78")
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
	if !cfg.LogBodyShape {
		t.Fatal("LogBodyShape = false, want true")
	}
	if !cfg.LogRequestIdentity {
		t.Fatal("LogRequestIdentity = false, want true")
	}
	if cfg.AgentQueueEnabled {
		t.Fatal("AgentQueueEnabled = true, want false")
	}
	if cfg.AgentMaxActive != 2 || cfg.AgentMaxActivePerKey != 2 || cfg.AgentQueueKeyMode != "header:x-cursor-session-id" || cfg.AgentQueueLimit != 7 || cfg.AgentQueueTimeout != 9*time.Second {
		t.Fatalf("agent queue config = enabled:%t max:%d max_per_key:%d key_mode:%q limit:%d timeout:%s", cfg.AgentQueueEnabled, cfg.AgentMaxActive, cfg.AgentMaxActivePerKey, cfg.AgentQueueKeyMode, cfg.AgentQueueLimit, cfg.AgentQueueTimeout)
	}
	if !cfg.ContextManagementEnabled ||
		cfg.ContextMaxBytes != 12345 ||
		cfg.ContextMaxMessages != 77 ||
		cfg.ContextRecentMessages != 9 ||
		cfg.ContextToolOutputMaxBytes != 456 ||
		cfg.ContextCompactedToolOutputMaxBytes != 78 {
		t.Fatalf("context config = enabled:%t max_bytes:%d max_messages:%d recent:%d tool_max:%d compacted_max:%d",
			cfg.ContextManagementEnabled,
			cfg.ContextMaxBytes,
			cfg.ContextMaxMessages,
			cfg.ContextRecentMessages,
			cfg.ContextToolOutputMaxBytes,
			cfg.ContextCompactedToolOutputMaxBytes,
		)
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
		"--log-body-shape",
		"--log-request-identity",
		"--agent-queue-enabled=false",
		"--agent-max-active", "3",
		"--agent-max-active-per-key", "2",
		"--agent-queue-key-mode", "body:session_id",
		"--agent-queue-limit", "8",
		"--agent-queue-timeout", "10s",
		"--context-management-enabled",
		"--context-max-bytes", "23456",
		"--context-max-messages", "88",
		"--context-recent-messages", "10",
		"--context-tool-output-max-bytes", "567",
		"--context-compacted-tool-output-max-bytes", "89",
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
	if !cfg.LogBodyShape {
		t.Fatal("LogBodyShape = false, want true")
	}
	if !cfg.LogRequestIdentity {
		t.Fatal("LogRequestIdentity = false, want true")
	}
	if cfg.AgentQueueEnabled {
		t.Fatal("AgentQueueEnabled = true, want false")
	}
	if cfg.AgentMaxActive != 3 || cfg.AgentMaxActivePerKey != 2 || cfg.AgentQueueKeyMode != "body:session_id" || cfg.AgentQueueLimit != 8 || cfg.AgentQueueTimeout != 10*time.Second {
		t.Fatalf("agent queue config = enabled:%t max:%d max_per_key:%d key_mode:%q limit:%d timeout:%s", cfg.AgentQueueEnabled, cfg.AgentMaxActive, cfg.AgentMaxActivePerKey, cfg.AgentQueueKeyMode, cfg.AgentQueueLimit, cfg.AgentQueueTimeout)
	}
	if !cfg.ContextManagementEnabled ||
		cfg.ContextMaxBytes != 23456 ||
		cfg.ContextMaxMessages != 88 ||
		cfg.ContextRecentMessages != 10 ||
		cfg.ContextToolOutputMaxBytes != 567 ||
		cfg.ContextCompactedToolOutputMaxBytes != 89 {
		t.Fatalf("context config = enabled:%t max_bytes:%d max_messages:%d recent:%d tool_max:%d compacted_max:%d",
			cfg.ContextManagementEnabled,
			cfg.ContextMaxBytes,
			cfg.ContextMaxMessages,
			cfg.ContextRecentMessages,
			cfg.ContextToolOutputMaxBytes,
			cfg.ContextCompactedToolOutputMaxBytes,
		)
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

func TestLoadInvalidLogBodyShape(t *testing.T) {
	t.Setenv("CODEX_LOG_BODY_SHAPE", "definitely")
	chdir(t, t.TempDir())

	if _, err := Load(nil); err == nil {
		t.Fatal("Load() error = nil, want invalid log body shape error")
	}
}

func TestLoadInvalidLogRequestIdentity(t *testing.T) {
	t.Setenv("CODEX_LOG_REQUEST_IDENTITY", "definitely")
	chdir(t, t.TempDir())

	if _, err := Load(nil); err == nil {
		t.Fatal("Load() error = nil, want invalid log request identity error")
	}
}

func TestLoadInvalidAgentQueueEnabled(t *testing.T) {
	t.Setenv("CODEX_AGENT_QUEUE_ENABLED", "definitely")
	chdir(t, t.TempDir())

	if _, err := Load(nil); err == nil {
		t.Fatal("Load() error = nil, want invalid agent queue enabled error")
	}
}

func TestLoadInvalidAgentMaxActive(t *testing.T) {
	t.Setenv("CODEX_AGENT_MAX_ACTIVE", "0")
	chdir(t, t.TempDir())

	if _, err := Load(nil); err == nil {
		t.Fatal("Load() error = nil, want invalid agent max active error")
	}
}

func TestLoadInvalidAgentMaxActivePerKey(t *testing.T) {
	t.Setenv("CODEX_AGENT_MAX_ACTIVE_PER_KEY", "0")
	chdir(t, t.TempDir())

	if _, err := Load(nil); err == nil {
		t.Fatal("Load() error = nil, want invalid agent max active per key error")
	}
}

func TestLoadInvalidAgentQueueKeyMode(t *testing.T) {
	tests := []string{"session", "header:", "body:"}
	for _, value := range tests {
		t.Run(value, func(t *testing.T) {
			t.Setenv("CODEX_AGENT_QUEUE_KEY_MODE", value)
			chdir(t, t.TempDir())

			if _, err := Load(nil); err == nil {
				t.Fatal("Load() error = nil, want invalid agent queue key mode error")
			}
		})
	}
}

func TestValidateAgentQueueKeyModeAcceptsCursor(t *testing.T) {
	if err := validateAgentQueueKeyMode("cursor"); err != nil {
		t.Fatalf("validateAgentQueueKeyMode(cursor) error = %v", err)
	}
}

func TestLoadInvalidAgentQueueLimit(t *testing.T) {
	t.Setenv("CODEX_AGENT_QUEUE_LIMIT", "-1")
	chdir(t, t.TempDir())

	if _, err := Load(nil); err == nil {
		t.Fatal("Load() error = nil, want invalid agent queue limit error")
	}
}

func TestLoadInvalidAgentQueueTimeout(t *testing.T) {
	t.Setenv("CODEX_AGENT_QUEUE_TIMEOUT", "0s")
	chdir(t, t.TempDir())

	if _, err := Load(nil); err == nil {
		t.Fatal("Load() error = nil, want invalid agent queue timeout error")
	}
}

func TestLoadInvalidContextManagementEnabled(t *testing.T) {
	t.Setenv("CODEX_CONTEXT_MANAGEMENT_ENABLED", "definitely")
	chdir(t, t.TempDir())

	if _, err := Load(nil); err == nil {
		t.Fatal("Load() error = nil, want invalid context management enabled error")
	}
}

func TestLoadInvalidContextLimits(t *testing.T) {
	tests := map[string]string{
		"CODEX_CONTEXT_MAX_BYTES":                       "-1",
		"CODEX_CONTEXT_MAX_MESSAGES":                    "-1",
		"CODEX_CONTEXT_RECENT_MESSAGES":                 "-1",
		"CODEX_CONTEXT_TOOL_OUTPUT_MAX_BYTES":           "-1",
		"CODEX_CONTEXT_COMPACTED_TOOL_OUTPUT_MAX_BYTES": "-1",
	}
	for key, value := range tests {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, value)
			chdir(t, t.TempDir())

			if _, err := Load(nil); err == nil {
				t.Fatal("Load() error = nil, want invalid context limit error")
			}
		})
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
