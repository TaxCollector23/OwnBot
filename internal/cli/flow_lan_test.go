package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDaemonStopUsesOwnerTokenAndWaitsForShutdown(t *testing.T) {
	dataDir := tokenDir(t)
	var stopped bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/daemon/shutdown":
			if r.Header.Get("Authorization") != "Bearer "+testToken {
				t.Errorf("missing local owner token")
			}
			if r.Method != http.MethodPost {
				t.Errorf("shutdown method=%s", r.Method)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 0 {
				t.Errorf("shutdown body=%v err=%v", body, err)
			}
			stopped = true
			_, _ = io.WriteString(w, `{"shutting_down":true}`)
		case "/api/status":
			if stopped {
				http.Error(w, "stopped", http.StatusServiceUnavailable)
				return
			}
			_, _ = io.WriteString(w, `{"ok":true,"service":"ownyourbot"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	var out strings.Builder
	if err := runDaemon([]string{"stop", "--url", api.URL, "--data-dir", dataDir}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "daemon stopped") {
		t.Fatalf("output=%q", out.String())
	}
}

func TestDaemonStartLanDoesNotClaimExistingNonLanDaemon(t *testing.T) {
	dataDir := tokenDir(t)
	var requests int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path == "/api/status" {
			_, _ = io.WriteString(w, `{"ok":true,"service":"ownyourbot"}`)
			return
		}
		if r.URL.Path == "/api/access" {
			if r.Header.Get("Authorization") != "Bearer "+testToken {
				t.Errorf("missing local owner token")
			}
			_, _ = io.WriteString(w, `{"enabled":false,"urls":[],"certificate_fingerprint":""}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer api.Close()
	config := `{"data_dir":"` + filepath.ToSlash(dataDir) + `","listen_addr":"` + strings.TrimPrefix(api.URL, "http://") + `","lan":true,"lan_addr":"192.168.1.10:8766","shutdown_timeout_seconds":10}`
	configPath := filepath.Join(t.TempDir(), "daemon.json")
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	err := runDaemon([]string{"start", "--config", configPath}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "daemon stop") || !strings.Contains(err.Error(), "start --lan") || requests != 3 {
		t.Fatalf("err=%v requests=%d", err, requests)
	}
}

func TestGenericOKServiceDoesNotCountAsOwnYourBotOrGetStopped(t *testing.T) {
	var requests int
	var shutdowns int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path == "/api/daemon/shutdown" {
			shutdowns++
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer api.Close()
	if daemonHealthy(api.URL) {
		t.Fatal("generic ok status was accepted as OwnYourBot")
	}
	err := startDaemon(nil, api.URL, t.TempDir(), io.Discard, false)
	if err == nil || !strings.Contains(err.Error(), "occupied") || !strings.Contains(err.Error(), "--listen") || requests != 2 || shutdowns != 0 {
		t.Fatalf("err=%v requests=%d shutdowns=%d", err, requests, shutdowns)
	}
}

func TestDaemonStartLanRejectsDifferentExistingLANAddress(t *testing.T) {
	dataDir := tokenDir(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/status" {
			_, _ = io.WriteString(w, `{"ok":true,"service":"ownyourbot"}`)
			return
		}
		if r.URL.Path == "/api/access" {
			if r.Header.Get("Authorization") != "Bearer "+testToken {
				t.Errorf("missing local owner token")
			}
			_, _ = io.WriteString(w, `{"enabled":true,"urls":["https://192.168.1.20:8766/dashboard"],"certificate_fingerprint":"AA"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer api.Close()
	config := `{"data_dir":"` + filepath.ToSlash(dataDir) + `","listen_addr":"` + strings.TrimPrefix(api.URL, "http://") + `","lan":true,"lan_addr":"192.168.1.10:8766","shutdown_timeout_seconds":10}`
	configPath := filepath.Join(t.TempDir(), "daemon.json")
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	err := runDaemon([]string{"start", "--config", configPath}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "different LAN address") || !strings.Contains(err.Error(), "192.168.1.10:8766") {
		t.Fatalf("unexpected result: %v", err)
	}
}
