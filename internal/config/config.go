package config

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

const (
	DefaultHost                               = "127.0.0.1"
	DefaultPort                               = 8088
	DefaultCodexWebsocketURL                  = "wss://chatgpt.com/backend-api/codex/responses"
	DefaultCodexTimeout                       = 10 * time.Minute
	DefaultGeminiEndpoint                     = "https://daily-cloudcode-pa.googleapis.com/v1internal"
	DefaultGeminiTimeout                      = 10 * time.Minute
	DefaultClaudeExecutable                   = "claude"
	DefaultClaudeModel                        = "sonnet"
	DefaultClaudeTimeout                      = 10 * time.Minute
	DefaultStreamIdleTimeout                  = 90 * time.Second
	DefaultCustomToolWire                     = "function"
	DefaultQuotaFallbackModel                 = "gpt-5.3-codex-spark"
	DefaultAgentQueueEnabled                  = true
	DefaultAgentMaxActive                     = 2
	DefaultAgentMaxActivePerKey               = 1
	DefaultAgentQueueKeyMode                  = "cursor"
	DefaultAgentQueueLimit                    = 20
	DefaultAgentQueueTimeout                  = 5 * time.Minute
	DefaultAgentQueuePriorityEnabled          = false
	DefaultContextManagementEnabled           = true
	DefaultContextMaxBytes                    = 192 * 1024
	DefaultContextMaxMessages                 = 120
	DefaultContextRecentMessages              = 24
	DefaultContextToolOutputMaxBytes          = 32 * 1024
	DefaultContextCompactedToolOutputMaxBytes = 512
	DefaultDegenerateTurnRetryEnabled         = true
	DefaultCodexClientMaxInflight             = 2
	DefaultCodexClientPoolUnavailable         = "fail"
	DefaultCodexClientCooldownDefault         = 5 * time.Minute
	DefaultMetricsEnabled                     = true
	DefaultGatewayTenantHeader                = "X-Smore-Tenant-ID"
	// Structured inference ships dark. The route is not registered unless it is
	// explicitly enabled, so the pre-ticket public surface is byte-identical.
	DefaultStructuredEnabled         = false
	DefaultStructuredMaxActive       = 4
	DefaultStructuredMaxActivePerKey = 2
	DefaultStructuredQueueLimit      = 16
	DefaultStructuredQueueTimeout    = 30 * time.Second
	DefaultStructuredMaxDeadline     = 5 * time.Minute
	DefaultStructuredIdempotencyTTL  = 10 * time.Minute
	DefaultStructuredReplicas        = 1
)

// DefaultGatewayProviders enables every provider so the local Cursor workflow
// keeps its current behavior unless a deploy narrows the allowlist.
func DefaultGatewayProviders() []string {
	return []string{"codex", "gemini", "claude"}
}

type Config struct {
	Host                               string
	Port                               int
	CodexHome                          string
	AuthPath                           string
	CodexProfilePath                   string
	CodexScaffoldPath                  string
	CodexWebsocketURL                  string
	CodexTimeout                       time.Duration
	GeminiAuthPath                     string
	GeminiEndpoint                     string
	GeminiProject                      string
	GeminiTimeout                      time.Duration
	ClaudeExecutable                   string
	ClaudeDefaultModel                 string
	ClaudeTimeout                      time.Duration
	StreamIdleTimeout                  time.Duration
	CustomToolWire                     string
	QuotaFallbackModel                 string
	LogBodyShape                       bool
	LogRequestIdentity                 bool
	LogCodexToolEvents                 bool
	AgentQueueEnabled                  bool
	AgentMaxActive                     int
	AgentMaxActivePerKey               int
	AgentQueueKeyMode                  string
	AgentQueueLimit                    int
	GeminiAgentMaxActive               int
	GeminiAgentMaxActivePerKey         int
	GeminiAgentQueueLimit              int
	ClaudeAgentMaxActive               int
	ClaudeAgentMaxActivePerKey         int
	ClaudeAgentQueueLimit              int
	AgentQueueTimeout                  time.Duration
	AgentQueueLockDir                  string
	AgentQueuePriorityEnabled          bool
	ContextManagementEnabled           bool
	ContextMaxBytes                    int
	ContextMaxMessages                 int
	ContextRecentMessages              int
	ContextToolOutputMaxBytes          int
	ContextCompactedToolOutputMaxBytes int
	DegenerateTurnRetryEnabled         bool
	CodexClients                       []CodexClient
	CodexClientMaxInflight             int
	CodexClientPoolUnavailable         string
	CodexClientCooldownDefault         time.Duration
	MetricsEnabled                     bool
	GatewayBearerSecret                string
	GatewayProviders                   []string
	GatewayTenantHeader                string
	// StructuredEnabled registers POST /v1/structured/inference. Default false:
	// the endpoint is dark until a deploy opts in, so enabling it is a
	// deliberate act and never a traffic cutover side effect.
	StructuredEnabled bool
	// Structured admission limits. They are a dedicated budget so structured
	// traffic can never consume the Cursor/agent queue.
	StructuredMaxActive       int
	StructuredMaxActivePerKey int
	StructuredQueueLimit      int
	StructuredQueueTimeout    time.Duration
	// StructuredMaxDeadline caps the caller-supplied deadline_ms.
	StructuredMaxDeadline time.Duration
	// StructuredIdempotencyTTL is how long a stored response can be replayed.
	StructuredIdempotencyTTL time.Duration
	// StructuredReplicas is required to be exactly one. Idempotency is
	// process-local, so deployments must also prevent rollout overlap.
	StructuredReplicas int
	// StructuredModels overrides the structured-capable model allowlist. Empty
	// means the built-in allowlist in internal/structured.
	StructuredModels []string
}

