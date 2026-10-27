package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
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

func run(args []string, logger *slog.Logger) (retErr error) {
	cfg, err := daemon.Load(args, os.Getenv, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	stateLock, err := daemon.AcquireStateLock(cfg.DataDir)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, stateLock.Close()) }()
	app, err := server.New(cfg.DataDir)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app.SetShutdown(stop)
	if !cfg.LAN {
		return daemon.Run(ctx, cfg, app.Handler(), app, logger)
	}
	tlsConfig, fingerprint, err := daemon.LANCertificate(cfg.DataDir, cfg.LANAddr)
	if err != nil {
		return err
	}
	_, port, err := net.SplitHostPort(cfg.LANAddr)
	if err != nil {
		return err
	}
	host, _, _ := net.SplitHostPort(cfg.LANAddr)
	authority := net.JoinHostPort(host, port)
	if err := app.ConfigureLAN(authority, fingerprint); err != nil {
		return err
	}
	return daemon.RunWithLAN(ctx, cfg, app.Handler(), app.LANHandler(), tlsConfig, app, logger)
}
