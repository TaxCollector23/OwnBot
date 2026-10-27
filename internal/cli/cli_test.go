package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"ownbot/internal/server"
	"path/filepath"
	"strings"
	"testing"
)

const testToken = "cli-test-token"

func tokenDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "api.token"), []byte(testToken+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSetupFromConfigIsHeadlessAndCreatesDisabledObjects(t *testing.T) {
	dataDir := tokenDir(t)
	configPath := filepath.Join(t.TempDir(), "setup.json")
	config := `{"model":{"name":"Mini PC model","provider":"ollama","model":"qwen3:8b"},"agent":{"name":"Calendar assistant","prompt":"Be careful."}}`
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	var modelCreated, agentCreated bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
			t.Errorf("authorization header missing or wrong")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
			return
		}
		switch r.URL.Path {
		case "/api/models":
			modelCreated = r.Method == http.MethodPost && body["provider"] == "ollama" && body["model"] == "qwen3:8b"
			_ = json.NewEncoder(w).Encode(modelConfig{ID: "model-1", Name: "Mini PC model", Model: "qwen3:8b", Provider: "ollama"})
		case "/api/agents":
			agentCreated = r.Method == http.MethodPost && body["enabled"] == false && body["model"] == "qwen3:8b" && body["prompt"] == "Be careful."
			_ = json.NewEncoder(w).Encode(agentConfig{ID: "agent-1", Name: "Calendar assistant", Model: "qwen3:8b"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	var stdout, stderr strings.Builder
	err := Run([]string{"setup", "--config", configPath, "--url", server.URL, "--data-dir", dataDir}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("headless setup failed: %v (stderr: %s)", err, stderr.String())
	}
	if !modelCreated || !agentCreated {
		t.Fatalf("expected both API creates, model=%t agent=%t", modelCreated, agentCreated)
	}
	if !strings.Contains(stdout.String(), "disabled agent") || !strings.Contains(stdout.String(), "No model was probed") {
		t.Fatalf("setup output overstates behavior: %s", stdout.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), testToken) {
		t.Fatal("setup output exposed the API token")
	}
}

func TestHeadlessSetupAgainstActualDaemonAPI(t *testing.T) {
	dataDir := t.TempDir()
	app, err := server.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	api := httptest.NewServer(app.Handler())
	defer api.Close()
	configPath := filepath.Join(t.TempDir(), "setup.json")
	config := `{"model":{"name":"Ollama local","provider":"ollama","model":"qwen3:8b"},"agent":{"name":"Assistant"}}`
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := Run([]string{"setup", "--config", configPath, "--url", api.URL, "--data-dir", dataDir}, strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatalf("actual-daemon headless setup: %v", err)
	}
	token, err := os.ReadFile(filepath.Join(dataDir, "api.token"))
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string, target any) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, api.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %s", path, response.Status)
		}
		if err := json.NewDecoder(response.Body).Decode(target); err != nil {
			t.Fatal(err)
		}
	}
	var models []modelConfig
	get("/api/models", &models)
	var agents []agentConfig
	get("/api/agents", &agents)
	if len(models) != 1 || models[0].Model != "qwen3:8b" {
		t.Fatalf("saved models: %+v", models)
	}
	if len(agents) != 1 || agents[0].Enabled || agents[0].Model != "qwen3:8b" {
		t.Fatalf("saved agents: %+v", agents)
	}
	if !strings.Contains(out.String(), "No model was probed") {
		t.Fatalf("setup output: %s", out.String())
	}
}

func TestSetupInteractiveReadsBothPromptsFromSameInput(t *testing.T) {
	dataDir := tokenDir(t)
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/api/models" {
			_ = json.NewEncoder(w).Encode(modelConfig{ID: "m", Name: "ollama · local-model", Provider: "ollama", Model: "local-model"})
		} else {
			_ = json.NewEncoder(w).Encode(agentConfig{ID: "a", Name: "Assistant", Model: "local-model"})
		}
	}))
	defer server.Close()
	var stdout strings.Builder
	err := Run([]string{"setup", "--url", server.URL, "--data-dir", dataDir}, strings.NewReader("local-model\n\n"), &stdout, io.Discard)
	if err != nil {
		t.Fatalf("interactive setup: %v", err)
	}
	if strings.Join(paths, ",") != "/api/models,/api/agents" {
		t.Fatalf("setup endpoints: %v", paths)
	}
	if !strings.Contains(stdout.String(), "Agent name [Assistant]") {
		t.Fatalf("setup should present the agent prompt: %s", stdout.String())
	}
}

