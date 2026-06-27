package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/teslashibe/codex-chat-api/internal/codex"
	"github.com/teslashibe/codex-chat-api/internal/config"
	"github.com/teslashibe/codex-chat-api/internal/server"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "codex-chat-api: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := config.Load(args)
	if err != nil {
		return err
	}

	codexService, err := buildCodexService(cfg)
	if err != nil {
		return err
	}

	app := server.New(cfg, server.WithCodexService(codexService))
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

func buildCodexService(cfg config.Config) (codex.Service, error) {
	clients := make([]codex.PooledClientConfig, 0, len(cfg.CodexClients))
	for _, clientCfg := range cfg.CodexClients {
		client, err := codex.NewClient(codex.ClientConfig{
			AuthPath:     clientCfg.AuthPath,
			CodexHome:    clientCfg.CodexHome,
			ProfilePath:  clientCfg.CodexProfilePath,
			ScaffoldPath: clientCfg.CodexScaffoldPath,
			WebsocketURL: cfg.CodexWebsocketURL,
			Timeout:      cfg.CodexTimeout,
			LogOutput:    os.Stdout,
			LogBodyShape: cfg.LogBodyShape,
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
		UnavailablePolicy: cfg.CodexClientPoolUnavailable,
		LogOutput:         os.Stdout,
	})
}
