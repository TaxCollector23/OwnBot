package cli

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"ownbot/internal/daemon"
	"ownbot/internal/service"
)

func serviceArgs(cfg daemon.Config) []string {
	args := []string{"--data-dir", filepath.Clean(cfg.DataDir)}
	if cfg.SocketPath != "" {
		args = append(args, "--socket", cfg.SocketPath)
	} else {
		args = append(args, "--listen", cfg.ListenAddr)
	}
	if cfg.LAN {
		args = append(args, "--lan", "--lan-address", cfg.LANAddr)
	} else {
		// Prevent daemon-manager environment variables from silently enabling
		// private-network access after installation.
		args = append(args, "--lan=false")
	}
	args = append(args, "--shutdown-timeout", strconv.Itoa(cfg.ShutdownTimeoutSeconds)+"s")
	return args
}

func serviceSpec(cfg daemon.Config) (service.Spec, error) {
	executable, err := os.Executable()
	if err != nil {
		return service.Spec{}, fmt.Errorf("locate OwnBot CLI: %w", err)
	}
	name := "ownbotd"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(filepath.Dir(executable), name)
	return service.Spec{Executable: path, Args: serviceArgs(cfg), DataDir: cfg.DataDir}, nil
}

func serviceAPIURL(cfg daemon.Config) (string, error) {
	return dashboardAPIURL(cfg.ListenAddr, cfg.SocketPath)
}

