package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"ownbot/internal/daemon"
	"ownbot/internal/service"
	"ownbot/internal/update"
)

const releaseAPI = "https://api.github.com/repos/TaxCollector23/OwnBot/releases"

var releaseVersionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

type releaseInfo struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func activeUpdateBinary(dataDir, name string) string {
	if isHomebrewManagedInstall() {
		return ""
	}
	return update.ActiveBinary(dataDir, name)
}

// ReexecActive dispatches future invocations through the versioned active CLI.
// It is called from main before command parsing, so even old npm shims follow
// the atomic active-version pointer without replacing a locked Windows binary.
func ReexecActive(args []string) (int, bool, error) {
	if os.Getenv("OWNBOT_SKIP_ACTIVE_REEXEC") == "1" || isHomebrewManagedInstall() {
		return 0, false, nil
	}
	dataDir, err := updateDataDir(args)
	if err != nil {
		return 1, true, err
	}
	active := activeUpdateBinary(dataDir, "ownbot")
	if active == "" {
		return 0, false, nil
	}
	current, err := os.Executable()
	if err != nil {
		return 1, true, fmt.Errorf("locate current OwnBot CLI: %w", err)
	}
	current, _ = filepath.Abs(current)
	active, _ = filepath.Abs(active)
	if filepath.Clean(current) == filepath.Clean(active) {
		return 0, false, nil
	}
	cmd := exec.Command(active, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), true, nil
		}
		return 1, true, fmt.Errorf("start active OwnBot CLI: %w", err)
	}
	return 0, true, nil
}

func updateDataDir(args []string) (string, error) {
	defaults, err := daemon.Defaults()
	if err != nil {
		return "", err
	}
	dataDir := defaults.DataDir
	if configured := os.Getenv("OWNBOT_DATA_DIR"); configured != "" {
		dataDir = configured
	}
	if len(args) > 0 && args[0] == "daemon" {
		var resolvedArgs []string
		for i := 2; i < len(args); i++ {
			arg := args[i]
			if arg == "--rollback" {
				continue
			}
			if strings.HasPrefix(arg, "--version=") {
				continue
			}
			if arg == "--version" {
				i++
				continue
			}
			if arg == "--url" {
				i++
				continue
			}
			if arg == "--config" || arg == "--data-dir" || arg == "--listen" || arg == "--socket" || arg == "--lan-address" || arg == "--shutdown-timeout" {
				if i+1 < len(args) {
					resolvedArgs = append(resolvedArgs, arg, args[i+1])
					i++
				}
				continue
			}
			if strings.HasPrefix(arg, "--") && !strings.Contains(arg, "=") {
				if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
					i++
				}
				continue
			}
			if strings.HasPrefix(arg, "--config=") || strings.HasPrefix(arg, "--data-dir=") || strings.HasPrefix(arg, "--listen=") || strings.HasPrefix(arg, "--socket=") || strings.HasPrefix(arg, "--lan-address=") || strings.HasPrefix(arg, "--shutdown-timeout=") {
				resolvedArgs = append(resolvedArgs, arg)
			}
		}
		cfg, err := daemon.Load(resolvedArgs, os.Getenv, io.Discard)
		if err != nil {
			return "", err
		}
		dataDir = cfg.DataDir
	} else {
		for i := 0; i < len(args); i++ {
			if args[i] == "--data-dir" && i+1 < len(args) {
				dataDir = args[i+1]
				i++
			} else if strings.HasPrefix(args[i], "--data-dir=") {
				dataDir = strings.TrimPrefix(args[i], "--data-dir=")
			}
		}
	}
	return filepath.Abs(dataDir)
}

