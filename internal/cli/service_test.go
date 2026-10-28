package cli

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ownbot/internal/daemon"
)

func TestServiceArgsPreserveListenerLANAndDataDirectory(t *testing.T) {
	cfg := daemon.Config{DataDir: "/tmp/Own Your Bot", ListenAddr: "127.0.0.1:18765", LAN: true, LANAddr: "192.168.1.24:8766", ShutdownTimeoutSeconds: 17}
	want := []string{"--data-dir", "/tmp/Own Your Bot", "--listen", "127.0.0.1:18765", "--lan", "--lan-address", "192.168.1.24:8766", "--shutdown-timeout", "17s"}
	if got := serviceArgs(cfg); !reflect.DeepEqual(got, want) {
		t.Fatalf("service args = %#v, want %#v", got, want)
	}
}

func TestPreservedRunningLANIsSerializedIntoServiceSpec(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.Chmod(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "api.token"), []byte(strings.Repeat("a", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/status" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "service": "ownbot"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"enabled": true, "urls": []string{"https://192.168.1.24:8766/dashboard"}})
	}))
	defer server.Close()

	cfg := daemon.Config{DataDir: dataDir, ListenAddr: "127.0.0.1:18765", ShutdownTimeoutSeconds: 10}
	preserveRunningLAN(&cfg, server.URL)
	if !cfg.LAN || cfg.LANAddr != "192.168.1.24:8766" {
		t.Fatalf("LAN state was not preserved: %+v", cfg)
	}
	spec, err := serviceSpec(cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--lan", "--lan-address", "192.168.1.24:8766"}
	if !reflect.DeepEqual(spec.Args[4:7], want) {
		t.Fatalf("service LAN args=%#v want %#v", spec.Args, want)
	}
}

func TestServiceArgsExplicitlyDisableLANByDefault(t *testing.T) {
	cfg := daemon.Config{DataDir: "/tmp/state", ListenAddr: "127.0.0.1:8765", ShutdownTimeoutSeconds: 10}
	want := []string{"--data-dir", "/tmp/state", "--listen", "127.0.0.1:8765", "--lan=false", "--shutdown-timeout", "10s"}
	if got := serviceArgs(cfg); !reflect.DeepEqual(got, want) {
		t.Fatalf("service args = %#v, want %#v", got, want)
	}
}

func TestFailedInstallRestoresPreviouslyRunningDaemon(t *testing.T) {
	cause := errors.New("service manager failed")
	cfg := daemon.Config{DataDir: "/tmp/state", ListenAddr: "127.0.0.1:18765", LAN: true, LANAddr: "192.168.1.24:8766", ShutdownTimeoutSeconds: 10}
	called := false
	err := restoreSessionDaemonWith(true, cfg, "http://127.0.0.1:18765", nil, cause, func(args []string, apiURL, dataDir string, _ io.Writer, open bool) error {
		called = true
		if apiURL != "http://127.0.0.1:18765" || dataDir != cfg.DataDir || open {
			t.Fatalf("restore args api=%q data=%q open=%v", apiURL, dataDir, open)
		}
		if !reflect.DeepEqual(args, serviceArgs(cfg)) {
			t.Fatalf("restore daemon args=%#v", args)
		}
		return nil
	})
	if !called || !errors.Is(err, cause) {
		t.Fatalf("restore called=%v err=%v", called, err)
	}
}
