package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCLIConnectDiscoveryAndAllowByServiceAlias(t *testing.T) {
	dataDir := tokenDir(t)
	var paths []string
	server := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
			t.Errorf("missing owner authorization on %s", r.URL.Path)
		}
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "GET /api/mcp/servers":
			_, _ = io.WriteString(w, `[{"id":"svc1","name":"Notion","transport":"streamable_http","endpoint":"https://mcp.notion.com/mcp"}]`)
		case "POST /api/mcp/servers/svc1/tools/discover":
			_, _ = io.WriteString(w, `{"tools":[{"name":"read_page","description":"Read a page"}]}`)
		case "PATCH /api/mcp/servers/svc1/permissions":
			var body struct {
				ReadTools  []string `json:"read_tools"`
				WriteTools []string `json:"write_tools"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.ReadTools) != 1 || body.ReadTools[0] != "read_page" || len(body.WriteTools) != 1 || body.WriteTools[0] != "create_page" {
				t.Errorf("unexpected permission body: %+v err=%v", body, err)
			}
			_, _ = io.WriteString(w, `{"agent_grants_changed":false}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	flags := []string{"--url", server.URL, "--data-dir", dataDir}
	var out strings.Builder
	if err := Run(append([]string{"connect", "discover"}, append(flags, "notion")...), nil, &out, io.Discard); err != nil {
		t.Fatalf("discover: %v", err)
	}
	if !strings.Contains(out.String(), "Read a page") || !strings.Contains(out.String(), "no tools were called") {
		t.Fatalf("discovery output missing metadata-only boundary: %s", out.String())
	}
	out.Reset()
	allowArgs := append([]string{"connect", "allow"}, flags...)
	allowArgs = append(allowArgs, "--read-tool", "read_page", "--write-tool", "create_page", "notion")
	if err := Run(allowArgs, nil, &out, io.Discard); err != nil {
		t.Fatalf("allow: %v", err)
	}
	if !strings.Contains(out.String(), "does not grant tools to any agent") || !strings.Contains(out.String(), "approval") {
		t.Fatalf("allow output omitted separate-agent or approval boundary: %s", out.String())
	}
	want := []string{"GET /api/mcp/servers", "POST /api/mcp/servers/svc1/tools/discover", "GET /api/mcp/servers", "PATCH /api/mcp/servers/svc1/permissions"}
	if len(paths) != len(want) {
		t.Fatalf("unexpected requests: %v", paths)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("request %d = %q want %q; all=%v", i, paths[i], want[i], paths)
		}
	}
}

func TestDaemonPairAndDeviceCommands(t *testing.T) {
	dataDir := tokenDir(t)
	server := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Errorf("missing owner authorization")
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/access":
			_, _ = io.WriteString(w, `{"enabled":true}`)
		case "POST /api/devices/pairing":
			_, _ = io.WriteString(w, `{"pairing_url":"https://ownbot.example/dashboard#pair=one-use-secret","expires_at":"2026-10-28T20:00:00Z","certificate_fingerprint":"SHA256:fixture"}`)
		case "GET /api/devices":
			_, _ = io.WriteString(w, `[{"id":"device1","name":"Phone","created_at":"now","revoked_at":""}]`)
		case "DELETE /api/devices/device1":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	flags := []string{"--url", server.URL, "--data-dir", dataDir}
	var out strings.Builder
	if err := Run(append([]string{"daemon", "pair"}, flags...), nil, &out, io.Discard); err != nil {
		t.Fatalf("pair: %v", err)
	}
	if !strings.Contains(out.String(), "one-use-secret") || !strings.Contains(out.String(), "verify this before trusting") || strings.Contains(out.String(), testToken) {
		t.Fatalf("pair output boundary incorrect: %s", out.String())
	}
	out.Reset()
	if err := Run(append([]string{"daemon", "devices"}, flags...), nil, &out, io.Discard); err != nil || !strings.Contains(out.String(), "device1") {
		t.Fatalf("list devices: err=%v output=%s", err, out.String())
	}
	out.Reset()
	if err := Run(append([]string{"daemon", "devices", "--revoke", "device1"}, flags...), nil, &out, io.Discard); err != nil || !strings.Contains(out.String(), "Revoked") {
		t.Fatalf("revoke device: err=%v output=%s", err, out.String())
	}
}

func TestSetupAliasesPrintCanonicalHelp(t *testing.T) {
	for _, args := range [][]string{{"setup", "model", "--help"}, {"setup", "agent", "--help"}, {"setup", "search", "--help"}} {
		var out strings.Builder
		if err := Run(args, nil, &out, io.Discard); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if !strings.Contains(out.String(), "ownbot setup "+args[1]) {
			t.Fatalf("%v did not show the canonical setup alias: %s", args, out.String())
		}
	}
}