func runDaemonUpdate(args []string, stdout, stderr io.Writer) error {
	version, rollback := "", false
	var daemonArgs []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--rollback":
			if rollback {
				return errors.New("--rollback may only be supplied once")
			}
			rollback = true
		case args[i] == "--version":
			if i+1 >= len(args) || version != "" {
				return errors.New("--version requires one release version")
			}
			version = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--version="):
			if version != "" {
				return errors.New("--version may only be supplied once")
			}
			version = strings.TrimPrefix(args[i], "--version=")
		default:
			daemonArgs = append(daemonArgs, args[i])
		}
	}
	if rollback && version != "" {
		return errors.New("choose either --version or --rollback")
	}
	cfg, err := daemon.Load(daemonArgs, os.Getenv, stderr)
	if err != nil {
		return err
	}
	if err := refuseManagedHomebrewInstall(); err != nil {
		return err
	}
	if cfg.SocketPath != "" {
		return errors.New("signed updates need a loopback TCP listener for authenticated daemon stop and version health checks; set --listen and retry")
	}
	apiURL, urlErr := dashboardAPIURL(cfg.ListenAddr, cfg.SocketPath)
	wasRunning := urlErr == nil && daemonHealthy(apiURL)
	serviceStatus, serviceErr := service.Inspect(service.Spec{DataDir: cfg.DataDir})
	if serviceErr != nil {
		return fmt.Errorf("inspect the current user's OwnBot service before updating: %w", serviceErr)
	}
	managed := serviceErr == nil && serviceStatus.Installed
	if rollback {
		active, err := update.ReadActive(cfg.DataDir)
		if err != nil {
			return err
		}
		health := updateHealth(cfg, apiURL, wasRunning, managed, stdout, stderr)
		if err := update.Rollback(cfg.DataDir, health); err != nil {
			return err
		}
		active, err = update.ReadActive(cfg.DataDir)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "OwnBot rolled back to %s. State and configuration were kept.\n", active.Version)
		return nil
	}
	if len(update.TrustedKeys) == 0 {
		return errors.New("signed updates are disabled until a maintainer pins the release public key in internal/update/trusted-keys.json")
	}
	info, err := fetchRelease(version)
	if err != nil {
		return err
	}
	manifestURL, signatureURL := releaseAssetURL(info, "manifest.json"), releaseAssetURL(info, "manifest.sig")
	if manifestURL == "" || signatureURL == "" {
		return errors.New("GitHub release is missing its signed manifest")
	}
	manifestBytes, err := downloadReleaseAsset(manifestURL, 1<<20)
	if err != nil {
		return err
	}
	signatureBytes, err := downloadReleaseAsset(signatureURL, 16<<10)
	if err != nil {
		return err
	}
	manifest, asset, err := update.VerifyRelease(manifestBytes, signatureBytes, update.TrustedKeys, runtime.GOOS, runtime.GOARCH, version)
	if err != nil {
		return err
	}
	assetURL := releaseAssetURL(info, asset.File)
	if assetURL == "" {
		return errors.New("GitHub release is missing the signed platform archive")
	}
	archive, err := downloadReleaseAsset(assetURL, 256<<20)
	if err != nil {
		return err
	}
	if _, err := update.ReadActive(cfg.DataDir); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		current, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate current signed CLI for rollback: %w", err)
		}
		if _, err := update.SeedBaseline(cfg.DataDir, filepath.Dir(current), runtime.GOOS, runtime.GOARCH, update.TrustedKeys); err != nil {
			return err
		}
	}
	health := updateHealth(cfg, apiURL, wasRunning, managed, stdout, stderr)
	if err := update.Install(cfg.DataDir, manifest, asset, archive, health); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "OwnBot %s installed. The active CLI and daemon now use the signed release; state and configuration were kept.\n", manifest.Version)
	return nil
}

