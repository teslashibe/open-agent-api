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

	codexClient, err := codex.NewClient(codex.ClientConfig{
		AuthPath:     cfg.AuthPath,
		CodexHome:    cfg.CodexHome,
		ProfilePath:  cfg.CodexProfilePath,
		ScaffoldPath: cfg.CodexScaffoldPath,
		WebsocketURL: cfg.CodexWebsocketURL,
		Timeout:      cfg.CodexTimeout,
		LogOutput:    os.Stdout,
		LogBodyShape: cfg.LogBodyShape,
	})
	if err != nil {
		return err
	}

	app := server.New(cfg, server.WithCodexService(codexClient))
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
