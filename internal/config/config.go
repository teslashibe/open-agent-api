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
	DefaultCodexTimeout                       = 120 * time.Second
	DefaultAgentQueueEnabled                  = true
	DefaultAgentMaxActive                     = 2
	DefaultAgentMaxActivePerKey               = 1
	DefaultAgentQueueKeyMode                  = "cursor"
	DefaultAgentQueueLimit                    = 20
	DefaultAgentQueueTimeout                  = 5 * time.Minute
	DefaultContextMaxBytes                    = 256 * 1024
	DefaultContextMaxMessages                 = 150
	DefaultContextRecentMessages              = 40
	DefaultContextToolOutputMaxBytes          = 64 * 1024
	DefaultContextCompactedToolOutputMaxBytes = 1024
	DefaultDegenerateTurnRetryEnabled         = true
	DefaultCodexClientPoolUnavailable         = "fail"
)

type Config struct {
	Host                               string
	Port                               int
	CodexHome                          string
	AuthPath                           string
	CodexProfilePath                   string
	CodexScaffoldPath                  string
	CodexWebsocketURL                  string
	CodexTimeout                       time.Duration
	LogBodyShape                       bool
	LogRequestIdentity                 bool
	AgentQueueEnabled                  bool
	AgentMaxActive                     int
	AgentMaxActivePerKey               int
	AgentQueueKeyMode                  string
	AgentQueueLimit                    int
	AgentQueueTimeout                  time.Duration
	AgentQueueLockDir                  string
	ContextManagementEnabled           bool
	ContextMaxBytes                    int
	ContextMaxMessages                 int
	ContextRecentMessages              int
	ContextToolOutputMaxBytes          int
	ContextCompactedToolOutputMaxBytes int
	DegenerateTurnRetryEnabled         bool
	CodexClients                       []CodexClient
	CodexClientPoolUnavailable         string
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
	if value := os.Getenv("CODEX_CLIENT_POOL_UNAVAILABLE"); value != "" {
		cfg.CodexClientPoolUnavailable = value
	}

	fs := flag.NewFlagSet("codex-chat-api", flag.ContinueOnError)
	fs.StringVar(&cfg.Host, "host", cfg.Host, "host address to bind")
	fs.IntVar(&cfg.Port, "port", cfg.Port, "port to bind")
	fs.StringVar(&cfg.CodexHome, "codex-home", cfg.CodexHome, "Codex home directory")
	fs.StringVar(&cfg.AuthPath, "auth-path", cfg.AuthPath, "Codex auth.json path")
	fs.StringVar(&cfg.CodexProfilePath, "codex-profile", cfg.CodexProfilePath, "Codex profile JSON path")
	fs.StringVar(&cfg.CodexScaffoldPath, "codex-scaffold", cfg.CodexScaffoldPath, "Codex scaffold JSON path")
	fs.StringVar(&cfg.CodexWebsocketURL, "codex-websocket-url", cfg.CodexWebsocketURL, "Codex websocket URL")
	fs.DurationVar(&cfg.CodexTimeout, "codex-timeout", cfg.CodexTimeout, "Codex websocket request timeout")
	fs.BoolVar(&cfg.LogBodyShape, "log-body-shape", cfg.LogBodyShape, "log redacted JSON request body shape")
	fs.BoolVar(&cfg.LogRequestIdentity, "log-request-identity", cfg.LogRequestIdentity, "log redacted request identity diagnostics")
	fs.BoolVar(&cfg.AgentQueueEnabled, "agent-queue-enabled", cfg.AgentQueueEnabled, "enable Agent queue for requests with tools")
	fs.IntVar(&cfg.AgentMaxActive, "agent-max-active", cfg.AgentMaxActive, "maximum concurrent tool-capable Agent requests")
	fs.IntVar(&cfg.AgentMaxActivePerKey, "agent-max-active-per-key", cfg.AgentMaxActivePerKey, "maximum concurrent tool-capable Agent requests per queue key")
	fs.StringVar(&cfg.AgentQueueKeyMode, "agent-queue-key-mode", cfg.AgentQueueKeyMode, "Agent queue key mode: cursor, global, auth_hash, request_fingerprint, header:<name>, or body:<field>")
	fs.IntVar(&cfg.AgentQueueLimit, "agent-queue-limit", cfg.AgentQueueLimit, "maximum waiting tool-capable Agent requests")
	fs.DurationVar(&cfg.AgentQueueTimeout, "agent-queue-timeout", cfg.AgentQueueTimeout, "maximum time a tool-capable Agent request can wait in the queue")
	fs.StringVar(&cfg.AgentQueueLockDir, "agent-queue-lock-dir", cfg.AgentQueueLockDir, "directory for cross-process Agent queue key locks")
	fs.BoolVar(&cfg.ContextManagementEnabled, "context-management-enabled", cfg.ContextManagementEnabled, "enable context management for tool-capable minimal-mode requests")
	fs.IntVar(&cfg.ContextMaxBytes, "context-max-bytes", cfg.ContextMaxBytes, "approximate message context byte threshold before compaction")
	fs.IntVar(&cfg.ContextMaxMessages, "context-max-messages", cfg.ContextMaxMessages, "message count threshold before compaction")
	fs.IntVar(&cfg.ContextRecentMessages, "context-recent-messages", cfg.ContextRecentMessages, "recent messages to leave unchanged during compaction")
	fs.IntVar(&cfg.ContextToolOutputMaxBytes, "context-tool-output-max-bytes", cfg.ContextToolOutputMaxBytes, "maximum bytes to keep from an individual tool output before adding a truncation marker")
	fs.IntVar(&cfg.ContextCompactedToolOutputMaxBytes, "context-compacted-tool-output-max-bytes", cfg.ContextCompactedToolOutputMaxBytes, "maximum bytes to keep from an older compacted tool output")
	fs.BoolVar(&cfg.DegenerateTurnRetryEnabled, "degenerate-turn-retry", cfg.DegenerateTurnRetryEnabled, "retry tool-capable turns that finish with text-only stop using tool_choice required")
	fs.StringVar(&clientsJSON, "codex-clients", clientsJSON, "JSON array of Codex clients with non-sensitive labels and optional codex_home, auth_path, profile_path, and scaffold_path")
	fs.StringVar(&cfg.CodexClientPoolUnavailable, "codex-client-pool-unavailable", cfg.CodexClientPoolUnavailable, "Codex client pool unavailable policy: fail or fallback_first")
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
		AgentQueueEnabled:                  DefaultAgentQueueEnabled,
		AgentMaxActive:                     DefaultAgentMaxActive,
		AgentMaxActivePerKey:               DefaultAgentMaxActivePerKey,
		AgentQueueKeyMode:                  DefaultAgentQueueKeyMode,
		AgentQueueLimit:                    DefaultAgentQueueLimit,
		AgentQueueTimeout:                  DefaultAgentQueueTimeout,
		AgentQueueLockDir:                  "",
		ContextMaxBytes:                    DefaultContextMaxBytes,
		ContextMaxMessages:                 DefaultContextMaxMessages,
		ContextRecentMessages:              DefaultContextRecentMessages,
		ContextToolOutputMaxBytes:          DefaultContextToolOutputMaxBytes,
		ContextCompactedToolOutputMaxBytes: DefaultContextCompactedToolOutputMaxBytes,
		DegenerateTurnRetryEnabled:         DefaultDegenerateTurnRetryEnabled,
		CodexClientPoolUnavailable:         DefaultCodexClientPoolUnavailable,
	}
	cfg.CodexClients = []CodexClient{cfg.defaultCodexClient()}
	return cfg
}

func (c Config) Addr() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
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
	if err := validateCodexClientPoolUnavailable(c.CodexClientPoolUnavailable); err != nil {
		return err
	}
	if err := validateCodexClients(c.CodexClients); err != nil {
		return err
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
