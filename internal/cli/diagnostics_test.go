package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInspectCheckEmptySetupIsInformationalAndReadOnly(t *testing.T) {
	dataDir := tokenDir(t)
	var methods []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		if r.URL.Path == "/api/status" && r.Header.Get("Authorization") == "" {
			_, _ = io.WriteString(w, `{"ok":true,"service":"ownbot"}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Errorf("missing local authorization on %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("diagnostics made a non-GET request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected write", http.StatusMethodNotAllowed)
			return
		}
		responses := map[string]string{
			"/api/status":      `{"ok":true,"service":"ownbot","version":"development"}`,
			"/api/models":      `[]`,
			"/api/agents":      `[]`,
			"/api/tools":       `[]`,
			"/api/mcp/servers": `[]`,
			"/api/approvals":   `[]`,
		}
		body, ok := responses[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	defer api.Close()

	var stdout, stderr strings.Builder
	err := runDiagnostics([]string{"--json", "--url", api.URL, "--data-dir", dataDir}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("empty setup should exit successfully: %v\nstderr: %s\nstdout: %s", err, stderr.String(), stdout.String())
	}
	var report diagnosticReport
	if err := json.Unmarshal([]byte(stdout.String()), &report); err != nil {
		t.Fatalf("diagnostics output is not JSON: %v\n%s", err, stdout.String())
	}
	if report.OverallStatus != "info" {
		t.Fatalf("empty setup overall status = %q, want info: %+v", report.OverallStatus, report)
	}
	for _, want := range []string{"No model configurations are saved", "No agents are configured", "No agent has tools selected", "No MCP integrations are configured"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("report missing setup guidance %q:\n%s", want, stdout.String())
		}
	}
	wantRoutes := map[string]int{
		"GET /api/status": 2, "GET /api/models": 1, "GET /api/agents": 1,
		"GET /api/tools": 1, "GET /api/mcp/servers": 1, "GET /api/approvals": 1,
	}
	for _, route := range methods {
		if wantRoutes[route] == 0 {
			t.Errorf("unexpected diagnostics request %q", route)
		} else {
			wantRoutes[route]--
		}
	}
	for route, remaining := range wantRoutes {
		if remaining > 0 {
			t.Errorf("missing %d expected read-only request(s) for %q", remaining, route)
		}
	}
	if strings.Contains(stdout.String()+stderr.String(), testToken) {
		t.Fatal("report exposed the local API token")
	}
}

func TestInspectCheckDisabledMCPAndMissingBindingWarnings(t *testing.T) {
	serverConfig := `[{"id":"serverA","name":"Calendar","enabled":true,"runtime_available":true,"allowed_tools":["calendar_read"]}]`
	toolList := `[{"name":"mcp_serverA_calendar_read_abcd123456","source":"mcp","server":"serverA","provider_tool":"calendar_read"}]`
	tests := []struct {
		name       string
		agent      string
		wantMCP    string
		wantStatus string
		wantError  bool
	}{
		{
			name:    "agent MCP disabled",
			agent:   `[{"id":"a1","name":"Assistant","model":"model-1","enabled":true,"mcp_enabled":false,"mcp_servers":["serverA"],"tools":["mcp_serverA_calendar_read_abcd123456"]}]`,
			wantMCP: "MCP is disabled for that agent", wantStatus: "warning", wantError: true,
		},
		{
			name:    "selected MCP server not bound",
			agent:   `[{"id":"a1","name":"Assistant","model":"model-1","enabled":true,"mcp_enabled":true,"mcp_servers":[],"tools":["mcp_serverA_calendar_read_abcd123456"]}]`,
			wantMCP: "not in the agent's MCP server allowlist", wantStatus: "warning", wantError: true,
		},
		{
			name:    "selected MCP server correctly bound",
			agent:   `[{"id":"a1","name":"Assistant","model":"model-1","enabled":true,"mcp_enabled":true,"mcp_servers":["serverA"],"tools":["mcp_serverA_calendar_read_abcd123456"]}]`,
			wantMCP: "1 MCP integration(s) configured", wantStatus: "info", wantError: false,
		},
		{
			name:    "older daemon omits server allowlist field",
			agent:   `[{"id":"a1","name":"Assistant","model":"model-1","enabled":true,"mcp_enabled":true,"tools":["mcp_serverA_calendar_read_abcd123456"]}]`,
			wantMCP: "does not expose per-agent MCP server bindings", wantStatus: "info", wantError: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dataDir := tokenDir(t)
			var requests []string
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests = append(requests, r.Method+" "+r.URL.Path)
				if r.URL.Path == "/api/status" && r.Header.Get("Authorization") == "" {
					_, _ = io.WriteString(w, `{"ok":true,"service":"ownbot"}`)
					return
				}
				if r.Header.Get("Authorization") != "Bearer "+testToken {
					t.Errorf("missing local authorization on %s", r.URL.Path)
				}
				if r.Method != http.MethodGet {
					t.Errorf("diagnostics made a non-GET request: %s %s", r.Method, r.URL.Path)
					http.Error(w, "unexpected write", http.StatusMethodNotAllowed)
					return
				}
				responses := map[string]string{
					"/api/status":      `{"ok":true,"service":"ownbot","version":"dev"}`,
					"/api/models":      `[{"id":"model-1","last_probe":{"restricted":false}}]`,
					"/api/agents":      test.agent,
					"/api/tools":       toolList,
					"/api/mcp/servers": serverConfig,
					"/api/approvals":   `[{"status":"pending","args":{"secret":"approval-argument-must-not-appear"}}]`,
				}
				_, _ = io.WriteString(w, responses[r.URL.Path])
			}))
			defer api.Close()

			var stdout, stderr strings.Builder
			err := runDiagnostics([]string{"--json", "--url", api.URL, "--data-dir", dataDir}, &stdout, &stderr)
			if (err != nil) != test.wantError {
				t.Fatalf("error = %v, wantError %t; stdout: %s", err, test.wantError, stdout.String())
			}
			var report diagnosticReport
			if err := json.Unmarshal([]byte(stdout.String()), &report); err != nil {
				t.Fatalf("diagnostics output is not JSON: %v\n%s", err, stdout.String())
			}
			mcpCheck := diagnosticCheckByName(t, report, "mcp")
			if !strings.Contains(mcpCheck.Summary+" "+strings.Join(mcpCheck.Details, " "), test.wantMCP) {
				t.Fatalf("MCP report missing %q: %+v", test.wantMCP, mcpCheck)
			}
			if report.OverallStatus != test.wantStatus {
				t.Fatalf("overall status = %q, want %q: %+v", report.OverallStatus, test.wantStatus, report)
			}
			if strings.Contains(stdout.String()+stderr.String(), testToken) {
				t.Fatal("report exposed the local API token")
			}
			if strings.Contains(stdout.String()+stderr.String(), "approval-argument-must-not-appear") {
				t.Fatal("report exposed approval arguments")
			}
			for _, request := range requests {
				if strings.HasPrefix(request, "POST ") || strings.HasPrefix(request, "PATCH ") || strings.HasPrefix(request, "DELETE ") {
					t.Errorf("diagnostics made a mutating request: %s", request)
				}
			}
		})
	}
}

func TestInspectCheckIdentityFailureIsUnknownAndSendsNoToken(t *testing.T) {
	dataDir := tokenDir(t)
	var receivedAuthorization bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			receivedAuthorization = true
		}
		_, _ = io.WriteString(w, `{"ok":true,"service":"not-ownbot"}`)
	}))
	defer api.Close()

	var stdout, stderr strings.Builder
	err := runDiagnostics([]string{"--json", "--url", api.URL, "--data-dir", dataDir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("identity mismatch should return a non-zero diagnostic result")
	}
	if receivedAuthorization {
		t.Fatal("identity mismatch received the local API token")
	}
	var report diagnosticReport
	if err := json.Unmarshal([]byte(stdout.String()), &report); err != nil {
		t.Fatalf("diagnostics output is not JSON: %v\n%s", err, stdout.String())
	}
	if report.OverallStatus != "unknown" {
		t.Fatalf("overall status = %q, want unknown", report.OverallStatus)
	}
	if strings.Contains(stdout.String()+stderr.String(), testToken) {
		t.Fatal("report exposed the local API token")
	}
}

func TestInspectCheckEndpointFailureStaysUnknownAndHidesServerError(t *testing.T) {
	dataDir := tokenDir(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/status" && r.Header.Get("Authorization") == "" {
			_, _ = io.WriteString(w, `{"ok":true,"service":"ownbot"}`)
			return
		}
		if r.URL.Path == "/api/models" {
			http.Error(w, `{"error":"provider-key-leak-marker"}`, http.StatusInternalServerError)
			return
		}
		responses := map[string]string{
			"/api/status":      `{"ok":true,"service":"ownbot"}`,
			"/api/agents":      `[]`,
			"/api/tools":       `[]`,
			"/api/mcp/servers": `[]`,
			"/api/approvals":   `[]`,
		}
		_, _ = io.WriteString(w, responses[r.URL.Path])
	}))
	defer api.Close()

	var stdout, stderr strings.Builder
	err := runDiagnostics([]string{"--json", "--url", api.URL, "--data-dir", dataDir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("failed metadata endpoint should make the command non-zero")
	}
	var report diagnosticReport
	if err := json.Unmarshal([]byte(stdout.String()), &report); err != nil {
		t.Fatalf("diagnostics output is not JSON: %v\n%s", err, stdout.String())
	}
	if report.OverallStatus != "unknown" || diagnosticCheckByName(t, report, "models").Status != "unknown" {
		t.Fatalf("failed model endpoint must remain unknown: %+v", report)
	}
	if strings.Contains(stdout.String()+stderr.String(), "provider-key-leak-marker") {
		t.Fatal("report echoed endpoint error content")
	}
}

func TestInspectCheckNullMetadataEndpointIsUnknown(t *testing.T) {
	dataDir := tokenDir(t)
	api := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		responses := map[string]string{
			"/api/status":      `{"ok":true,"service":"ownbot"}`,
			"/api/models":      `null`,
			"/api/agents":      `[]`,
			"/api/tools":       `[]`,
			"/api/mcp/servers": `[]`,
			"/api/approvals":   `[]`,
		}
		_, _ = io.WriteString(w, responses[r.URL.Path])
	}))
	defer api.Close()

	var stdout, stderr strings.Builder
	err := runDiagnostics([]string{"--json", "--url", api.URL, "--data-dir", dataDir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("null metadata should make the command non-zero")
	}
	var report diagnosticReport
	if err := json.Unmarshal([]byte(stdout.String()), &report); err != nil {
		t.Fatalf("diagnostics output is not JSON: %v\n%s", err, stdout.String())
	}
	if report.OverallStatus != "unknown" || diagnosticCheckByName(t, report, "models").Status != "unknown" {
		t.Fatalf("null models should remain unknown, not zero models: %+v", report)
	}
}

func TestInspectCheckEnabledAgentModelAndWebSearchReadiness(t *testing.T) {
	tests := []struct {
		name       string
		models     string
		agents     string
		tools      string
		wantStatus string
		wantAgent  string
		wantTool   string
		wantError  bool
	}{
		{
			name:       "enabled agent without model",
			models:     `[]`,
			agents:     `[{"name":"Ready agent","enabled":true,"tools":[]}]`,
			tools:      `[]`,
			wantStatus: "warning",
			wantAgent:  "no model configuration selected",
			wantError:  true,
		},
		{
			name:       "disabled agent without model is informational",
			models:     `[]`,
			agents:     `[{"name":"Draft agent","enabled":false,"tools":[]}]`,
			tools:      `[]`,
			wantStatus: "info",
			wantAgent:  "no model configuration selected",
			wantError:  false,
		},
		{
			name:       "enabled web search selection has its toggle off",
			models:     `[{"id":"model-1","last_probe":{"restricted":false}}]`,
			agents:     `[{"name":"Search agent","model":"model-1","enabled":true,"web_search":false,"tools":["web_search"]}]`,
			tools:      `[{"name":"web_search","source":"web_search"}]`,
			wantStatus: "warning",
			wantTool:   "selects web search while web search is disabled",
			wantError:  true,
		},
		{
			name:       "web search off with no selected search tool is safe",
			models:     `[{"id":"model-1","last_probe":{"restricted":false}}]`,
			agents:     `[{"name":"Text agent","model":"model-1","enabled":true,"web_search":false,"tools":[]}]`,
			tools:      `[{"name":"web_search","source":"web_search"}]`,
			wantStatus: "info",
			wantAgent:  "Web search is off for all agents",
			wantError:  false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dataDir := tokenDir(t)
			api := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("diagnostics made a non-GET request: %s %s", r.Method, r.URL.Path)
					http.Error(w, "unexpected write", http.StatusMethodNotAllowed)
					return
				}
				if r.Header.Get("Authorization") != "Bearer "+testToken {
					t.Errorf("missing local authorization on %s", r.URL.Path)
				}
				responses := map[string]string{
					"/api/status":      `{"ok":true,"service":"ownbot","version":"dev"}`,
					"/api/models":      test.models,
					"/api/agents":      test.agents,
					"/api/tools":       test.tools,
					"/api/mcp/servers": `[]`,
					"/api/approvals":   `[]`,
				}
				_, _ = io.WriteString(w, responses[r.URL.Path])
			}))
			defer api.Close()

			var stdout, stderr strings.Builder
			err := runDiagnostics([]string{"--json", "--url", api.URL, "--data-dir", dataDir}, &stdout, &stderr)
			if (err != nil) != test.wantError {
				t.Fatalf("error = %v, wantError %t; output: %s", err, test.wantError, stdout.String())
			}
			var report diagnosticReport
			if err := json.Unmarshal([]byte(stdout.String()), &report); err != nil {
				t.Fatalf("diagnostics output is not JSON: %v\n%s", err, stdout.String())
			}
			if report.OverallStatus != test.wantStatus {
				t.Fatalf("overall status = %q, want %q: %+v", report.OverallStatus, test.wantStatus, report)
			}
			if test.wantAgent != "" {
				check := diagnosticCheckByName(t, report, "agents")
				if !strings.Contains(check.Summary+" "+strings.Join(check.Details, " "), test.wantAgent) {
					t.Fatalf("agent report missing %q: %+v", test.wantAgent, check)
				}
			}
			if test.wantTool != "" {
				check := diagnosticCheckByName(t, report, "tools")
				if !strings.Contains(check.Summary+" "+strings.Join(check.Details, " "), test.wantTool) {
					t.Fatalf("tool report missing %q: %+v", test.wantTool, check)
				}
			}
		})
	}
}

func diagnosticCheckByName(t *testing.T, report diagnosticReport, name string) diagnosticCheck {
	t.Helper()
	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("missing %q check in report: %+v", name, report)
	return diagnosticCheck{}
}