func updateHealth(cfg daemon.Config, apiURL string, wasRunning, managed bool, stdout, stderr io.Writer) func(string) error {
	return func(path string) error {
		if strings.HasPrefix(filepath.Base(path), ".stage-") {
			if err := runHealthCommand(filepath.Join(path, binaryName("ownbot")), "--help"); err != nil {
				return err
			}
			return runHealthCommand(filepath.Join(path, binaryName("ownbotd")), "--help")
		}
		if !wasRunning && !managed {
			return runHealthCommand(filepath.Join(path, binaryName("ownbotd")), "--help")
		}
		if managed {
			spec, err := serviceSpec(cfg)
			if err != nil {
				return err
			}
			if err := service.Install(spec); err != nil {
				return err
			}
		} else {
			if daemonHealthy(apiURL) {
				if err := stopDaemon([]string{"--url", apiURL, "--data-dir", cfg.DataDir}, stdout, stderr); err != nil {
					return fmt.Errorf("stop owned daemon for update: %w", err)
				}
			}
			if err := startDaemon(serviceArgs(cfg), apiURL, cfg.DataDir, stdout, false); err != nil {
				return fmt.Errorf("start updated daemon: %w", err)
			}
		}
		if err := waitForDaemonVersion(apiURL, filepath.Base(path), 20*time.Second); err != nil {
			return err
		}
		return nil
	}
}

func waitForDaemonVersion(apiURL, version string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if daemonReportsVersion(apiURL, version) {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("updated daemon did not pass its local OwnBot health check")
}

func daemonReportsVersion(apiURL, expectedVersion string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(apiURL, "/")+"/api/status", nil)
	if err != nil {
		return false
	}
	resp, err := directHTTPClient(1500 * time.Millisecond).Do(req)
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
		Version string `json:"version"`
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body) == nil && body.OK && body.Service == "ownbot" && body.Version == expectedVersion
}

func binaryName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func runHealthCommand(binary string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = withoutEnv(os.Environ(), "OWNBOT_SKIP_ACTIVE_REEXEC")
	cmd.Env = append(cmd.Env, "OWNBOT_SKIP_ACTIVE_REEXEC=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("run staged %s: %s: %w", filepath.Base(binary), strings.TrimSpace(string(output)), err)
	}
	return nil
}

func withoutEnv(environ []string, name string) []string {
	filtered := make([]string, 0, len(environ))
	for _, entry := range environ {
		if !strings.HasPrefix(entry, name+"=") {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func refuseManagedHomebrewInstall() error {
	if isHomebrewManagedInstall() {
		return errors.New("Homebrew manages this installation; run `brew upgrade ownbot` to update it safely")
	}
	return nil
}

func isHomebrewManagedInstall() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	path, err := os.Executable()
	if err != nil {
		return false
	}
	if resolved, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil {
		path = resolved
	}
	return strings.Contains(filepath.ToSlash(path), "/Cellar/")
}

func fetchRelease(version string) (releaseInfo, error) {
	url := releaseAPI + "/latest"
	if version != "" {
		version = strings.TrimPrefix(version, "v")
		if !releaseVersionPattern.MatchString(version) {
			return releaseInfo{}, errors.New("release version must be semantic version text")
		}
		url = releaseAPI + "/tags/v" + version
	}
	body, err := downloadReleaseAsset(url, 2<<20)
	if err != nil {
		return releaseInfo{}, err
	}
	var release releaseInfo
	if err := json.Unmarshal(body, &release); err != nil {
		return releaseInfo{}, fmt.Errorf("parse GitHub release response: %w", err)
	}
	if release.TagName == "" {
		return releaseInfo{}, errors.New("GitHub did not return a tagged OwnBot release")
	}
	if version != "" && strings.TrimPrefix(release.TagName, "v") != version {
		return releaseInfo{}, errors.New("GitHub release tag does not match the requested version")
	}
	return release, nil
}

func releaseAssetURL(release releaseInfo, name string) string {
	for _, asset := range release.Assets {
		if asset.Name == name {
			return asset.URL
		}
	}
	return ""
}

func downloadReleaseAsset(url string, limit int64) ([]byte, error) {
	if !allowedReleaseHost(url) {
		return nil, errors.New("refusing release asset URL outside GitHub HTTPS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "OwnBot signed updater")
	client := &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 4 || !allowedReleaseHost(req.URL.String()) {
			return errors.New("refusing release redirect outside GitHub HTTPS")
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download signed OwnBot release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download signed OwnBot release: GitHub returned %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("GitHub release asset exceeded its size limit")
	}
	return bytes.Clone(data), nil
}

func allowedReleaseHost(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	switch u.Hostname() {
	case "api.github.com", "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		return true
	default:
		return false
	}
}