func TestModelCommandsUseTokenAndSendEmptyProbeBody(t *testing.T) {
	dataDir := tokenDir(t)
	var probeBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Errorf("request did not use token file")
		}
		switch r.URL.Path {
		case "/api/models":
			if r.Method == http.MethodGet {
				_, _ = io.WriteString(w, `[{"id":"m1","name":"local","provider":"ollama","model":"qwen"}]`)
				return
			}
			_, _ = io.WriteString(w, `{"id":"m2","name":"local","provider":"ollama","model":"qwen"}`)
		case "/api/models/m1/probe":
			body, _ := io.ReadAll(r.Body)
			probeBody = string(body)
			_, _ = io.WriteString(w, `{"model_id":"m1","probe":{"score":75,"restricted":true}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	for _, args := range [][]string{
		{"model", "list", "--url", server.URL, "--data-dir", dataDir},
		{"model", "add", "--url", server.URL, "--data-dir", dataDir, "--model", "qwen", "--provider", "ollama"},
		{"model", "probe", "--url", server.URL, "--data-dir", dataDir, "m1"},
	} {
		if err := Run(args, strings.NewReader(""), io.Discard, io.Discard); err != nil {
			t.Fatalf("Run(%q): %v", args, err)
		}
	}
	if probeBody != "{}" {
		t.Fatalf("probe must send empty JSON, got %q", probeBody)
	}
}

func TestConnectAddStoresDisabledMetadataOnly(t *testing.T) {
	dataDir := tokenDir(t)
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/mcp/servers" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if got["enabled"] != false {
			t.Errorf("connection metadata must be disabled: %#v", got["enabled"])
		}
		_ = json.NewEncoder(w).Encode(mcpConfig{ID: "mcp-1", Name: "local tools", Enabled: false, RuntimeAvailable: false})
	}))
	defer server.Close()
	var out strings.Builder
	err := Run([]string{"connect", "add", "--url", server.URL, "--data-dir", dataDir, "--name", "local tools", "--command", "/usr/local/bin/example", "--tool", "lookup", "--read-tool", "lookup"}, strings.NewReader(""), &out, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if got["command"] != "/usr/local/bin/example" || !strings.Contains(out.String(), "No provider connection or tool is active") {
		t.Fatalf("unexpected connect result: %#v / %s", got, out.String())
	}
}

func TestAuditExportWritesJSONLAndReportsCurrentScope(t *testing.T) {
	dataDir := tokenDir(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/audit" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `[{"id":1,"event_type":"agent.created"},{"id":2,"event_type":"model.probed"}]`)
	}))
	defer server.Close()
	var stdout, stderr strings.Builder
	err := Run([]string{"audit", "export", "--url", server.URL, "--data-dir", dataDir}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "agent.created") || !strings.Contains(stderr.String(), "tool-call audit history is not implemented") {
		t.Fatalf("unexpected audit export: %q; note %q", stdout.String(), stderr.String())
	}
}

func TestUnavailableCommandsDoNotClaimSuccessOrCallAPI(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; http.NotFound(w, r) }))
	defer server.Close()
	dataDir := tokenDir(t)
	var out strings.Builder
	err := Run([]string{"logs", "--url", server.URL, "--data-dir", dataDir}, strings.NewReader(""), &out, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "no /api/logs endpoint") || calls != 0 || !strings.Contains(out.String(), "inspect the ownbotd process stdout/stderr") {
		t.Fatalf("logs command should be explicit and offline: err=%v calls=%d out=%s", err, calls, out.String())
	}
	err = Run([]string{"update"}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "unavailable") || calls != 0 {
		t.Fatalf("update should be unavailable: err=%v calls=%d", err, calls)
	}
}

func TestPromptRollbackCallsDaemonEndpoint(t *testing.T) {
	dataDir := tokenDir(t)
	var gotBody map[string]int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/agents/a1/prompts/rollback" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = io.WriteString(w, `{"version":1,"prompt":"restored"}`)
	}))
	defer server.Close()
	var out strings.Builder
	err := Run([]string{"agent", "prompt", "rollback", "--id", "a1", "--version", "1", "--url", server.URL, "--data-dir", dataDir}, strings.NewReader(""), &out, io.Discard)
	if err != nil || gotBody["version"] != 1 || !strings.Contains(out.String(), "No agent runtime was changed") {
		t.Fatalf("rollback request failed: err=%v body=%v out=%s", err, gotBody, out.String())
	}
}

func TestMissingTokenDoesNotMakeAPIRequest(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer server.Close()
	var stderr strings.Builder
	err := Run([]string{"model", "list", "--url", server.URL, "--data-dir", t.TempDir()}, strings.NewReader(""), io.Discard, &stderr)
	if err == nil || calls != 0 || strings.Contains(stderr.String(), testToken) {
		t.Fatalf("missing token should fail before request: err=%v calls=%d stderr=%s", err, calls, stderr.String())
	}
}

func TestSetupConfigRejectsCredentialFieldsWithoutEchoingValues(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "setup.json")
	if err := os.WriteFile(configPath, []byte(`{"api_key":"never-print-this","model":{"model":"local"},"agent":{"name":"Assistant"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	err := Run([]string{"setup", "--config", configPath}, strings.NewReader(""), &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("credential config field should be rejected: %v", err)
	}
	if strings.Contains(err.Error()+stdout.String()+stderr.String(), "never-print-this") {
		t.Fatal("credential value appeared in CLI output")
	}
}

func TestHelpDoesNotCallAPI(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer server.Close()
	var stderr strings.Builder
	err := Run([]string{"model", "list", "--help"}, strings.NewReader(""), io.Discard, &stderr)
	if err != nil || calls != 0 || !strings.Contains(stderr.String(), "loopback URL") {
		t.Fatalf("help should print flags without requesting API: err=%v calls=%d stderr=%s", err, calls, stderr.String())
	}
}