type CodexClient struct {
	Label             string `json:"label"`
	CodexHome         string `json:"codex_home"`
	AuthPath          string `json:"auth_path"`
	CodexProfilePath  string `json:"profile_path"`
	CodexScaffoldPath string `json:"scaffold_path"`
}

func Load(args []string) (Config, error) {
	cfg := Defaults()
	clientsJSON := ""

	if err := loadDotEnv(".env"); err != nil {
		return Config{}, err
	}

	if value := os.Getenv("CODEX_CHAT_API_HOST"); value != "" {
		cfg.Host = value
	} else if value := os.Getenv("HOST"); value != "" {
		cfg.Host = value
	}

	if value := os.Getenv("CODEX_CHAT_API_PORT"); value != "" {
		port, err := parsePort(value)
		if err != nil {
			return Config{}, fmt.Errorf("CODEX_CHAT_API_PORT: %w", err)
		}
		cfg.Port = port
	} else if value := os.Getenv("PORT"); value != "" {
		port, err := parsePort(value)
		if err != nil {
			return Config{}, fmt.Errorf("PORT: %w", err)
		}
		cfg.Port = port
	}

	if value := os.Getenv("CODEX_HOME"); value != "" {
		cfg.CodexHome = value
		cfg.AuthPath = filepath.Join(value, "auth.json")
	}
	if value := os.Getenv("CODEX_AUTH_PATH"); value != "" {
		cfg.AuthPath = value
	}
	if value := os.Getenv("CODEX_PROFILE_PATH"); value != "" {
		cfg.CodexProfilePath = value
	}
	if value := os.Getenv("CODEX_SCAFFOLD_PATH"); value != "" {
		cfg.CodexScaffoldPath = value
	}
	if value := os.Getenv("CODEX_WEBSOCKET_URL"); value != "" {
		cfg.CodexWebsocketURL = value
	}
	if value := os.Getenv("CODEX_TIMEOUT"); value != "" {
		timeout, err := time.ParseDuration(value)
		if err != nil {
			return Config{}, fmt.Errorf("CODEX_TIMEOUT: %w", err)
		}
		cfg.CodexTimeout = timeout
	}
	if value := os.Getenv("GEMINI_AUTH_PATH"); value != "" {
		cfg.GeminiAuthPath = value
	}
	if value := os.Getenv("GEMINI_ENDPOINT"); value != "" {
		cfg.GeminiEndpoint = value
	}
	if value := os.Getenv("GEMINI_PROJECT"); value != "" {
		cfg.GeminiProject = value
	}
	if value := os.Getenv("GEMINI_TIMEOUT"); value != "" {
		timeout, err := time.ParseDuration(value)
		if err != nil {
			return Config{}, fmt.Errorf("GEMINI_TIMEOUT: %w", err)
		}
		cfg.GeminiTimeout = timeout
	}
	if value := os.Getenv("CLAUDE_EXECUTABLE"); value != "" {
		cfg.ClaudeExecutable = value
	}
	if value := os.Getenv("CLAUDE_PATH"); value != "" {
		cfg.ClaudeExecutable = value
	}
	if value := os.Getenv("CLAUDE_DEFAULT_MODEL"); value != "" {
		cfg.ClaudeDefaultModel = value
	}
	if value := os.Getenv("CLAUDE_TIMEOUT"); value != "" {
		timeout, err := time.ParseDuration(value)
		if err != nil {
			return Config{}, fmt.Errorf("CLAUDE_TIMEOUT: %w", err)
		}
		cfg.ClaudeTimeout = timeout
	}
	if value := os.Getenv("CODEX_CUSTOM_TOOL_WIRE"); value != "" {
		cfg.CustomToolWire = value
	}
	if value, ok := os.LookupEnv("CODEX_QUOTA_FALLBACK_MODEL"); ok {
		cfg.QuotaFallbackModel = value
	}
	if value := os.Getenv("CODEX_STREAM_IDLE_TIMEOUT"); value != "" {
		timeout, err := time.ParseDuration(value)
		if err != nil {
			return Config{}, fmt.Errorf("CODEX_STREAM_IDLE_TIMEOUT: %w", err)
		}
		cfg.StreamIdleTimeout = timeout
	}
	if value := os.Getenv("CODEX_LOG_BODY_SHAPE"); value != "" {
		logBodyShape, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("CODEX_LOG_BODY_SHAPE: %w", err)
		}
		cfg.LogBodyShape = logBodyShape
	}
	if value := os.Getenv("CODEX_LOG_REQUEST_IDENTITY"); value != "" {
		logRequestIdentity, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("CODEX_LOG_REQUEST_IDENTITY: %w", err)
		}
		cfg.LogRequestIdentity = logRequestIdentity
	}
	if value := os.Getenv("CODEX_LOG_CODEX_TOOL_EVENTS"); value != "" {
		logCodexToolEvents, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("CODEX_LOG_CODEX_TOOL_EVENTS: %w", err)
		}
		cfg.LogCodexToolEvents = logCodexToolEvents
	}
	if value := os.Getenv("CODEX_AGENT_QUEUE_ENABLED"); value != "" {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("CODEX_AGENT_QUEUE_ENABLED: %w", err)
		}
		cfg.AgentQueueEnabled = enabled
	}
	if value := os.Getenv("CODEX_AGENT_MAX_ACTIVE"); value != "" {
		maxActive, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("CODEX_AGENT_MAX_ACTIVE: %w", err)
		}
		cfg.AgentMaxActive = maxActive
	}
	if value := os.Getenv("CODEX_AGENT_MAX_ACTIVE_PER_KEY"); value != "" {
		maxActivePerKey, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("CODEX_AGENT_MAX_ACTIVE_PER_KEY: %w", err)
		}
		cfg.AgentMaxActivePerKey = maxActivePerKey
	}
	if value := os.Getenv("CODEX_AGENT_QUEUE_KEY_MODE"); value != "" {
		cfg.AgentQueueKeyMode = value
	}
	if value := os.Getenv("CODEX_AGENT_QUEUE_LIMIT"); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("CODEX_AGENT_QUEUE_LIMIT: %w", err)
		}
		cfg.AgentQueueLimit = limit
	}
	for _, override := range []struct {
		env    string
		target *int
	}{
		{"GEMINI_AGENT_MAX_ACTIVE", &cfg.GeminiAgentMaxActive},
		{"GEMINI_AGENT_MAX_ACTIVE_PER_KEY", &cfg.GeminiAgentMaxActivePerKey},
		{"GEMINI_AGENT_QUEUE_LIMIT", &cfg.GeminiAgentQueueLimit},
		{"CLAUDE_AGENT_MAX_ACTIVE", &cfg.ClaudeAgentMaxActive},
		{"CLAUDE_AGENT_MAX_ACTIVE_PER_KEY", &cfg.ClaudeAgentMaxActivePerKey},
		{"CLAUDE_AGENT_QUEUE_LIMIT", &cfg.ClaudeAgentQueueLimit},
	} {
		if value := os.Getenv(override.env); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return Config{}, fmt.Errorf("%s: %w", override.env, err)
			}
			*override.target = parsed
		}
	}
	if value := os.Getenv("CODEX_AGENT_QUEUE_TIMEOUT"); value != "" {
		timeout, err := time.ParseDuration(value)
		if err != nil {
			return Config{}, fmt.Errorf("CODEX_AGENT_QUEUE_TIMEOUT: %w", err)
		}
		cfg.AgentQueueTimeout = timeout
	}
	if value := os.Getenv("CODEX_AGENT_QUEUE_LOCK_DIR"); value != "" {
		cfg.AgentQueueLockDir = value
	}
	if value := os.Getenv("CODEX_AGENT_QUEUE_PRIORITY_ENABLED"); value != "" {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("CODEX_AGENT_QUEUE_PRIORITY_ENABLED: %w", err)
		}
		cfg.AgentQueuePriorityEnabled = enabled
	}
	if value := os.Getenv("CODEX_CONTEXT_MANAGEMENT_ENABLED"); value != "" {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("CODEX_CONTEXT_MANAGEMENT_ENABLED: %w", err)
		}
		cfg.ContextManagementEnabled = enabled
	}
	if value := os.Getenv("CODEX_CONTEXT_MAX_BYTES"); value != "" {
		maxBytes, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("CODEX_CONTEXT_MAX_BYTES: %w", err)
		}
		cfg.ContextMaxBytes = maxBytes
	}
	if value := os.Getenv("CODEX_CONTEXT_MAX_MESSAGES"); value != "" {
		maxMessages, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("CODEX_CONTEXT_MAX_MESSAGES: %w", err)
		}
		cfg.ContextMaxMessages = maxMessages
	}
	if value := os.Getenv("CODEX_CONTEXT_RECENT_MESSAGES"); value != "" {
		recentMessages, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("CODEX_CONTEXT_RECENT_MESSAGES: %w", err)
		}
		cfg.ContextRecentMessages = recentMessages
	}
	if value := os.Getenv("CODEX_CONTEXT_TOOL_OUTPUT_MAX_BYTES"); value != "" {
		maxBytes, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("CODEX_CONTEXT_TOOL_OUTPUT_MAX_BYTES: %w", err)
		}
		cfg.ContextToolOutputMaxBytes = maxBytes
	}
	if value := os.Getenv("CODEX_CONTEXT_COMPACTED_TOOL_OUTPUT_MAX_BYTES"); value != "" {
		maxBytes, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("CODEX_CONTEXT_COMPACTED_TOOL_OUTPUT_MAX_BYTES: %w", err)
		}
		cfg.ContextCompactedToolOutputMaxBytes = maxBytes
	}
	if value := os.Getenv("CODEX_DEGENERATE_TURN_RETRY"); value != "" {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("CODEX_DEGENERATE_TURN_RETRY: %w", err)
		}
		cfg.DegenerateTurnRetryEnabled = enabled
	}
	if value := os.Getenv("CODEX_CLIENTS"); value != "" {
		clientsJSON = value
	}
	if value := os.Getenv("CODEX_CLIENT_MAX_INFLIGHT"); value != "" {
		maxInflight, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("CODEX_CLIENT_MAX_INFLIGHT: %w", err)
		}
		cfg.CodexClientMaxInflight = maxInflight
	}
	if value := os.Getenv("CODEX_CLIENT_POOL_UNAVAILABLE"); value != "" {
		cfg.CodexClientPoolUnavailable = value
	}
	if value := os.Getenv("CODEX_CLIENT_COOLDOWN_DEFAULT"); value != "" {
		cooldown, err := time.ParseDuration(value)
		if err != nil {
			return Config{}, fmt.Errorf("CODEX_CLIENT_COOLDOWN_DEFAULT: %w", err)
		}
		cfg.CodexClientCooldownDefault = cooldown
	}
	if value := os.Getenv("CODEX_METRICS_ENABLED"); value != "" {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("CODEX_METRICS_ENABLED: %w", err)
		}
		cfg.MetricsEnabled = enabled
	}
	if value := os.Getenv("GATEWAY_BEARER_SECRET"); value != "" {
		cfg.GatewayBearerSecret = value
	}
	providersRaw := strings.Join(cfg.GatewayProviders, ",")
	if value := os.Getenv("GATEWAY_PROVIDERS"); value != "" {
		providersRaw = value
	}
	if value := os.Getenv("GATEWAY_TENANT_HEADER"); value != "" {
		cfg.GatewayTenantHeader = value
	}
	if value := os.Getenv("STRUCTURED_INFERENCE_ENABLED"); value != "" {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("STRUCTURED_INFERENCE_ENABLED: %w", err)
		}
		cfg.StructuredEnabled = enabled
	}
	for _, override := range []struct {
		env    string
		target *int
	}{
		{"STRUCTURED_MAX_ACTIVE", &cfg.StructuredMaxActive},
		{"STRUCTURED_MAX_ACTIVE_PER_KEY", &cfg.StructuredMaxActivePerKey},
		{"STRUCTURED_QUEUE_LIMIT", &cfg.StructuredQueueLimit},
		{"STRUCTURED_REPLICAS", &cfg.StructuredReplicas},
	} {
		if value := os.Getenv(override.env); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return Config{}, fmt.Errorf("%s: %w", override.env, err)
			}
			*override.target = parsed
		}
	}
	for _, override := range []struct {
		env    string
		target *time.Duration
	}{
		{"STRUCTURED_QUEUE_TIMEOUT", &cfg.StructuredQueueTimeout},
		{"STRUCTURED_MAX_DEADLINE", &cfg.StructuredMaxDeadline},
		{"STRUCTURED_IDEMPOTENCY_TTL", &cfg.StructuredIdempotencyTTL},
	} {
		if value := os.Getenv(override.env); value != "" {
			parsed, err := time.ParseDuration(value)
			if err != nil {
				return Config{}, fmt.Errorf("%s: %w", override.env, err)
			}
			*override.target = parsed
		}
	}
	structuredModelsRaw := strings.Join(cfg.StructuredModels, ",")
	if value := os.Getenv("STRUCTURED_MODELS"); value != "" {
		structuredModelsRaw = value
	}

	fs := flag.NewFlagSet("open-agent-api", flag.ContinueOnError)
	fs.StringVar(&cfg.Host, "host", cfg.Host, "host address to bind")
	fs.IntVar(&cfg.Port, "port", cfg.Port, "port to bind")
	fs.StringVar(&cfg.CodexHome, "codex-home", cfg.CodexHome, "Codex home directory")
	fs.StringVar(&cfg.AuthPath, "auth-path", cfg.AuthPath, "Codex auth.json path")
	fs.StringVar(&cfg.CodexProfilePath, "codex-profile", cfg.CodexProfilePath, "Codex profile JSON path")
	fs.StringVar(&cfg.CodexScaffoldPath, "codex-scaffold", cfg.CodexScaffoldPath, "Codex scaffold JSON path")
	fs.StringVar(&cfg.CodexWebsocketURL, "codex-websocket-url", cfg.CodexWebsocketURL, "Codex websocket URL")
	fs.DurationVar(&cfg.CodexTimeout, "codex-timeout", cfg.CodexTimeout, "Codex websocket request timeout")
	fs.StringVar(&cfg.GeminiAuthPath, "gemini-auth-path", cfg.GeminiAuthPath, "Gemini/Antigravity oauth_creds.json path")
	fs.StringVar(&cfg.GeminiEndpoint, "gemini-endpoint", cfg.GeminiEndpoint, "Gemini Code Assist v1internal endpoint")
	fs.StringVar(&cfg.GeminiProject, "gemini-project", cfg.GeminiProject, "Gemini Code Assist project override")
	fs.DurationVar(&cfg.GeminiTimeout, "gemini-timeout", cfg.GeminiTimeout, "Gemini Code Assist request timeout")
	fs.StringVar(&cfg.ClaudeExecutable, "claude-executable", cfg.ClaudeExecutable, "Claude Code executable path")
	fs.StringVar(&cfg.ClaudeDefaultModel, "claude-default-model", cfg.ClaudeDefaultModel, "Claude Code default model")
	fs.DurationVar(&cfg.ClaudeTimeout, "claude-timeout", cfg.ClaudeTimeout, "Claude Code request timeout")
	fs.DurationVar(&cfg.StreamIdleTimeout, "stream-idle-timeout", cfg.StreamIdleTimeout, "maximum silence between upstream stream events before the request is failed (0 disables)")
	fs.BoolVar(&cfg.LogBodyShape, "log-body-shape", cfg.LogBodyShape, "log redacted JSON request body shape")
	fs.BoolVar(&cfg.LogRequestIdentity, "log-request-identity", cfg.LogRequestIdentity, "log redacted request identity diagnostics")
	fs.BoolVar(&cfg.LogCodexToolEvents, "log-codex-tool-events", cfg.LogCodexToolEvents, "log redacted upstream Codex tool-event diagnostics")
	fs.BoolVar(&cfg.AgentQueueEnabled, "agent-queue-enabled", cfg.AgentQueueEnabled, "enable Agent queue for requests with tools")
	fs.IntVar(&cfg.AgentMaxActive, "agent-max-active", cfg.AgentMaxActive, "maximum concurrent tool-capable Agent requests")
	fs.IntVar(&cfg.AgentMaxActivePerKey, "agent-max-active-per-key", cfg.AgentMaxActivePerKey, "maximum concurrent tool-capable Agent requests per queue key")
	fs.StringVar(&cfg.AgentQueueKeyMode, "agent-queue-key-mode", cfg.AgentQueueKeyMode, "Agent queue key mode: cursor, global, auth_hash, request_fingerprint, header:<name>, or body:<field>")
	fs.IntVar(&cfg.AgentQueueLimit, "agent-queue-limit", cfg.AgentQueueLimit, "maximum waiting tool-capable Agent requests")
	fs.DurationVar(&cfg.AgentQueueTimeout, "agent-queue-timeout", cfg.AgentQueueTimeout, "maximum time a tool-capable Agent request can wait in the queue")
	fs.StringVar(&cfg.AgentQueueLockDir, "agent-queue-lock-dir", cfg.AgentQueueLockDir, "directory for cross-process Agent queue key locks")
	fs.BoolVar(&cfg.AgentQueuePriorityEnabled, "agent-queue-priority-enabled", cfg.AgentQueuePriorityEnabled, "experimentally prioritize low-risk eligible Agent queue waiters")
	fs.BoolVar(&cfg.ContextManagementEnabled, "context-management-enabled", cfg.ContextManagementEnabled, "enable context management for tool-capable minimal-mode requests")
	fs.IntVar(&cfg.ContextMaxBytes, "context-max-bytes", cfg.ContextMaxBytes, "approximate message context byte threshold before compaction")
	fs.IntVar(&cfg.ContextMaxMessages, "context-max-messages", cfg.ContextMaxMessages, "message count threshold before compaction")
	fs.IntVar(&cfg.ContextRecentMessages, "context-recent-messages", cfg.ContextRecentMessages, "recent messages to leave unchanged during compaction")
	fs.IntVar(&cfg.ContextToolOutputMaxBytes, "context-tool-output-max-bytes", cfg.ContextToolOutputMaxBytes, "maximum bytes to keep from an individual tool output before adding a truncation marker")
	fs.IntVar(&cfg.ContextCompactedToolOutputMaxBytes, "context-compacted-tool-output-max-bytes", cfg.ContextCompactedToolOutputMaxBytes, "maximum bytes to keep from an older compacted tool output")
	fs.BoolVar(&cfg.DegenerateTurnRetryEnabled, "degenerate-turn-retry", cfg.DegenerateTurnRetryEnabled, "retry tool-capable turns that finish with text-only stop using tool_choice required")
	fs.StringVar(&clientsJSON, "codex-clients", clientsJSON, "JSON array of Codex clients with non-sensitive labels and optional codex_home, auth_path, profile_path, and scaffold_path")
	fs.IntVar(&cfg.CodexClientMaxInflight, "codex-client-max-inflight", cfg.CodexClientMaxInflight, "maximum concurrent requests per Codex client")
	fs.StringVar(&cfg.CodexClientPoolUnavailable, "codex-client-pool-unavailable", cfg.CodexClientPoolUnavailable, "Codex client pool unavailable policy: fail or fallback_first")
	fs.DurationVar(&cfg.CodexClientCooldownDefault, "codex-client-cooldown-default", cfg.CodexClientCooldownDefault, "default cooldown for rate-limited Codex clients when no retry hint is available")
	fs.BoolVar(&cfg.MetricsEnabled, "metrics-enabled", cfg.MetricsEnabled, "expose Prometheus metrics on /metrics")
	fs.StringVar(&cfg.GatewayBearerSecret, "gateway-bearer-secret", cfg.GatewayBearerSecret, "shared bearer secret required on /v1 routes (empty disables inbound auth)")
	fs.StringVar(&providersRaw, "gateway-providers", providersRaw, "comma-separated provider allowlist: codex, gemini, claude (codex is required)")
	fs.StringVar(&cfg.GatewayTenantHeader, "gateway-tenant-header", cfg.GatewayTenantHeader, "request header whose value overrides agent queue affinity per tenant")
	fs.BoolVar(&cfg.StructuredEnabled, "structured-enabled", cfg.StructuredEnabled, "register POST /v1/structured/inference (default off)")
	fs.IntVar(&cfg.StructuredMaxActive, "structured-max-active", cfg.StructuredMaxActive, "maximum concurrent structured inference requests")
	fs.IntVar(&cfg.StructuredMaxActivePerKey, "structured-max-active-per-key", cfg.StructuredMaxActivePerKey, "maximum concurrent structured inference requests per caller")
	fs.IntVar(&cfg.StructuredQueueLimit, "structured-queue-limit", cfg.StructuredQueueLimit, "maximum waiting structured inference requests")
	fs.DurationVar(&cfg.StructuredQueueTimeout, "structured-queue-timeout", cfg.StructuredQueueTimeout, "maximum time a structured inference request can wait for admission")
	fs.DurationVar(&cfg.StructuredMaxDeadline, "structured-max-deadline", cfg.StructuredMaxDeadline, "upper bound applied to the caller-supplied structured deadline_ms")
	fs.DurationVar(&cfg.StructuredIdempotencyTTL, "structured-idempotency-ttl", cfg.StructuredIdempotencyTTL, "how long a structured response can be replayed for the same idempotency key")
	fs.IntVar(&cfg.StructuredReplicas, "structured-replicas", cfg.StructuredReplicas, "gateway replica count; structured inference requires exactly one replica")
	fs.StringVar(&structuredModelsRaw, "structured-models", structuredModelsRaw, "comma-separated structured-capable model allowlist (empty uses the built-in list)")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	visitedFlags := map[string]bool{}
	fs.Visit(func(f *flag.Flag) {
		visitedFlags[f.Name] = true
	})
	if visitedFlags["codex-home"] && !visitedFlags["auth-path"] {
		cfg.AuthPath = filepath.Join(cfg.CodexHome, "auth.json")
	}
	if cfg.AuthPath == "" {
		cfg.AuthPath = filepath.Join(cfg.CodexHome, "auth.json")
	}
	cfg.GatewayProviders = parseGatewayProviders(providersRaw)
	cfg.StructuredModels = parseCommaList(structuredModelsRaw)
	if clientsJSON != "" {
		clients, err := parseCodexClients(clientsJSON, cfg)
		if err != nil {
			return Config{}, err
		}
		cfg.CodexClients = clients
	} else {
		cfg.CodexClients = []CodexClient{cfg.defaultCodexClient()}
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func Defaults() Config {
	codexHome := defaultCodexHome()
	cfg := Config{
		Host:                               DefaultHost,
		Port:                               DefaultPort,
		CodexHome:                          codexHome,
		AuthPath:                           filepath.Join(codexHome, "auth.json"),
		CodexProfilePath:                   "codex_profile.json",
		CodexScaffoldPath:                  "codex_scaffold.json",
		CodexWebsocketURL:                  DefaultCodexWebsocketURL,
		CodexTimeout:                       DefaultCodexTimeout,
		GeminiAuthPath:                     defaultGeminiAuthPath(),
		GeminiEndpoint:                     DefaultGeminiEndpoint,
		GeminiTimeout:                      DefaultGeminiTimeout,
		ClaudeExecutable:                   DefaultClaudeExecutable,
		ClaudeDefaultModel:                 DefaultClaudeModel,
		ClaudeTimeout:                      DefaultClaudeTimeout,
		AgentQueueEnabled:                  DefaultAgentQueueEnabled,
		AgentMaxActive:                     DefaultAgentMaxActive,
		AgentMaxActivePerKey:               DefaultAgentMaxActivePerKey,
		AgentQueueKeyMode:                  DefaultAgentQueueKeyMode,
		AgentQueueLimit:                    DefaultAgentQueueLimit,
		AgentQueueTimeout:                  DefaultAgentQueueTimeout,
		AgentQueueLockDir:                  "",
		AgentQueuePriorityEnabled:          DefaultAgentQueuePriorityEnabled,
		ContextManagementEnabled:           DefaultContextManagementEnabled,
		ContextMaxBytes:                    DefaultContextMaxBytes,
		ContextMaxMessages:                 DefaultContextMaxMessages,
		ContextRecentMessages:              DefaultContextRecentMessages,
		ContextToolOutputMaxBytes:          DefaultContextToolOutputMaxBytes,
		ContextCompactedToolOutputMaxBytes: DefaultContextCompactedToolOutputMaxBytes,
		DegenerateTurnRetryEnabled:         DefaultDegenerateTurnRetryEnabled,
		CodexClientMaxInflight:             DefaultCodexClientMaxInflight,
		CodexClientPoolUnavailable:         DefaultCodexClientPoolUnavailable,
		CodexClientCooldownDefault:         DefaultCodexClientCooldownDefault,
		MetricsEnabled:                     DefaultMetricsEnabled,
		StreamIdleTimeout:                  DefaultStreamIdleTimeout,
		CustomToolWire:                     DefaultCustomToolWire,
		QuotaFallbackModel:                 DefaultQuotaFallbackModel,
		GatewayProviders:                   DefaultGatewayProviders(),
		GatewayTenantHeader:                DefaultGatewayTenantHeader,
		StructuredEnabled:                  DefaultStructuredEnabled,
		StructuredMaxActive:                DefaultStructuredMaxActive,
		StructuredMaxActivePerKey:          DefaultStructuredMaxActivePerKey,
		StructuredQueueLimit:               DefaultStructuredQueueLimit,
		StructuredQueueTimeout:             DefaultStructuredQueueTimeout,
		StructuredMaxDeadline:              DefaultStructuredMaxDeadline,
		StructuredIdempotencyTTL:           DefaultStructuredIdempotencyTTL,
		StructuredReplicas:                 DefaultStructuredReplicas,
	}
	cfg.CodexClients = []CodexClient{cfg.defaultCodexClient()}
	return cfg
}

func (c Config) Addr() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

type AgentQueueLimits struct {
	MaxActive       int
	MaxActivePerKey int
	QueueLimit      int
}

// AgentQueueLimitsFor returns the queue limits for a provider. Each provider
// has its own queue, so the caps apply per provider; gemini and claude
// inherit the codex/global values unless explicitly overridden (0 = inherit).
func (c Config) AgentQueueLimitsFor(provider string) AgentQueueLimits {
	limits := AgentQueueLimits{
		MaxActive:       c.AgentMaxActive,
		MaxActivePerKey: c.AgentMaxActivePerKey,
		QueueLimit:      c.AgentQueueLimit,
	}
	override := func(value int, target *int) {
		if value > 0 {
			*target = value
		}
	}
	switch provider {
	case "gemini":
		override(c.GeminiAgentMaxActive, &limits.MaxActive)
		override(c.GeminiAgentMaxActivePerKey, &limits.MaxActivePerKey)
		override(c.GeminiAgentQueueLimit, &limits.QueueLimit)
	case "claude":
		override(c.ClaudeAgentMaxActive, &limits.MaxActive)
		override(c.ClaudeAgentMaxActivePerKey, &limits.MaxActivePerKey)
		override(c.ClaudeAgentQueueLimit, &limits.QueueLimit)
	}
	return limits
}

func (c Config) Validate() error {
	if c.Host == "" {
		return errors.New("host is required")
	}
	if _, err := parsePort(strconv.Itoa(c.Port)); err != nil {
		return err
	}
	if c.CodexHome == "" {
		return errors.New("codex home is required")
	}
	if c.AuthPath == "" {
		return errors.New("auth path is required")
	}
	if c.CodexProfilePath == "" {
		return errors.New("codex profile path is required")
	}
	if c.CodexScaffoldPath == "" {
		return errors.New("codex scaffold path is required")
	}
	if c.CodexWebsocketURL == "" {
		return errors.New("codex websocket URL is required")
	}
	if c.CodexTimeout <= 0 {
		return errors.New("codex timeout must be positive")
	}
	if c.GeminiAuthPath == "" {
		return errors.New("gemini auth path is required")
	}
	if c.GeminiEndpoint == "" {
		return errors.New("gemini endpoint is required")
	}
	if c.GeminiTimeout <= 0 {
		return errors.New("gemini timeout must be positive")
	}
	if c.ClaudeExecutable == "" {
		return errors.New("claude executable is required")
	}
	if c.ClaudeDefaultModel == "" {
		return errors.New("claude default model is required")
	}
	if c.ClaudeTimeout <= 0 {
		return errors.New("claude timeout must be positive")
	}
	if c.StreamIdleTimeout < 0 {
		return errors.New("stream idle timeout must be non-negative")
	}
	if c.CustomToolWire != "custom" && c.CustomToolWire != "function" {
		return errors.New("custom tool wire must be \"custom\" or \"function\"")
	}
	if c.AgentMaxActive < 1 {
		return errors.New("agent max active must be at least 1")
	}
	if c.AgentMaxActivePerKey < 1 {
		return errors.New("agent max active per key must be at least 1")
	}
	if err := validateAgentQueueKeyMode(c.AgentQueueKeyMode); err != nil {
		return err
	}
	if c.AgentQueueLimit < 0 {
		return errors.New("agent queue limit must be non-negative")
	}
	for _, override := range []int{
		c.GeminiAgentMaxActive, c.GeminiAgentMaxActivePerKey, c.GeminiAgentQueueLimit,
		c.ClaudeAgentMaxActive, c.ClaudeAgentMaxActivePerKey, c.ClaudeAgentQueueLimit,
	} {
		if override < 0 {
			return errors.New("per-provider agent queue overrides must be non-negative")
		}
	}
	if c.AgentQueueEnabled && c.AgentQueueTimeout <= 0 {
		return errors.New("agent queue timeout must be positive")
	}
	if c.ContextMaxBytes < 0 {
		return errors.New("context max bytes must be non-negative")
	}
	if c.ContextMaxMessages < 0 {
		return errors.New("context max messages must be non-negative")
	}
	if c.ContextRecentMessages < 0 {
		return errors.New("context recent messages must be non-negative")
	}
	if c.ContextToolOutputMaxBytes < 0 {
		return errors.New("context tool output max bytes must be non-negative")
	}
	if c.ContextCompactedToolOutputMaxBytes < 0 {
		return errors.New("context compacted tool output max bytes must be non-negative")
	}
	if c.CodexClientMaxInflight < 1 {
		return errors.New("codex client max inflight must be at least 1")
	}
	if err := validateCodexClientPoolUnavailable(c.CodexClientPoolUnavailable); err != nil {
		return err
	}
	if c.CodexClientCooldownDefault <= 0 {
		return errors.New("codex client cooldown default must be positive")
	}
	if err := validateCodexClients(c.CodexClients); err != nil {
		return err
	}
	if err := validateGatewayProviders(c.GatewayProviders); err != nil {
		return err
	}
	if strings.TrimSpace(c.GatewayTenantHeader) == "" {
		return errors.New("gateway tenant header is required")
	}
	if err := c.validateStructured(); err != nil {
		return err
	}
	return nil
}

// validateStructured only rejects impossible admission budgets. The limits are
// checked even when the endpoint is disabled so a bad value is caught at deploy
// time rather than at the moment someone flips the feature on.
func (c Config) validateStructured() error {
	if c.StructuredMaxActive < 1 {
		return errors.New("structured max active must be at least 1")
	}
	if c.StructuredMaxActivePerKey < 1 {
		return errors.New("structured max active per key must be at least 1")
	}
	if c.StructuredQueueLimit < 0 {
		return errors.New("structured queue limit must be non-negative")
	}
	if c.StructuredQueueTimeout <= 0 {
		return errors.New("structured queue timeout must be positive")
	}
	if c.StructuredMaxDeadline <= 0 {
		return errors.New("structured max deadline must be positive")
	}
	if c.StructuredIdempotencyTTL <= 0 {
		return errors.New("structured idempotency ttl must be positive")
	}
	if c.StructuredReplicas != 1 {
		return errors.New("structured replicas must be exactly 1; process-local idempotency requires a single replica with maxSurge=0 or strategy Recreate")
	}
	return nil
}

// ProviderEnabled reports whether a provider is on the gateway allowlist. An
// empty allowlist (hand-built Config) means all providers, matching the
// pre-allowlist behavior.
func (c Config) ProviderEnabled(provider string) bool {
	if len(c.GatewayProviders) == 0 {
		return true
	}
	for _, enabled := range c.GatewayProviders {
		if enabled == provider {
			return true
		}
	}
	return false
}

func parseGatewayProviders(raw string) []string {
	providers := []string{}
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		provider := strings.ToLower(strings.TrimSpace(part))
		if provider == "" || seen[provider] {
			continue
		}
		seen[provider] = true
		providers = append(providers, provider)
	}
	return providers
}

