package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"ownbot/internal/daemon"
)

func runDaemon(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || (len(args) > 1 && (args[1] == "--help" || args[1] == "-h")) {
		_, _ = fmt.Fprintln(stdout, "Usage: ownyourbot daemon start [daemon flags] | stop [--url URL] [--data-dir DIR]\n\nStarts or stops the local daemon. Start options include --config, --data-dir, --listen, --socket, --lan, --lan-address, and --shutdown-timeout.")
		return nil
	}
	if args[0] == "stop" {
		return stopDaemon(args[1:], stdout, stderr)
	}
	if args[0] != "start" {
		return fmt.Errorf("unknown daemon command %q; use `ownyourbot daemon start` or `stop`", args[0])
	}
	cfg, err := daemon.Load(args[1:], os.Getenv, io.Discard)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	apiURL, err := dashboardAPIURL(cfg.ListenAddr, cfg.SocketPath)
	if err != nil {
		return err
	}
	if cfg.LAN && daemonHealthy(apiURL) {
		client, clientErr := clientFor(apiURL, cfg.DataDir)
		if clientErr != nil {
			return clientErr
		}
		var access struct {
			Enabled     bool     `json:"enabled"`
			URLs        []string `json:"urls"`
			Fingerprint string   `json:"certificate_fingerprint"`
		}
		if clientErr = client.Do(context.Background(), http.MethodGet, "/api/access", nil, &access); clientErr != nil {
			return clientErr
		}
		if !access.Enabled {
			return errors.New("daemon is already running without LAN access; run `ownyourbot daemon stop`, then `ownyourbot daemon start --lan`")
		}
		expectedURL := "https://" + cfg.LANAddr + "/dashboard"
		matchedAddress := false
		for _, address := range access.URLs {
			if address == expectedURL {
				matchedAddress = true
				break
			}
		}
		if !matchedAddress {
			return fmt.Errorf("daemon is already running with a different LAN address; run `ownyourbot daemon stop`, then start with --lan-address %s", cfg.LANAddr)
		}
		_, _ = fmt.Fprintln(stdout, "OwnYourBot daemon is already running with LAN HTTPS access.")
		for _, address := range access.URLs {
			_, _ = fmt.Fprintln(stdout, address)
		}
		if access.Fingerprint != "" {
			_, _ = fmt.Fprintf(stdout, "Certificate fingerprint: %s\n", access.Fingerprint)
		}
		return nil
	}
	if err := startDaemon(args[1:], apiURL, cfg.DataDir, stdout, true); err != nil {
		return err
	}
	if cfg.LAN {
		return printLANAccess(apiURL, cfg.DataDir, stdout)
	}
	return nil
}

func printLANAccess(apiURL, dataDir string, stdout io.Writer) error {
	client, err := clientFor(apiURL, dataDir)
	if err != nil {
		return err
	}
	var access struct {
		Enabled     bool     `json:"enabled"`
		URLs        []string `json:"urls"`
		Fingerprint string   `json:"certificate_fingerprint"`
	}
	if err := client.Do(context.Background(), http.MethodGet, "/api/access", nil, &access); err != nil {
		return err
	}
	if !access.Enabled || len(access.URLs) == 0 {
		return errors.New("daemon started, but LAN access is not enabled; check its startup output")
	}
	_, _ = fmt.Fprintln(stdout, "LAN HTTPS access:")
	for _, address := range access.URLs {
		_, _ = fmt.Fprintln(stdout, address)
	}
	if access.Fingerprint != "" {
		_, _ = fmt.Fprintf(stdout, "Certificate fingerprint: %s\n", access.Fingerprint)
	}
	return nil
}

