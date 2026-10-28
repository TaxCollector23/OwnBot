package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(k string) string { return values[k] }
}
func TestConfigPrecedenceAndLegacyEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"data_dir":"/from-file","listen_addr":"127.0.0.1:8000","shutdown_timeout_seconds":20}`), 0600); err != nil {
		t.Fatal(err)
	}
	stderr := new(bytes.Buffer)
	cfg, err := Load([]string{"--config", path, "--listen", "127.0.0.1:9000"}, env(map[string]string{"OWNBOT_DATA_DIR": "/from-env", "OWNBOT_ADDR": "127.0.0.1:8500"}), stderr)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != "/from-env" || cfg.ListenAddr != "127.0.0.1:9000" || cfg.ShutdownTimeoutSeconds != 20 {
		t.Fatalf("precedence mismatch: %+v", cfg)
	}
}

func TestDataDirPrecedenceFileEnvironmentThenExplicitFlag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"data_dir":"/from-file","listen_addr":"127.0.0.1:8000"}`), 0600); err != nil {
		t.Fatal(err)
	}
	getenv := env(map[string]string{"OWNBOT_DATA_DIR": "/from-env"})
	fromEnv, err := Load([]string{"--config", path}, getenv, new(bytes.Buffer))
	if err != nil {
		t.Fatal(err)
	}
	if fromEnv.DataDir != "/from-env" {
		t.Fatalf("environment should override file: got %q", fromEnv.DataDir)
	}
	fromFlag, err := Load([]string{"--config", path, "--data-dir", "/from-flag"}, getenv, new(bytes.Buffer))
	if err != nil {
		t.Fatal(err)
	}
	if fromFlag.DataDir != "/from-flag" {
		t.Fatalf("explicit flag should override environment and file: got %q", fromFlag.DataDir)
	}
}
func TestConfigAcceptsSocketFromFileAndRejectsPublicListener(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	dataDir := filepath.Join(t.TempDir(), "state")
	socketPath := filepath.Join(t.TempDir(), "ownbot.sock")
	config, err := json.Marshal(map[string]string{"data_dir": dataDir, "socket_path": socketPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, config, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load([]string{"--config", path}, env(nil), new(bytes.Buffer))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SocketPath != socketPath || cfg.ListenAddr != "" {
		t.Fatalf("unexpected socket config: %+v", cfg)
	}
	if _, err = Load([]string{"--listen", "127.0.0.1:0"}, env(nil), new(bytes.Buffer)); err == nil {
		t.Fatal("ephemeral port configuration accepted")
	}
	if _, err = Load([]string{"--listen", "0.0.0.0:8765"}, env(nil), new(bytes.Buffer)); err == nil {
		t.Fatal("public listener accepted")
	}
	if _, err = Load([]string{"--listen", "127.0.0.1:1", "--socket", socketPath}, env(nil), new(bytes.Buffer)); err == nil {
		t.Fatal("conflicting listeners accepted")
	}
}
func TestConfigHelpAndUnknownFields(t *testing.T) {
	out := new(bytes.Buffer)
	_, err := Load([]string{"--help"}, env(nil), out)
	if !errors.Is(err, flag.ErrHelp) || !strings.Contains(out.String(), "socket path") {
		t.Fatalf("help output=%q err=%v", out.String(), err)
	}
	p := filepath.Join(t.TempDir(), "bad.json")
	_ = os.WriteFile(p, []byte(`{"data_dir":"x","secret":"not allowed"}`), 0600)
	if _, err = Load([]string{"--config", p}, env(nil), new(bytes.Buffer)); err == nil {
		t.Fatal("unknown config field accepted")
	}
	_ = os.WriteFile(p, []byte(`null`), 0600)
	if _, err = Load([]string{"--config", p}, env(nil), new(bytes.Buffer)); err == nil {
		t.Fatal("non-object config accepted")
	}
}
func TestLoggerRedactsCredentialFields(t *testing.T) {
	out := new(bytes.Buffer)
	NewLogger(out).Info("request", "api_key", "fixture-secret", slog.Group("provider", slog.String("authorization", "bearer fixture-secret")))
	if strings.Contains(out.String(), "fixture-secret") {
		t.Fatalf("structured log leaked credential: %s", out.String())
	}
}