// parseCommaList splits and de-duplicates a comma-separated allowlist,
// preserving order and case (model IDs are case sensitive).
func parseCommaList(raw string) []string {
	values := []string{}
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		value := strings.TrimSpace(part)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		values = append(values, value)
	}
	if len(values) == 0 {
		return nil
	}
	return values
}

func validateGatewayProviders(providers []string) error {
	if len(providers) == 0 {
		return errors.New("at least one gateway provider is required")
	}
	hasCodex := false
	for _, provider := range providers {
		switch provider {
		case "codex":
			hasCodex = true
		case "gemini", "claude":
		default:
			return fmt.Errorf("unsupported gateway provider %q (expected codex, gemini, or claude)", provider)
		}
	}
	if !hasCodex {
		return errors.New("gateway providers must include codex (it is the router fallback)")
	}
	return nil
}

func (c Config) defaultCodexClient() CodexClient {
	return CodexClient{
		Label:             "default",
		CodexHome:         c.CodexHome,
		AuthPath:          c.AuthPath,
		CodexProfilePath:  c.CodexProfilePath,
		CodexScaffoldPath: c.CodexScaffoldPath,
	}
}

func parseCodexClients(raw string, defaults Config) ([]CodexClient, error) {
	var clients []CodexClient
	if err := json.Unmarshal([]byte(raw), &clients); err != nil {
		return nil, fmt.Errorf("CODEX_CLIENTS: invalid JSON: %w", err)
	}
	for i := range clients {
		if clients[i].Label == "" {
			clients[i].Label = fmt.Sprintf("client-%d", i)
		}
		if clients[i].CodexHome == "" {
			clients[i].CodexHome = defaults.CodexHome
		}
		if clients[i].AuthPath == "" && clients[i].CodexHome != "" {
			clients[i].AuthPath = filepath.Join(clients[i].CodexHome, "auth.json")
		}
		if clients[i].CodexProfilePath == "" {
			clients[i].CodexProfilePath = defaults.CodexProfilePath
		}
		if clients[i].CodexScaffoldPath == "" {
			clients[i].CodexScaffoldPath = defaults.CodexScaffoldPath
		}
	}
	return clients, nil
}

