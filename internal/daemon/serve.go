package daemon

import (
	"context"
	"crypto/tls"
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
	return RunWithLAN(ctx, cfg, handler, nil, nil, resource, logger)
}

// RunWithLAN owns the local owner API and, when configured, a separate private
// HTTPS listener for paired devices. The local listener remains loopback/Unix.
func RunWithLAN(ctx context.Context, cfg Config, localHandler, lanHandler http.Handler, tlsConfig *tls.Config, resource io.Closer, logger *slog.Logger) (retErr error) {
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
	local := newHTTPServer(localHandler, logger)
	servers := []*http.Server{local}
	listeners := []net.Listener{ln}
	cleanups := []func(){}
	if cfg.LAN {
		if lanHandler == nil || tlsConfig == nil {
			return errors.New("LAN access requires a handler and TLS configuration")
		}
		lanListener, listenErr := net.Listen("tcp", cfg.LANAddr)
		if listenErr != nil {
			return listenErr
		}
		servers = append(servers, newHTTPServer(lanHandler, logger))
		listeners = append(listeners, tls.NewListener(lanListener, tlsConfig))
		cleanups = append(cleanups, func() { _ = lanListener.Close() })
		logger.Info("daemon LAN HTTPS listening", "address", cfg.LANAddr)
	}
	defer func() {
		for _, closeListener := range cleanups {
			closeListener()
		}
	}()
	type serveResult struct {
		index int
		err   error
	}
	serveErr := make(chan serveResult, len(servers))
	for i := range servers {
		go func(i int) { serveErr <- serveResult{i, servers[i].Serve(listeners[i])} }(i)
	}
	select {
	case result := <-serveErr:
		err = result.err
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		logger.Error("HTTP server stopped", "error", err)
		for _, srv := range servers {
			_ = srv.Close()
		}
		for i := 1; i < len(servers); i++ {
			<-serveErr
		}
		return err
	case <-ctx.Done():
		logger.Info("shutdown requested")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.ShutdownTimeoutSeconds)*time.Second)
		defer cancel()
		var shutdownErr error
		for _, srv := range servers {
			shutdownErr = errors.Join(shutdownErr, srv.Shutdown(shutdownCtx))
		}
		if shutdownErr != nil {
			logger.Error("graceful shutdown timed out", "error", shutdownErr)
			for _, srv := range servers {
				_ = srv.Close()
			}
			for range servers {
				<-serveErr
			}
			return shutdownErr
		}
		for range servers {
			result := <-serveErr
			if !errors.Is(result.err, http.ErrServerClosed) {
				err = errors.Join(err, result.err)
			}
		}
		return err
	}
}

func newHTTPServer(handler http.Handler, logger *slog.Logger) *http.Server {
	return &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 5 * time.Minute, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20, ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelError)}
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
