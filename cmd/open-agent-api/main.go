package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/teslashibe/open-agent-api/internal/claude"
	"github.com/teslashibe/open-agent-api/internal/codex"
	"github.com/teslashibe/open-agent-api/internal/config"
	"github.com/teslashibe/open-agent-api/internal/gemini"
	metricspkg "github.com/teslashibe/open-agent-api/internal/metrics"
	"github.com/teslashibe/open-agent-api/internal/server"
	"github.com/teslashibe/open-agent-api/internal/telemetry"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "open-agent-api: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := config.Load(args)
	if err != nil {
		return err
	}
	logOutput, err := telemetry.New(os.Stdout, cfg.TelemetryFile, cfg.TelemetryMaxBytes, cfg.TelemetryBackups)
	if err != nil {
		return fmt.Errorf("initialize telemetry: %w", err)
	}
	defer logOutput.Close()
	metrics := metricspkg.New(cfg.MetricsEnabled)
	codexService, err := buildCodexService(cfg, metrics, logOutput)
	if err != nil {
		return err
	}
	// Disabled providers get no client at all: the server rejects their models
	// before routing, and a nil service here guarantees no upstream (including
	// the claude CLI) can be reached even if a request slipped through.
	var geminiService codex.Service
	if cfg.ProviderEnabled(codex.ProviderGemini) {
		geminiService, err = buildGeminiService(cfg)
		if err != nil {
			return err
		}
	}
	var claudeService codex.Service
	if cfg.ProviderEnabled(codex.ProviderClaude) {
		claudeService, err = buildClaudeService(cfg)
		if err != nil {
			return err
		}
	}
	service := codex.Router{Codex: codexService, Gemini: geminiService, Claude: claudeService}

	app := server.New(cfg, server.WithCodexService(service), server.WithMetrics(metrics), server.WithLogOutput(logOutput))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		errCh <- app.Listen(cfg.Addr())
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		stop()
		if err := app.Shutdown(); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		if err := <-errCh; err != nil {
			return err
		}
		return nil
	}
}

func buildCodexService(cfg config.Config, metrics *metricspkg.Metrics, logOutput io.Writer) (codex.Service, error) {
	clients := make([]codex.PooledClientConfig, 0, len(cfg.CodexClients))
	for _, clientCfg := range cfg.CodexClients {
		client, err := codex.NewClient(codex.ClientConfig{
			AuthPath:      clientCfg.AuthPath,
			CodexHome:     clientCfg.CodexHome,
			ProfilePath:   clientCfg.CodexProfilePath,
			ScaffoldPath:  clientCfg.CodexScaffoldPath,
			WebsocketURL:  cfg.CodexWebsocketURL,
			Timeout:       cfg.CodexTimeout,
			LogOutput:     logOutput,
			LogBodyShape:  cfg.LogBodyShape,
			LogToolEvents: cfg.LogCodexToolEvents,
			ClientLabel:   clientCfg.Label,
			Metrics:       metrics,
		})
		if err != nil {
			return nil, fmt.Errorf("create codex client %q: %w", clientCfg.Label, err)
		}
		clients = append(clients, codex.PooledClientConfig{
			Label:   clientCfg.Label,
			Service: client,
		})
	}
	return codex.NewPooledService(codex.PooledServiceConfig{
		Clients:           clients,
		MaxInflight:       cfg.CodexClientMaxInflight,
		UnavailablePolicy: cfg.CodexClientPoolUnavailable,
		LogOutput:         logOutput,
		CooldownDefault:   cfg.CodexClientCooldownDefault,
		CooldownMax:       cfg.CodexClientCooldownMax,
		Metrics:           metrics,
	})
}

func buildGeminiService(cfg config.Config) (codex.Service, error) {
	client, err := gemini.NewClient(gemini.Config{
		AuthPath:      cfg.GeminiAuthPath,
		Endpoint:      cfg.GeminiEndpoint,
		Project:       cfg.GeminiProject,
		Timeout:       cfg.GeminiTimeout,
		HeaderTimeout: cfg.StreamIdleTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("create gemini client: %w", err)
	}
	return client, nil
}

func buildClaudeService(cfg config.Config) (codex.Service, error) {
	client, err := claude.NewClient(claude.Config{
		Executable:   cfg.ClaudeExecutable,
		DefaultModel: cfg.ClaudeDefaultModel,
		Timeout:      cfg.ClaudeTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("create claude client: %w", err)
	}
	return client, nil
}