func validateCodexClientPoolUnavailable(policy string) error {
	switch policy {
	case "fail", "fallback_first":
		return nil
	default:
		return fmt.Errorf("unsupported codex client pool unavailable policy %q", policy)
	}
}

func validateCodexClients(clients []CodexClient) error {
	if len(clients) == 0 {
		return errors.New("at least one codex client is required")
	}
	seen := map[string]bool{}
	for i, client := range clients {
		if err := validateCodexClientLabel(client.Label); err != nil {
			return fmt.Errorf("codex client %d label: %w", i, err)
		}
		if seen[client.Label] {
			return fmt.Errorf("duplicate codex client label %q", client.Label)
		}
		seen[client.Label] = true
		if client.CodexHome == "" {
			return fmt.Errorf("codex client %q codex home is required", client.Label)
		}
		if client.AuthPath == "" {
			return fmt.Errorf("codex client %q auth path is required", client.Label)
		}
		if client.CodexProfilePath == "" {
			return fmt.Errorf("codex client %q profile path is required", client.Label)
		}
		if client.CodexScaffoldPath == "" {
			return fmt.Errorf("codex client %q scaffold path is required", client.Label)
		}
	}
	return nil
}

var codexClientLabelPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

func validateCodexClientLabel(label string) error {
	if !codexClientLabelPattern.MatchString(label) {
		return errors.New("must be 1-64 characters of letters, digits, underscore, dot, or dash")
	}
	return nil
}