func installUserService(cfg daemon.Config, stdout io.Writer) error {
	if !service.Supported(runtime.GOOS) {
		return fmt.Errorf("user service installation is not supported on %s; run `ownbot daemon start` for this session", runtime.GOOS)
	}
	spec, err := serviceSpec(cfg)
	if err != nil {
		return err
	}
	if _, err := os.Stat(spec.Executable); err != nil {
		return errors.New("daemon binary is missing; reinstall OwnBot or rebuild both binaries")
	}
	if err := service.Preflight(spec); err != nil {
		return err
	}
	apiURL, err := serviceAPIURL(cfg)
	if err != nil {
		return fmt.Errorf("user service control requires a loopback TCP listener: %w", err)
	}
	wasRunning := daemonHealthy(apiURL)
	if wasRunning {
		preserveRunningLAN(&cfg, apiURL)
		if err := stopDaemon([]string{"--url", apiURL, "--data-dir", cfg.DataDir}, stdout, io.Discard); err != nil {
			return err
		}
	} else {
		available, err := daemonAddressAvailable(apiURL)
		if err != nil {
			return err
		}
		if !available {
			return errors.New("listener address is occupied by another service; choose another port before installing the user service")
		}
	}
	// The existing daemon may carry a LAN opt-in discovered above. Serialize
	// the final resolved configuration rather than the earlier snapshot.
	spec, err = serviceSpec(cfg)
	if err != nil {
		return restoreSessionDaemon(wasRunning, cfg, apiURL, stdout, err)
	}
	if err := service.Install(spec); err != nil {
		return restoreSessionDaemon(wasRunning, cfg, apiURL, stdout, err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if daemonHealthy(apiURL) {
			_, _ = fmt.Fprintf(stdout, "OwnBot user service installed and running at %s. It starts at login.\n", apiURL)
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	installErr := errors.New("user service did not become ready within 20 seconds; check the service manager and daemon log")
	if err := service.Uninstall(spec); err != nil {
		installErr = errors.Join(installErr, fmt.Errorf("remove failed user service: %w", err))
	}
	return restoreSessionDaemon(wasRunning, cfg, apiURL, stdout, installErr)
}

func restoreSessionDaemon(wasRunning bool, cfg daemon.Config, apiURL string, stdout io.Writer, cause error) error {
	return restoreSessionDaemonWith(wasRunning, cfg, apiURL, stdout, cause, startDaemon)
}

func restoreSessionDaemonWith(wasRunning bool, cfg daemon.Config, apiURL string, stdout io.Writer, cause error, start func([]string, string, string, io.Writer, bool) error) error {
	if !wasRunning {
		return cause
	}
	if err := start(serviceArgs(cfg), apiURL, cfg.DataDir, stdout, false); err != nil {
		return errors.Join(cause, fmt.Errorf("could not restore the previously running daemon: %w", err))
	}
	return cause
}

// Preserve an explicit paired-device opt-in when moving a currently running
// daemon under the native service manager.
func preserveRunningLAN(cfg *daemon.Config, apiURL string) {
	if cfg.LAN {
		return
	}
	client, err := clientFor(apiURL, cfg.DataDir)
	if err != nil {
		return
	}
	var access struct {
		Enabled bool     `json:"enabled"`
		URLs    []string `json:"urls"`
	}
	if client.Do(backgroundContext(), "GET", "/api/access", nil, &access) != nil || !access.Enabled {
		return
	}
	for _, address := range access.URLs {
		u, err := url.Parse(address)
		if err == nil && u.Scheme == "https" {
			if _, _, err := net.SplitHostPort(u.Host); err == nil {
				cfg.LAN, cfg.LANAddr = true, u.Host
				return
			}
		}
	}
}

func runServiceCommand(action string, args []string, stdout, stderr io.Writer) error {
	if action != "install" && action != "uninstall" && action != "status" {
		return fmt.Errorf("unknown daemon command %q; use `ownbot daemon start|stop|install|uninstall|status`", action)
	}
	cfg, err := daemon.Load(args, os.Getenv, stderr)
	if err != nil {
		return err
	}
	if action == "install" {
		return installUserService(cfg, stdout)
	}
	spec, err := serviceSpec(cfg)
	if err != nil {
		return err
	}
	status, err := service.Inspect(spec)
	if err != nil {
		return err
	}
	if action == "uninstall" {
		apiURL, urlErr := serviceAPIURL(cfg)
		if urlErr == nil && status.Installed && status.Active && daemonHealthy(apiURL) {
			if err := stopDaemon([]string{"--url", apiURL, "--data-dir", cfg.DataDir}, stdout, stderr); err != nil {
				return err
			}
		}
		if err := service.Uninstall(spec); err != nil {
			return err
		}
		if status.Installed {
			_, _ = fmt.Fprintln(stdout, "OwnBot user service removed. State and token files were kept.")
		} else {
			_, _ = fmt.Fprintln(stdout, "OwnBot user service was not installed; state and token files were kept.")
		}
		return nil
	}
	_, _ = fmt.Fprintf(stdout, "User service: %s", serviceState(status))
	if status.Path != "" {
		_, _ = fmt.Fprintf(stdout, " (%s)", status.Path)
	}
	_, _ = fmt.Fprintln(stdout)
	if apiURL, urlErr := serviceAPIURL(cfg); urlErr == nil && daemonHealthy(apiURL) {
		_, _ = fmt.Fprintf(stdout, "Daemon: running at %s\n", apiURL)
	} else {
		_, _ = fmt.Fprintln(stdout, "Daemon: not running at the configured listener")
	}
	if !status.Supported {
		return fmt.Errorf("%s; run `ownbot daemon start` for this session", status.Message)
	}
	return nil
}

func serviceState(status service.Status) string {
	switch {
	case !status.Supported:
		return "unsupported on this operating system"
	case !status.Installed:
		return "not installed"
	case status.Active:
		return "active"
	default:
		return "installed, inactive"
	}
}

func setupManagedDaemon(args []string, apiURL, dataDir string, stdout io.Writer) (string, error) {
	if !service.Supported(runtime.GOOS) {
		if err := startDaemon(args, apiURL, dataDir, stdout, false); err != nil {
			return "", err
		}
		return fmt.Sprintf("User login service is unsupported on %s; daemon started for this session only.\n", runtime.GOOS), nil
	}
	cfg, err := daemon.Load(args, os.Getenv, io.Discard)
	if err != nil {
		return "", err
	}
	if err := installUserService(cfg, stdout); err != nil {
		return setupSessionFallback(runtime.GOOS, args, apiURL, dataDir, stdout, err, startDaemon)
	}
	return "Daemon is managed by the user login service.\n", nil
}

func setupSessionFallback(goos string, args []string, apiURL, dataDir string, stdout io.Writer, installErr error, start func([]string, string, string, io.Writer, bool) error) (string, error) {
	if goos != "linux" || !errors.Is(installErr, service.ErrUserManagerUnavailable) {
		return "", installErr
	}
	if err := start(args, apiURL, dataDir, stdout, false); err != nil {
		return "", errors.Join(installErr, fmt.Errorf("could not start the session daemon: %w", err))
	}
	return "No systemd user manager is available; the daemon was started for this session only and will not restart at login.\n", nil
}
