package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
	unsetenv(t, "CODEX_LOG_CODEX_TOOL_EVENTS")
	unsetenv(t, "CODEX_AGENT_QUEUE_ENABLED")
	unsetenv(t, "CODEX_AGENT_MAX_ACTIVE")
	unsetenv(t, "CODEX_AGENT_MAX_ACTIVE_PER_KEY")
	unsetenv(t, "CODEX_AGENT_QUEUE_KEY_MODE")
	unsetenv(t, "CODEX_AGENT_QUEUE_LIMIT")
	unsetenv(t, "CODEX_AGENT_QUEUE_TIMEOUT")
	unsetenv(t, "CODEX_AGENT_QUEUE_LOCK_DIR")
	unsetenv(t, "CODEX_AGENT_QUEUE_PRIORITY_ENABLED")
	unsetenv(t, "CODEX_CONTEXT_MANAGEMENT_ENABLED")
	unsetenv(t, "CODEX_CONTEXT_MAX_BYTES")
	unsetenv(t, "CODEX_CONTEXT_MAX_MESSAGES")
	unsetenv(t, "CODEX_CONTEXT_RECENT_MESSAGES")
	unsetenv(t, "CODEX_CONTEXT_TOOL_OUTPUT_MAX_BYTES")
	unsetenv(t, "CODEX_CONTEXT_COMPACTED_TOOL_OUTPUT_MAX_BYTES")
	unsetenv(t, "CODEX_DEGENERATE_TURN_RETRY")
	unsetenv(t, "CODEX_CLIENTS")
	unsetenv(t, "CODEX_CLIENT_MAX_INFLIGHT")
	unsetenv(t, "CODEX_CLIENT_POOL_UNAVAILABLE")
	unsetenv(t, "CODEX_CLIENT_COOLDOWN_DEFAULT")
	unsetenv(t, "CODEX_METRICS_ENABLED")
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
	if cfg.LogCodexToolEvents {
		t.Fatal("LogCodexToolEvents = true, want false")
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
	if cfg.AgentQueueLockDir != "" {
		t.Fatalf("AgentQueueLockDir = %q, want empty default", cfg.AgentQueueLockDir)
	}
	if cfg.AgentQueuePriorityEnabled != DefaultAgentQueuePriorityEnabled {
		t.Fatalf("AgentQueuePriorityEnabled = %t, want %t", cfg.AgentQueuePriorityEnabled, DefaultAgentQueuePriorityEnabled)
	}
	if cfg.ContextManagementEnabled != DefaultContextManagementEnabled {
		t.Fatalf("ContextManagementEnabled = %t, want %t", cfg.ContextManagementEnabled, DefaultContextManagementEnabled)
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
	if cfg.CodexClientPoolUnavailable != DefaultCodexClientPoolUnavailable {
		t.Fatalf("CodexClientPoolUnavailable = %q, want %q", cfg.CodexClientPoolUnavailable, DefaultCodexClientPoolUnavailable)
	}
	if cfg.CodexClientMaxInflight != DefaultCodexClientMaxInflight {
		t.Fatalf("CodexClientMaxInflight = %d, want %d", cfg.CodexClientMaxInflight, DefaultCodexClientMaxInflight)
	}
	if cfg.CodexClientCooldownDefault != DefaultCodexClientCooldownDefault {
		t.Fatalf("CodexClientCooldownDefault = %s, want %s", cfg.CodexClientCooldownDefault, DefaultCodexClientCooldownDefault)
	}
	if cfg.MetricsEnabled != DefaultMetricsEnabled {
		t.Fatalf("MetricsEnabled = %t, want %t", cfg.MetricsEnabled, DefaultMetricsEnabled)
	}
	if len(cfg.CodexClients) != 1 {
		t.Fatalf("CodexClients length = %d, want 1", len(cfg.CodexClients))
	}
	if cfg.CodexClients[0].Label != "default" || cfg.CodexClients[0].AuthPath != cfg.AuthPath || cfg.CodexClients[0].CodexHome != cfg.CodexHome {
		t.Fatalf("default codex client = %#v", cfg.CodexClients[0])
	}
}

func TestLoadMetricsEnvironmentAndFlag(t *testing.T) {
	t.Setenv("CODEX_METRICS_ENABLED", "false")
	chdir(t, t.TempDir())

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MetricsEnabled {
		t.Fatal("MetricsEnabled = true, want false from environment")
	}

	cfg, err = Load([]string{"--metrics-enabled=true"})
	if err != nil {
		t.Fatalf("Load() with flag error = %v", err)
	}
	if !cfg.MetricsEnabled {
		t.Fatal("MetricsEnabled = false, want true from flag")
	}
}

func TestLoadInvalidMetricsEnabled(t *testing.T) {
	t.Setenv("CODEX_METRICS_ENABLED", "sometimes")
	chdir(t, t.TempDir())

	if _, err := Load(nil); err == nil {
		t.Fatal("Load() error = nil, want invalid metrics boolean error")
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
	t.Setenv("CODEX_LOG_CODEX_TOOL_EVENTS", "true")
	t.Setenv("CODEX_AGENT_QUEUE_ENABLED", "false")
	t.Setenv("CODEX_AGENT_MAX_ACTIVE", "2")
	t.Setenv("CODEX_AGENT_MAX_ACTIVE_PER_KEY", "2")
	t.Setenv("CODEX_AGENT_QUEUE_KEY_MODE", "header:x-cursor-session-id")
	t.Setenv("CODEX_AGENT_QUEUE_LIMIT", "7")
	t.Setenv("CODEX_AGENT_QUEUE_TIMEOUT", "9s")
	t.Setenv("CODEX_AGENT_QUEUE_LOCK_DIR", "/tmp/codex-locks")
	t.Setenv("CODEX_AGENT_QUEUE_PRIORITY_ENABLED", "true")
	t.Setenv("CODEX_CONTEXT_MANAGEMENT_ENABLED", "true")
	t.Setenv("CODEX_CONTEXT_MAX_BYTES", "12345")
	t.Setenv("CODEX_CONTEXT_MAX_MESSAGES", "77")
	t.Setenv("CODEX_CONTEXT_RECENT_MESSAGES", "9")
	t.Setenv("CODEX_CONTEXT_TOOL_OUTPUT_MAX_BYTES", "456")
	t.Setenv("CODEX_CONTEXT_COMPACTED_TOOL_OUTPUT_MAX_BYTES", "78")
	t.Setenv("CODEX_CLIENT_MAX_INFLIGHT", "4")
	t.Setenv("CODEX_CLIENT_POOL_UNAVAILABLE", "fallback_first")
	t.Setenv("CODEX_CLIENT_COOLDOWN_DEFAULT", "17s")
	t.Setenv("CODEX_CLIENTS", `[
		{"label":"primary","codex_home":"/tmp/codex-a","profile_path":"/tmp/profile-a.json","scaffold_path":"/tmp/scaffold-a.json"},
		{"label":"secondary","auth_path":"/tmp/auth-b.json","profile_path":"/tmp/profile-b.json","scaffold_path":"/tmp/scaffold-b.json"}
	]`)
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
	if !cfg.LogCodexToolEvents {
		t.Fatal("LogCodexToolEvents = false, want true")
	}
	if cfg.AgentQueueEnabled {
		t.Fatal("AgentQueueEnabled = true, want false")
	}
	if cfg.AgentMaxActive != 2 || cfg.AgentMaxActivePerKey != 2 || cfg.AgentQueueKeyMode != "header:x-cursor-session-id" || cfg.AgentQueueLimit != 7 || cfg.AgentQueueTimeout != 9*time.Second || cfg.AgentQueueLockDir != "/tmp/codex-locks" || !cfg.AgentQueuePriorityEnabled {
		t.Fatalf("agent queue config = enabled:%t max:%d max_per_key:%d key_mode:%q limit:%d timeout:%s lock_dir:%q priority:%t", cfg.AgentQueueEnabled, cfg.AgentMaxActive, cfg.AgentMaxActivePerKey, cfg.AgentQueueKeyMode, cfg.AgentQueueLimit, cfg.AgentQueueTimeout, cfg.AgentQueueLockDir, cfg.AgentQueuePriorityEnabled)
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
	if cfg.CodexClientPoolUnavailable != "fallback_first" {
		t.Fatalf("CodexClientPoolUnavailable = %q", cfg.CodexClientPoolUnavailable)
	}
	if cfg.CodexClientMaxInflight != 4 {
		t.Fatalf("CodexClientMaxInflight = %d, want 4", cfg.CodexClientMaxInflight)
	}
	if cfg.CodexClientCooldownDefault != 17*time.Second {
		t.Fatalf("CodexClientCooldownDefault = %s", cfg.CodexClientCooldownDefault)
	}
	if len(cfg.CodexClients) != 2 {
		t.Fatalf("CodexClients length = %d, want 2", len(cfg.CodexClients))
	}
	if cfg.CodexClients[0].Label != "primary" || cfg.CodexClients[0].AuthPath != filepath.Join("/tmp/codex-a", "auth.json") {
		t.Fatalf("first codex client = %#v", cfg.CodexClients[0])
	}
	if cfg.CodexClients[1].Label != "secondary" || cfg.CodexClients[1].CodexHome != "/tmp/codex-home" || cfg.CodexClients[1].AuthPath != "/tmp/auth-b.json" {
		t.Fatalf("second codex client = %#v", cfg.CodexClients[1])
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
		"--log-codex-tool-events",
		"--agent-queue-enabled=false",
		"--agent-max-active", "3",
		"--agent-max-active-per-key", "2",
		"--agent-queue-key-mode", "body:session_id",
		"--agent-queue-limit", "8",
		"--agent-queue-timeout", "10s",
		"--agent-queue-lock-dir", "/tmp/flag-locks",
		"--agent-queue-priority-enabled",
		"--context-management-enabled",
		"--context-max-bytes", "23456",
		"--context-max-messages", "88",
		"--context-recent-messages", "10",
		"--context-tool-output-max-bytes", "567",
		"--context-compacted-tool-output-max-bytes", "89",
		"--codex-client-max-inflight", "5",
		"--codex-client-pool-unavailable", "fallback_first",
		"--codex-client-cooldown-default", "19s",
		"--codex-clients", `[{"label":"flag-a","codex_home":"/tmp/flag-a"},{"label":"flag-b","auth_path":"/tmp/flag-b-auth.json"}]`,
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
	if !cfg.LogCodexToolEvents {
		t.Fatal("LogCodexToolEvents = false, want true")
	}
	if cfg.AgentQueueEnabled {
		t.Fatal("AgentQueueEnabled = true, want false")
	}
	if cfg.AgentMaxActive != 3 || cfg.AgentMaxActivePerKey != 2 || cfg.AgentQueueKeyMode != "body:session_id" || cfg.AgentQueueLimit != 8 || cfg.AgentQueueTimeout != 10*time.Second || cfg.AgentQueueLockDir != "/tmp/flag-locks" || !cfg.AgentQueuePriorityEnabled {
		t.Fatalf("agent queue config = enabled:%t max:%d max_per_key:%d key_mode:%q limit:%d timeout:%s lock_dir:%q priority:%t", cfg.AgentQueueEnabled, cfg.AgentMaxActive, cfg.AgentMaxActivePerKey, cfg.AgentQueueKeyMode, cfg.AgentQueueLimit, cfg.AgentQueueTimeout, cfg.AgentQueueLockDir, cfg.AgentQueuePriorityEnabled)
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
	if cfg.CodexClientPoolUnavailable != "fallback_first" {
		t.Fatalf("CodexClientPoolUnavailable = %q", cfg.CodexClientPoolUnavailable)
	}
	if cfg.CodexClientMaxInflight != 5 {
		t.Fatalf("CodexClientMaxInflight = %d, want 5", cfg.CodexClientMaxInflight)
	}
	if cfg.CodexClientCooldownDefault != 19*time.Second {
		t.Fatalf("CodexClientCooldownDefault = %s", cfg.CodexClientCooldownDefault)
	}
	if len(cfg.CodexClients) != 2 {
		t.Fatalf("CodexClients length = %d, want 2", len(cfg.CodexClients))
	}
	if cfg.CodexClients[0].Label != "flag-a" || cfg.CodexClients[0].AuthPath != filepath.Join("/tmp/flag-a", "auth.json") {
		t.Fatalf("first flag codex client = %#v", cfg.CodexClients[0])
	}
	if cfg.CodexClients[1].Label != "flag-b" || cfg.CodexClients[1].CodexHome != "/tmp/flag-codex" || cfg.CodexClients[1].AuthPath != "/tmp/flag-b-auth.json" {
		t.Fatalf("second flag codex client = %#v", cfg.CodexClients[1])
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

func TestLoadInvalidLogCodexToolEvents(t *testing.T) {
	t.Setenv("CODEX_LOG_CODEX_TOOL_EVENTS", "definitely")
	chdir(t, t.TempDir())

	if _, err := Load(nil); err == nil {
		t.Fatal("Load() error = nil, want invalid Codex tool-event log error")
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

func TestLoadInvalidAgentQueuePriorityEnabled(t *testing.T) {
	t.Setenv("CODEX_AGENT_QUEUE_PRIORITY_ENABLED", "definitely")
	chdir(t, t.TempDir())

	if _, err := Load(nil); err == nil {
		t.Fatal("Load() error = nil, want invalid agent queue priority enabled error")
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

func TestLoadInvalidCodexClients(t *testing.T) {
	tests := map[string]string{
		"empty":           `[]`,
		"duplicate_label": `[{"label":"same"},{"label":"same"}]`,
		"bad_label":       `[{"label":"secret/path"}]`,
		"bad_json":        `not-json`,
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			t.Setenv("CODEX_CLIENTS", value)
			chdir(t, t.TempDir())

			if _, err := Load(nil); err == nil {
				t.Fatal("Load() error = nil, want invalid codex clients error")
			}
		})
	}
}

func TestLoadInvalidCodexClientPoolUnavailable(t *testing.T) {
	t.Setenv("CODEX_CLIENT_POOL_UNAVAILABLE", "random")
	chdir(t, t.TempDir())

	if _, err := Load(nil); err == nil {
		t.Fatal("Load() error = nil, want invalid codex client pool unavailable error")
	}
}

func TestLoadInvalidCodexClientMaxInflight(t *testing.T) {
	tests := []string{"not-a-number", "0", "-1"}
	for _, value := range tests {
		t.Run(value, func(t *testing.T) {
			t.Setenv("CODEX_CLIENT_MAX_INFLIGHT", value)
			chdir(t, t.TempDir())

			if _, err := Load(nil); err == nil {
				t.Fatal("Load() error = nil, want invalid Codex client max inflight error")
			}
		})
	}
}

func TestLoadInvalidCodexClientCooldownDefault(t *testing.T) {
	for _, value := range []string{"0s", "-1s", "not-a-duration"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("CODEX_CLIENT_COOLDOWN_DEFAULT", value)
			chdir(t, t.TempDir())

			if _, err := Load(nil); err == nil {
				t.Fatal("Load() error = nil, want invalid cooldown error")
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

func TestClaudeDefaults(t *testing.T) {
	cfg := Defaults()
	if cfg.ClaudeExecutable != DefaultClaudeExecutable {
		t.Fatalf("ClaudeExecutable = %q", cfg.ClaudeExecutable)
	}
	if cfg.ClaudeDefaultModel != DefaultClaudeModel {
		t.Fatalf("ClaudeDefaultModel = %q", cfg.ClaudeDefaultModel)
	}
	if cfg.ClaudeTimeout != DefaultClaudeTimeout {
		t.Fatalf("ClaudeTimeout = %s", cfg.ClaudeTimeout)
	}
}

func TestLoadGatewayDefaults(t *testing.T) {
	unsetenv(t, "GATEWAY_BEARER_SECRET")
	unsetenv(t, "GATEWAY_PROVIDERS")
	unsetenv(t, "GATEWAY_TENANT_HEADER")
	chdir(t, t.TempDir())

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.GatewayBearerSecret != "" {
		t.Fatalf("GatewayBearerSecret = %q, want empty (auth off)", cfg.GatewayBearerSecret)
	}
	if len(cfg.GatewayProviders) != 3 {
		t.Fatalf("GatewayProviders = %v, want codex, gemini, claude", cfg.GatewayProviders)
	}
	for _, provider := range []string{"codex", "gemini", "claude"} {
		if !cfg.ProviderEnabled(provider) {
			t.Fatalf("ProviderEnabled(%q) = false, want true", provider)
		}
	}
	if cfg.GatewayTenantHeader != DefaultGatewayTenantHeader {
		t.Fatalf("GatewayTenantHeader = %q, want %q", cfg.GatewayTenantHeader, DefaultGatewayTenantHeader)
	}
}

func TestLoadGatewayEnvironment(t *testing.T) {
	t.Setenv("GATEWAY_BEARER_SECRET", "shared-secret")
	t.Setenv("GATEWAY_PROVIDERS", " Codex , GEMINI ,codex")
	t.Setenv("GATEWAY_TENANT_HEADER", "X-Custom-Tenant")
	chdir(t, t.TempDir())

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.GatewayBearerSecret != "shared-secret" {
		t.Fatalf("GatewayBearerSecret = %q, want shared-secret", cfg.GatewayBearerSecret)
	}
	if len(cfg.GatewayProviders) != 2 || cfg.GatewayProviders[0] != "codex" || cfg.GatewayProviders[1] != "gemini" {
		t.Fatalf("GatewayProviders = %v, want [codex gemini]", cfg.GatewayProviders)
	}
	if cfg.ProviderEnabled("claude") {
		t.Fatal("ProviderEnabled(claude) = true, want false")
	}
	if cfg.GatewayTenantHeader != "X-Custom-Tenant" {
		t.Fatalf("GatewayTenantHeader = %q, want X-Custom-Tenant", cfg.GatewayTenantHeader)
	}
}

func TestLoadGatewayFlagsOverrideEnvironment(t *testing.T) {
	t.Setenv("GATEWAY_BEARER_SECRET", "env-secret")
	t.Setenv("GATEWAY_PROVIDERS", "codex,gemini,claude")
	chdir(t, t.TempDir())

	cfg, err := Load([]string{
		"-gateway-bearer-secret", "flag-secret",
		"-gateway-providers", "codex,gemini",
		"-gateway-tenant-header", "X-Flag-Tenant",
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.GatewayBearerSecret != "flag-secret" {
		t.Fatalf("GatewayBearerSecret = %q, want flag-secret", cfg.GatewayBearerSecret)
	}
	if cfg.ProviderEnabled("claude") {
		t.Fatal("ProviderEnabled(claude) = true, want false")
	}
	if cfg.GatewayTenantHeader != "X-Flag-Tenant" {
		t.Fatalf("GatewayTenantHeader = %q, want X-Flag-Tenant", cfg.GatewayTenantHeader)
	}
}

func TestLoadGatewayInvalidProvider(t *testing.T) {
	t.Setenv("GATEWAY_PROVIDERS", "codex,openai")
	chdir(t, t.TempDir())

	if _, err := Load(nil); err == nil {
		t.Fatal("Load() error = nil, want unsupported provider error")
	}
}

func TestLoadGatewayProvidersRequireCodex(t *testing.T) {
	t.Setenv("GATEWAY_PROVIDERS", "gemini,claude")
	chdir(t, t.TempDir())

	if _, err := Load(nil); err == nil {
		t.Fatal("Load() error = nil, want codex-required error")
	}
}

func TestLoadGatewayEmptyProviders(t *testing.T) {
	t.Setenv("GATEWAY_PROVIDERS", " , ")
	chdir(t, t.TempDir())

	if _, err := Load(nil); err == nil {
		t.Fatal("Load() error = nil, want empty allowlist error")
	}
}

func TestProviderEnabledEmptyAllowlistMeansAll(t *testing.T) {
	cfg := Config{}
	for _, provider := range []string{"codex", "gemini", "claude"} {
		if !cfg.ProviderEnabled(provider) {
			t.Fatalf("ProviderEnabled(%q) = false, want true for empty allowlist", provider)
		}
	}
}

func TestStructuredInferenceDefaultsAreDark(t *testing.T) {
	cfg := Defaults()
	if cfg.StructuredEnabled {
		t.Fatal("StructuredEnabled = true, want the endpoint dark by default")
	}
	if len(cfg.StructuredModels) != 0 {
		t.Fatalf("StructuredModels = %v, want the built-in allowlist", cfg.StructuredModels)
	}
	if cfg.StructuredMaxActive != DefaultStructuredMaxActive ||
		cfg.StructuredMaxActivePerKey != DefaultStructuredMaxActivePerKey ||
		cfg.StructuredQueueLimit != DefaultStructuredQueueLimit ||
		cfg.StructuredQueueTimeout != DefaultStructuredQueueTimeout ||
		cfg.StructuredMaxDeadline != DefaultStructuredMaxDeadline ||
		cfg.StructuredIdempotencyTTL != DefaultStructuredIdempotencyTTL ||
		cfg.StructuredIdempotencyBackend != IdempotencyBackendMemory ||
		cfg.StructuredIdempotencyDir != "" ||
		cfg.StructuredReplicas != DefaultStructuredReplicas {
		t.Fatalf("structured defaults = %#v", cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestLoadStructuredEnvironmentAndFlags(t *testing.T) {
	t.Setenv("STRUCTURED_INFERENCE_ENABLED", "true")
	t.Setenv("STRUCTURED_MAX_ACTIVE", "9")
	t.Setenv("STRUCTURED_MAX_ACTIVE_PER_KEY", "3")
	t.Setenv("STRUCTURED_QUEUE_LIMIT", "7")
	t.Setenv("STRUCTURED_QUEUE_TIMEOUT", "11s")
	t.Setenv("STRUCTURED_MAX_DEADLINE", "90s")
	t.Setenv("STRUCTURED_IDEMPOTENCY_TTL", "2m")
	t.Setenv("STRUCTURED_IDEMPOTENCY_BACKEND", "file")
	t.Setenv("STRUCTURED_IDEMPOTENCY_DIR", "/var/lib/open-agent-api/structured-idempotency")
	t.Setenv("STRUCTURED_REPLICAS", "3")
	t.Setenv("STRUCTURED_MODELS", "gpt-5.6-sol, gpt-5.6-terra ,gpt-5.6-sol")
	chdir(t, t.TempDir())

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.StructuredEnabled || cfg.StructuredMaxActive != 9 || cfg.StructuredMaxActivePerKey != 3 ||
		cfg.StructuredQueueLimit != 7 || cfg.StructuredQueueTimeout != 11*time.Second ||
		cfg.StructuredMaxDeadline != 90*time.Second ||
		cfg.StructuredIdempotencyTTL != 2*time.Minute ||
		cfg.StructuredIdempotencyBackend != IdempotencyBackendFile ||
		cfg.StructuredIdempotencyDir != "/var/lib/open-agent-api/structured-idempotency" ||
		cfg.StructuredReplicas != 3 {
		t.Fatalf("structured config from environment = %#v", cfg)
	}
	if len(cfg.StructuredModels) != 2 || cfg.StructuredModels[0] != "gpt-5.6-sol" || cfg.StructuredModels[1] != "gpt-5.6-terra" {
		t.Fatalf("StructuredModels = %v, want the trimmed de-duplicated list", cfg.StructuredModels)
	}

	cfg, err = Load([]string{
		"--structured-enabled=false",
		"--structured-max-active=2",
		"--structured-models=gpt-5.6-luna",
		"--structured-idempotency-backend=memory",
		"--structured-idempotency-dir=",
		"--structured-replicas=1",
	})
	if err != nil {
		t.Fatalf("Load() with flags error = %v", err)
	}
	if cfg.StructuredEnabled || cfg.StructuredMaxActive != 2 ||
		cfg.StructuredIdempotencyBackend != IdempotencyBackendMemory ||
		cfg.StructuredIdempotencyDir != "" || cfg.StructuredReplicas != 1 {
		t.Fatalf("structured config from flags = %#v", cfg)
	}
	if len(cfg.StructuredModels) != 1 || cfg.StructuredModels[0] != "gpt-5.6-luna" {
		t.Fatalf("StructuredModels = %v, want the flag override", cfg.StructuredModels)
	}
}

// Issue 126 AC2: Codex Responses honours no output-token cap, so the knob that
// promised one is gone. Neither the environment variable nor the flag may be
// recognized again: a stale deploy that still sets it must not silently be
// believed, and the flag must fail loudly rather than be quietly accepted.
func TestLoadNoLongerRecognizesTheStructuredOutputTokenKnob(t *testing.T) {
	t.Setenv("STRUCTURED_INFERENCE_ENABLED", "true")
	t.Setenv("STRUCTURED_MAX_OUTPUT_TOKENS", "4096")
	chdir(t, t.TempDir())

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load() error = %v, want a stale environment variable to be ignored", err)
	}
	// Reflection, not a compile-time reference: the field is gone, so this is
	// the only way to keep asserting that it stays gone.
	if field, ok := reflect.TypeOf(cfg).FieldByName("StructuredMaxOutputTokens"); ok {
		t.Fatalf("Config still carries StructuredMaxOutputTokens: %#v", field)
	}
	// flag.ContinueOnError dumps the usage text to stderr on the way out; the
	// noise in the test log is the flag package proving the knob is undefined.
	if _, err := Load([]string{"--structured-max-output-tokens=4096"}); err == nil {
		t.Fatal("Load() accepted --structured-max-output-tokens, want the removed flag to be rejected")
	}
}

// Issue 120 AC1, AC5: the durable-store guard is fail-closed. A multi-replica
// structured deployment on the process-local store is rejected at load time,
// and the message names both remedies.
func TestLoadRejectsMultiReplicaWithoutADurableIdempotencyStore(t *testing.T) {
	t.Setenv("STRUCTURED_INFERENCE_ENABLED", "true")
	t.Setenv("STRUCTURED_REPLICAS", "2")
	chdir(t, t.TempDir())

	_, err := Load(nil)
	if err == nil {
		t.Fatal("Load() error = nil, want the multi-replica guard to fail closed")
	}
	for _, want := range []string{"STRUCTURED_IDEMPOTENCY_BACKEND=file", "STRUCTURED_IDEMPOTENCY_DIR", "single replica"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to mention %q", err, want)
		}
	}

	// The same deployment with a shared directory is allowed.
	t.Setenv("STRUCTURED_IDEMPOTENCY_BACKEND", "file")
	t.Setenv("STRUCTURED_IDEMPOTENCY_DIR", t.TempDir())
	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load() with the file backend error = %v", err)
	}
	if cfg.IdempotencyBackend() != IdempotencyBackendFile {
		t.Fatalf("IdempotencyBackend() = %q, want file", cfg.IdempotencyBackend())
	}
}

// The guard only binds a deployment that has actually turned the endpoint on:
// a dark gateway keeps every existing configuration valid.
func TestMultiReplicaGuardOnlyAppliesWhenStructuredIsEnabled(t *testing.T) {
	cfg := Defaults()
	cfg.StructuredReplicas = 4
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with structured off error = %v", err)
	}
	cfg.StructuredEnabled = true
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want the guard once structured is enabled")
	}
}

// AC1/AC2: the guard reads a declared replica count, so the limits it cannot
// see have to be said out loud instead of silently assumed away.
func TestStructuredIdempotencyWarningsCoverRollingUpdates(t *testing.T) {
	dark := Defaults()
	if warnings := dark.StructuredIdempotencyWarnings(); len(warnings) != 0 {
		t.Fatalf("warnings with structured off = %v, want none", warnings)
	}

	memory := Defaults()
	memory.StructuredEnabled = true
	warnings := memory.StructuredIdempotencyWarnings()
	if len(warnings) != 1 {
		t.Fatalf("memory-backend warnings = %v, want exactly one", warnings)
	}
	for _, want := range []string{"process-local", "rolling update", "maxSurge", "STRUCTURED_REPLICAS=1", "STRUCTURED_IDEMPOTENCY_BACKEND=file", "Recreate"} {
		if !strings.Contains(warnings[0], want) {
			t.Fatalf("memory warning = %q, want it to mention %q", warnings[0], want)
		}
	}

	file := memory
	file.StructuredIdempotencyBackend = IdempotencyBackendFile
	file.StructuredIdempotencyDir = t.TempDir()
	if err := file.Validate(); err != nil {
		t.Fatalf("Validate() with the file backend error = %v", err)
	}
	warnings = file.StructuredIdempotencyWarnings()
	if len(warnings) != 1 {
		t.Fatalf("file-backend warnings = %v, want exactly one", warnings)
	}
	for _, want := range []string{"declared count", "HPA", "drift"} {
		if !strings.Contains(warnings[0], want) {
			t.Fatalf("file warning = %q, want it to mention %q", warnings[0], want)
		}
	}

	// Warnings are advisory only: nothing they describe changes what validates.
	if err := memory.Validate(); err != nil {
		t.Fatalf("Validate() with the memory backend error = %v, want warnings not to reject", err)
	}
}

// A zero-value backend means the default, so a hand-built Config still passes.
func TestIdempotencyBackendNormalizesTheConfiguredValue(t *testing.T) {
	for value, want := range map[string]string{
		"":       IdempotencyBackendMemory,
		"  ":     IdempotencyBackendMemory,
		"FILE":   IdempotencyBackendFile,
		" file ": IdempotencyBackendFile,
		"memory": IdempotencyBackendMemory,
	} {
		cfg := Config{StructuredIdempotencyBackend: value}
		if got := cfg.IdempotencyBackend(); got != want {
			t.Fatalf("IdempotencyBackend(%q) = %q, want %q", value, got, want)
		}
	}
}

func TestLoadInvalidStructuredValues(t *testing.T) {
	for name, env := range map[string][2]string{
		"enabled":                    {"STRUCTURED_INFERENCE_ENABLED", "sometimes"},
		"max active":                 {"STRUCTURED_MAX_ACTIVE", "many"},
		"queue timeout":              {"STRUCTURED_QUEUE_TIMEOUT", "soon"},
		"zero max active":            {"STRUCTURED_MAX_ACTIVE", "0"},
		"zero deadline":              {"STRUCTURED_MAX_DEADLINE", "0s"},
		"zero ttl":                   {"STRUCTURED_IDEMPOTENCY_TTL", "0s"},
		"unknown backend":            {"STRUCTURED_IDEMPOTENCY_BACKEND", "redis"},
		"file backend without a dir": {"STRUCTURED_IDEMPOTENCY_BACKEND", "file"},
		"negative replicas":          {"STRUCTURED_REPLICAS", "-1"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(env[0], env[1])
			chdir(t, t.TempDir())
			if _, err := Load(nil); err == nil {
				t.Fatalf("Load() with %s=%s error = nil, want a validation error", env[0], env[1])
			}
		})
	}
}
