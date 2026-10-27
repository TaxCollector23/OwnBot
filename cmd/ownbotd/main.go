package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"ownbot/internal/daemon"
	"ownbot/internal/server"
)

func main() {
	logger := daemon.NewLogger(os.Stderr)
	if err := run(os.Args[1:], logger); err != nil {
		logger.Error("daemon startup or shutdown failed", "error", err)
		os.Exit(1)
	}
}

func run(args []string, logger *slog.Logger) error {
	cfg, err := daemon.Load(args, os.Getenv, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	app, err := server.New(cfg.DataDir)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return daemon.Run(ctx, cfg, app.Handler(), app, logger)
}
