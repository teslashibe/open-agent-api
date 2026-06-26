package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

const (
	DefaultHost              = "127.0.0.1"
	DefaultPort              = 8088
	DefaultCodexWebsocketURL = "wss://chatgpt.com/backend-api/codex/responses"
	DefaultCodexTimeout      = 120 * time.Second
)

type Config struct {
	Host              string
	Port              int
	CodexHome         string
	AuthPath          string
	CodexProfilePath  string
	CodexScaffoldPath string
	CodexWebsocketURL string
	CodexTimeout      time.Duration
	LogBodyShape      bool
}

func Load(args []string) (Config, error) {
	cfg := Defaults()

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
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func Defaults() Config {
	codexHome := defaultCodexHome()
	return Config{
		Host:              DefaultHost,
		Port:              DefaultPort,
		CodexHome:         codexHome,
		AuthPath:          filepath.Join(codexHome, "auth.json"),
		CodexProfilePath:  "codex_profile.json",
		CodexScaffoldPath: "codex_scaffold.json",
		CodexWebsocketURL: DefaultCodexWebsocketURL,
		CodexTimeout:      DefaultCodexTimeout,
	}
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
	return nil
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
