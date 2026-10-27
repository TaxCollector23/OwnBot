package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Run owns the listener, HTTP server lifecycle, and supplied closer. It always
// closes the resource after HTTP shutdown and removes only its own Unix socket.
func Run(ctx context.Context, cfg Config, handler http.Handler, resource io.Closer, logger *slog.Logger) (retErr error) {
	defer func() {
		if resource != nil {
			if err := resource.Close(); err != nil {
				retErr = errors.Join(retErr, err)
			}
		}
	}()
	if err := validate(cfg); err != nil {
		return err
	}
	if logger == nil {
		logger = NewLogger(io.Discard)
	}
	ln, cleanup, err := openListener(cfg)
	if err != nil {
		return err
	}
	defer cleanup()
	transport, address := "tcp", cfg.ListenAddr
	if cfg.SocketPath != "" {
		transport, address = "unix", cfg.SocketPath
	}
	logger.Info("daemon listening", "transport", transport, "address", address)
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 5 * time.Minute, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20, ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelError)}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	select {
	case err = <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		logger.Error("HTTP server stopped", "error", err)
		return err
	case <-ctx.Done():
		logger.Info("shutdown requested")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.ShutdownTimeoutSeconds)*time.Second)
		defer cancel()
		if err = srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown timed out", "error", err)
			_ = srv.Close()
			serveErrErr := <-serveErr
			return errors.Join(err, serveErrErr)
		}
		serveErrErr := <-serveErr
		if !errors.Is(serveErrErr, http.ErrServerClosed) {
			return serveErrErr
		}
		return nil
	}
}

func openListener(cfg Config) (net.Listener, func(), error) {
	if cfg.SocketPath != "" {
		return listenUnix(cfg.SocketPath)
	}
	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return nil, func() {}, err
	}
	return ln, func() {}, nil
}