func validateAgentQueueKeyMode(mode string) error {
	switch {
	case mode == "cursor", mode == "global", mode == "auth_hash", mode == "request_fingerprint":
		return nil
	case strings.HasPrefix(mode, "header:"):
		if strings.TrimSpace(strings.TrimPrefix(mode, "header:")) == "" {
			return errors.New("agent queue header key mode requires a header name")
		}
		return nil
	case strings.HasPrefix(mode, "body:"):
		if strings.TrimSpace(strings.TrimPrefix(mode, "body:")) == "" {
			return errors.New("agent queue body key mode requires a field name")
		}
		return nil
	default:
		return fmt.Errorf("unsupported agent queue key mode %q", mode)
	}
}

func loadDotEnv(path string) error {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	if err := godotenv.Load(path); err != nil {
		return fmt.Errorf("load %s: %w", path, err)
	}
	return nil
}

func defaultGeminiHome() string {
	if home := os.Getenv("GEMINI_HOME"); home != "" {
		return home
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".gemini")
	}
	return ".gemini"
}

// Prefer Antigravity oauth (gemini-3.* catalog) when present; else Gemini CLI.
func defaultGeminiAuthPath() string {
	home := defaultGeminiHome()
	antigravity := filepath.Join(home, "antigravity_oauth_creds.json")
	if _, err := os.Stat(antigravity); err == nil {
		return antigravity
	}
	return filepath.Join(home, "oauth_creds.json")
}

func defaultCodexHome() string {
	if value := os.Getenv("CODEX_HOME"); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".codex"
	}
	return filepath.Join(home, ".codex")
}

func parsePort(value string) (int, error) {
	port, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid port %q", value)
	}
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("port must be between 1 and 65535")
	}
	return port, nil
}