func stopDaemon(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("daemon stop", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apiURL, dataDir := commonFlags(fs)
	if err := parseFlags(fs, args, stderr); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("daemon stop does not accept positional arguments")
	}
	client, err := clientFor(*apiURL, *dataDir)
	if err != nil {
		return err
	}
	var response struct {
		ShuttingDown bool `json:"shutting_down"`
	}
	if err := client.Do(context.Background(), http.MethodPost, "/api/daemon/shutdown", map[string]any{}, &response); err != nil {
		return err
	}
	if !response.ShuttingDown {
		return errors.New("daemon did not confirm shutdown")
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if !daemonHealthy(*apiURL) {
			_, _ = fmt.Fprintln(stdout, "OwnYourBot daemon stopped.")
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return errors.New("daemon accepted the shutdown request but did not stop within 20 seconds")
}

func dashboardAPIURL(listen, socket string) (string, error) {
	if socket != "" {
		return "", errors.New("the browser dashboard needs a loopback TCP listener; configure --listen")
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("invalid daemon listener: %w", err)
	}
	if strings.EqualFold(host, "localhost") {
		host = "127.0.0.1"
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		return "", errors.New("dashboard listener must use a loopback address")
	}
	return "http://" + net.JoinHostPort(host, port), nil
}

func daemonHealthy(apiURL string) bool {
	u, err := url.Parse(strings.TrimRight(apiURL, "/") + "/api/status")
	if err != nil || u.Host == "" {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	if !strings.EqualFold(u.Hostname(), "localhost") && (ip == nil || !ip.IsLoopback()) {
		return false
	}
	client := directHTTPClient(700 * time.Millisecond)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, u.String(), nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var body struct {
		OK      bool   `json:"ok"`
		Service string `json:"service"`
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body) == nil && body.OK && body.Service == "ownyourbot"
}

func daemonAddressAvailable(apiURL string) (bool, error) {
	u, err := url.Parse(strings.TrimRight(apiURL, "/"))
	if err != nil || u.Host == "" {
		return false, errors.New("invalid local daemon URL")
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		return false, errors.New("local daemon URL must include an explicit port")
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return false, errors.New("daemon listener address must be loopback")
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(host, port))
	if err != nil {
		return false, nil
	}
	_ = listener.Close()
	return true, nil
}

func openDashboard(apiURL string) {
	address := strings.TrimRight(apiURL, "/") + "/dashboard"
	openURL(address)
}

func openURL(address string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", address)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", address)
	default:
		cmd = exec.Command("xdg-open", address)
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Open this address in your browser: %s\n", address)
		return
	}
	_ = cmd.Process.Release()
}

func runInspect(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "models":
			return runModel(args[1:], stdout, stderr)
		case "agents":
			return runAgent(args[1:], stdin, stdout, stderr)
		case "runs":
			return runRuns(args[1:], stdout, stderr)
		case "audit":
			return runAudit(args[1:], stdout, stderr)
		case "search":
			return runSearch(args[1:], stdout, stderr)
		case "logs":
			return runLogs(args[1:], stdout, stderr)
		case "hardware":
			return runHardwareChecks(args[1:], stdout, stderr)
		default:
			return fmt.Errorf("unknown inspect section %q; available: hardware, models, agents, runs, audit, search, logs", args[0])
		}
	}
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	apiURL, dataDir := commonFlags(fs)
	if err := parseFlags(fs, args, stderr); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("inspect does not accept positional arguments")
	}
	client, err := clientFor(*apiURL, *dataDir)
	if err != nil {
		return err
	}
	state := map[string]any{}
	for key, path := range map[string]string{
		"daemon": "/api/status", "models": "/api/models", "agents": "/api/agents",
		"tool_permissions": "/api/tools", "mcp_integrations": "/api/mcp/servers",
	} {
		var value any
		if err := client.Do(backgroundContext(), http.MethodGet, path, nil, &value); err != nil {
			return fmt.Errorf("inspect %s: %w", key, err)
		}
		state[key] = value
	}
	return writeJSON(stdout, state)
}

func runHarness(stdin io.Reader, stdout, stderr io.Writer) error {
	printWelcome(stdout)
	printHelp(stdout)
	_, _ = fmt.Fprintln(stdout, "Task harness: enter a task, `run <agent-id> <task>`, `help`, or `quit`.")
	if !interactiveOutput(stdout) {
		return nil
	}
	reader := bufio.NewReader(stdin)
	for {
		_, _ = io.WriteString(stdout, "ownyourbot> ")
		line, err := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "quit" || line == "exit" {
			return nil
		}
		if line == "help" {
			printHelp(stdout)
			if err != nil {
				return nil
			}
			continue
		}
		if line == "" {
			if err != nil {
				return nil
			}
			continue
		}
		if strings.HasPrefix(line, "run ") {
			parts := strings.SplitN(strings.TrimSpace(strings.TrimPrefix(line, "run ")), " ", 2)
			if len(parts) != 2 {
				_, _ = fmt.Fprintln(stderr, "Use: run <agent-id> <task>")
				if err != nil {
					return nil
				}
				continue
			}
			if runErr := runTask(parts[0], parts[1], stdout); runErr != nil {
				_, _ = fmt.Fprintln(stderr, runErr)
			}
		} else if err = runDefaultTask(line, reader, stdout); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
		}
		if err != nil {
			return nil
		}
	}
}

func interactiveOutput(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func runDefaultTask(task string, reader *bufio.Reader, stdout io.Writer) error {
	client, err := clientFor(defaultAPIURL(), "")
	if err != nil {
		return err
	}
	var agents []agentConfig
	if err = client.Do(backgroundContext(), http.MethodGet, "/api/agents", nil, &agents); err != nil {
		return err
	}
	var enabled []agentConfig
	for _, a := range agents {
		if a.Enabled {
			enabled = append(enabled, a)
		}
	}
	if len(enabled) == 0 {
		return errors.New("no enabled agents are available; create and enable one with `ownyourbot setup` and the dashboard")
	}
	selected := ""
	if len(enabled) == 1 {
		selected = enabled[0].ID
	} else {
		_, _ = fmt.Fprintln(stdout, "Enabled agents:")
		for _, a := range enabled {
			_, _ = fmt.Fprintf(stdout, "  %s  %s\n", a.ID, a.Name)
		}
		selected, err = ask(reader, stdout, "Agent ID", "")
		if err != nil {
			return errors.New("agent ID required")
		}
	}
	return runTask(selected, task, stdout)
}

func runTask(agentID, task string, stdout io.Writer) error {
	client, err := clientFor(defaultAPIURL(), "")
	if err != nil {
		return err
	}
	var result runResult
	if err := client.Do(backgroundContext(), http.MethodPost, "/api/agents/"+urlPath(agentID)+"/runs", map[string]string{"input": task}, &result); err != nil {
		return err
	}
	if result.Status == "awaiting_approval" {
		_, _ = fmt.Fprintln(stdout, "This task needs approval. Review it in the local dashboard before it continues.")
	}
	return writeJSON(stdout, result)
}

func setupDaemonArgs(apiURL, dataDir string) ([]string, error) {
	args := []string{"--data-dir", filepath.Clean(dataDir)}
	u, err := url.Parse(apiURL)
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return nil, errors.New("automatic daemon startup requires a local HTTP API URL")
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		return nil, errors.New("automatic daemon startup requires a host and port")
	}
	args = append(args, "--listen", net.JoinHostPort(host, port))
	return args, nil
}
