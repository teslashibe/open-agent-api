package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/joho/godotenv"
)

const (
	DefaultHost = "127.0.0.1"
	DefaultPort = 8088
)

type Config struct {
	Host      string
	Port      int
	CodexHome string
	AuthPath  string
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

	fs := flag.NewFlagSet("codex-chat-api", flag.ContinueOnError)
	fs.StringVar(&cfg.Host, "host", cfg.Host, "host address to bind")
	fs.IntVar(&cfg.Port, "port", cfg.Port, "port to bind")
	fs.StringVar(&cfg.CodexHome, "codex-home", cfg.CodexHome, "Codex home directory")
	fs.StringVar(&cfg.AuthPath, "auth-path", cfg.AuthPath, "Codex auth.json path")
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
		Host:      DefaultHost,
		Port:      DefaultPort,
		CodexHome: codexHome,
		AuthPath:  filepath.Join(codexHome, "auth.json"),
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
