package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"ownbot/internal/server"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func ownBotTestServer(next http.Handler) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/status" {
			_, _ = io.WriteString(w, `{"ok":true,"service":"ownbot"}`)
			return
		}
		next.ServeHTTP(w, r)
	}))
}

const testToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func tokenDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "api.token"), []byte(testToken+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSetupFromConfigIsHeadlessAndCreatesToolFreeAgent(t *testing.T) {
	dataDir := tokenDir(t)
	configPath := filepath.Join(t.TempDir(), "setup.toml")
	var modelCreated, agentCreated bool
	server := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/status" {
			_, _ = io.WriteString(w, `{"ok":true,"service":"ownbot"}`)
			return
		}
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"qwen3:8b"}]}`)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
			t.Errorf("authorization header missing or wrong")
		}
		if r.URL.Path == "/api/models/model-1/probe" {
			_, _ = io.WriteString(w, `{"probe":{"score":100,"json_output":true,"single_tool":true,"tool_choice":true,"argument_accuracy":true,"restricted":false}}`)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
			return
		}
		switch r.URL.Path {
		case "/api/models":
			modelCreated = r.Method == http.MethodPost && body["provider"] == "openai_compatible" && body["model"] == "qwen3:8b"
			_ = json.NewEncoder(w).Encode(modelConfig{ID: "model-1", Name: "Mini PC model", Model: "qwen3:8b", Provider: "openai_compatible"})
		case "/api/agents":
			agentCreated = r.Method == http.MethodPost && body["enabled"] == true && body["model"] == "qwen3:8b" && body["prompt"] == "Be careful." && len(body["tools"].([]any)) == 0
			_ = json.NewEncoder(w).Encode(agentConfig{ID: "agent-1", Name: "Calendar assistant", Model: "qwen3:8b"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	config := "[model]\nname = \"Mini PC model\"\nprovider = \"openai_compatible\"\nbase_url = \"" + server.URL + "/v1\"\nmodel = \"stale-config-model\"\n\n[agent]\nname = \"Calendar assistant\"\nprompt = \"Be careful.\"\n"
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	err := Run([]string{"setup", "--yes", "--model", "qwen3:8b", "--config", configPath, "--url", server.URL, "--data-dir", dataDir}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("headless setup failed: %v (stderr: %s)", err, stderr.String())
	}
	if !modelCreated || !agentCreated {
		t.Fatalf("expected both API creates, model=%t agent=%t", modelCreated, agentCreated)
	}
	if !strings.Contains(stdout.String(), "enabled, text-only agent") || !strings.Contains(stdout.String(), "saved and probed model") {
		t.Fatalf("setup output overstates behavior: %s", stdout.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), testToken) {
		t.Fatal("setup output exposed the API token")
	}
}

func TestHeadlessSetupAgainstActualDaemonAPI(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "state")
	app, err := server.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	api := httptest.NewServer(app.Handler())
	defer api.Close()
	modelsHost := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"data":[{"id":"qwen3:8b"}]}`)
			return
		}
		var request struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if len(request.Tools) == 0 {
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"ok\":true}"}}]}`)
			return
		}
		name, value := "probe_one", "alpha"
		if len(request.Tools) == 2 {
			name, value = "probe_two", "beta"
		}
		b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"tool_calls": []any{map[string]any{"id": "c1", "function": map[string]string{"name": name, "arguments": `{"value":"` + value + `"}`}}}}}}})
		_, _ = w.Write(b)
	}))
	defer modelsHost.Close()
	configPath := filepath.Join(t.TempDir(), "setup.json")
	config := `{"model":{"name":"Local service","provider":"openai_compatible","base_url":"` + modelsHost.URL + `/v1","model":"qwen3:8b"},"agent":{"name":"Assistant"}}`
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := Run([]string{"setup", "--yes", "--config", configPath, "--url", api.URL, "--data-dir", dataDir}, strings.NewReader(""), &out, io.Discard); err != nil {
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
	if len(agents) != 1 || !agents[0].Enabled || len(agents[0].Tools) != 0 || agents[0].Model != "qwen3:8b" {
		t.Fatalf("saved agents: %+v", agents)
	}
	if !strings.Contains(out.String(), "saved and probed model") {
		t.Fatalf("setup output: %s", out.String())
	}
}

func TestSetupInteractiveReadsBothPromptsFromSameInput(t *testing.T) {
	dataDir := tokenDir(t)
	var paths []string
	server := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/api/status" {
			_, _ = io.WriteString(w, `{"ok":true,"service":"ownbot"}`)
			return
		}
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"local-model"}]}`)
			return
		}
		if r.URL.Path == "/api/models/m/probe" {
			_, _ = io.WriteString(w, `{"probe":{"score":100,"json_output":true,"single_tool":true,"tool_choice":true,"argument_accuracy":true,"restricted":false}}`)
			return
		}
		if r.URL.Path == "/api/models" {
			_ = json.NewEncoder(w).Encode(modelConfig{ID: "m", Name: "ollama · local-model", Provider: "ollama", Model: "local-model"})
		} else {
			_ = json.NewEncoder(w).Encode(agentConfig{ID: "a", Name: "Assistant", Model: "local-model"})
		}
	}))
	defer server.Close()
	var stdout strings.Builder
	err := Run([]string{"setup", "--base-url", server.URL + "/v1", "--url", server.URL, "--data-dir", dataDir}, strings.NewReader("\n"), &stdout, io.Discard)
	if err != nil {
		t.Fatalf("interactive setup: %v", err)
	}
	if strings.Join(paths, ",") != "/v1/models,/api/models,/api/models/m/probe,/api/agents" {
		t.Fatalf("setup endpoints: %v", paths)
	}
	if !strings.Contains(stdout.String(), "Agent name [Assistant]") {
		t.Fatalf("setup should present the agent prompt: %s", stdout.String())
	}
}

func TestModelCommandsUseTokenAndSendEmptyProbeBody(t *testing.T) {
	dataDir := tokenDir(t)
	var probeBody string
	server := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		{"inspect", "models", "list", "--url", server.URL, "--data-dir", dataDir},
		{"inspect", "models", "add", "--url", server.URL, "--data-dir", dataDir, "--model", "qwen", "--provider", "ollama"},
		{"inspect", "models", "probe", "--url", server.URL, "--data-dir", dataDir, "m1"},
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
	server := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestAuditExportWritesJSONLAndReportsSupportedScope(t *testing.T) {
	dataDir := tokenDir(t)
	server := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/audit" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("limit") != "500" {
			t.Errorf("page size = %q, want 500", r.URL.Query().Get("limit"))
		}
		_, _ = io.WriteString(w, `[{"id":2,"event_type":"model.probed"},{"id":1,"event_type":"agent.created"}]`)
	}))
	defer server.Close()
	var stdout, stderr strings.Builder
	err := Run([]string{"inspect", "audit", "export", "--url", server.URL, "--data-dir", dataDir}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "model.probed") || strings.Contains(stderr.String(), "tool-call audit history is not implemented") || !strings.Contains(stderr.String(), "stored audit events newest first") || !strings.Contains(stderr.String(), "tool-call arguments and results are redacted") || !strings.Contains(stderr.String(), "idempotency keys") {
		t.Fatalf("unexpected audit export: %q; note %q", stdout.String(), stderr.String())
	}
}

func TestAuditExportRejectsOlderDaemonThatIgnoresFiltersBeforeWritingPage(t *testing.T) {
	for _, test := range []struct {
		name  string
		flag  string
		value string
		item  string
	}{
		{
			name:  "run filter",
			flag:  "--run",
			value: "wanted-run",
			item:  `{"id":1,"run_id":"other-run","event_type":"tool.succeeded"}`,
		},
		{
			name:  "event filter",
			flag:  "--event",
			value: "wanted.event",
			item:  `{"id":1,"run_id":"run-1","event_type":"other.event"}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dataDir := tokenDir(t)
			server := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/audit" || r.Method != http.MethodGet {
					http.NotFound(w, r)
					return
				}
				_ = json.NewEncoder(w).Encode([]json.RawMessage{json.RawMessage(test.item)})
			}))
			defer server.Close()

			var stdout, stderr strings.Builder
			err := Run([]string{"inspect", "audit", "export", test.flag, test.value, "--url", server.URL, "--data-dir", dataDir}, strings.NewReader(""), &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), "update or restart the daemon") {
				t.Fatalf("expected old-daemon filter mismatch error, got %v", err)
			}
			if stdout.Len() != 0 {
				t.Fatalf("mismatched filter page was exported: %q", stdout.String())
			}

			outputDir := t.TempDir()
			outputPath := filepath.Join(outputDir, "audit.jsonl")
			const original = "keep prior export\n"
			if err := os.WriteFile(outputPath, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			var fileStdout strings.Builder
			err = Run([]string{"inspect", "audit", "export", test.flag, test.value, "--out", outputPath, "--url", server.URL, "--data-dir", dataDir}, strings.NewReader(""), &fileStdout, &stderr)
			if err == nil {
				t.Fatal("file export with mismatched filter unexpectedly succeeded")
			}
			if fileStdout.Len() != 0 {
				t.Fatalf("file export wrote to stdout: %q", fileStdout.String())
			}
			contents, err := os.ReadFile(outputPath)
			if err != nil || string(contents) != original {
				t.Fatalf("mismatched filter changed existing destination: %q, err=%v", contents, err)
			}
			entries, err := os.ReadDir(outputDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != "audit.jsonl" {
				t.Fatalf("temporary export was left behind: %#v", entries)
			}
		})
	}
}

func TestAuditExportRejectsOldDaemonThatIgnoresCursorWithoutLooping(t *testing.T) {
	dataDir := tokenDir(t)
	var requests int
	server := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/audit" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		requests++
		events := make([]map[string]any, 0, auditExportPageLimit)
		for id := auditExportPageLimit; id > 0; id-- {
			events = append(events, map[string]any{"id": id, "run_id": "run-1", "event_type": "fixture.event"})
		}
		_ = json.NewEncoder(w).Encode(events)
	}))
	defer server.Close()

	var stdout, stderr strings.Builder
	err := Run([]string{"inspect", "audit", "export", "--url", server.URL, "--data-dir", dataDir}, strings.NewReader(""), &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "cursor order") {
		t.Fatalf("expected unsupported cursor error, got %v", err)
	}
	if requests != 2 {
		t.Fatalf("old daemon was queried %d times, want bounded detection after 2 pages", requests)
	}
	if lines := strings.Split(strings.TrimSpace(stdout.String()), "\n"); len(lines) != auditExportPageLimit {
		t.Fatalf("stdout contains %d lines after late cursor error, want first page only", len(lines))
	}
}

func TestAuditExportPaginatesFilteredEventsAndAtomicallyReplacesFile(t *testing.T) {
	dataDir := tokenDir(t)
	const total = 1201
	var requests int
	var cursors []string
	server := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/audit" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		query := r.URL.Query()
		if query.Get("limit") != "500" || query.Get("run_id") != "run/one" || query.Get("event_type") != "tool.succeeded" {
			t.Errorf("unexpected audit query: %s", r.URL.RawQuery)
		}
		requests++
		cursor := query.Get("before_id")
		cursors = append(cursors, cursor)
		upper := total
		if cursor != "" {
			var before int
			if _, err := fmt.Sscanf(cursor, "%d", &before); err != nil {
				t.Errorf("invalid cursor %q: %v", cursor, err)
				return
			}
			upper = before - 1
		}
		lower := upper - 499
		if lower < 1 {
			lower = 1
		}
		events := make([]map[string]any, 0, upper-lower+1)
		for id := upper; id >= lower; id-- {
			events = append(events, map[string]any{"id": id, "run_id": "run/one", "event_type": "tool.succeeded"})
		}
		_ = json.NewEncoder(w).Encode(events)
	}))
	defer server.Close()

	directory := t.TempDir()
	outputPath := filepath.Join(directory, "audit.jsonl")
	if err := os.WriteFile(outputPath, []byte("old export\n"), 0644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	err := Run([]string{"inspect", "audit", "export", "--run", "run/one", "--event", "tool.succeeded", "--out", outputPath, "--url", server.URL, "--data-dir", dataDir}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("audit export failed: %v (%s)", err, stderr.String())
	}
	if stdout.Len() != 0 || requests != 3 || strings.Join(cursors, ",") != ",702,202" {
		t.Fatalf("stdout=%q requests=%d cursors=%v", stdout.String(), requests, cursors)
	}
	contents, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(contents)), "\n")
	if len(lines) != total {
		t.Fatalf("got %d lines, want %d", len(lines), total)
	}
	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("export mode %04o, want 0600", info.Mode().Perm())
	}
	seen := make(map[int]bool, total)
	for i, line := range lines {
		var item struct {
			ID int `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			t.Fatal(err)
		}
		want := total - i
		if item.ID != want || seen[item.ID] {
			t.Fatalf("line %d has ID %d (duplicate=%t), want %d", i, item.ID, seen[item.ID], want)
		}
		seen[item.ID] = true
	}
}

func TestAuditExportSecondPageFailurePreservesDestination(t *testing.T) {
	dataDir := tokenDir(t)
	var requests int
	server := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/audit" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		requests++
		if requests == 2 {
			http.Error(w, `{"error":"fixture second page failure"}`, http.StatusBadGateway)
			return
		}
		events := make([]map[string]any, 0, 500)
		for id := 1000; id >= 501; id-- {
			events = append(events, map[string]any{"id": id, "event_type": "fixture.event"})
		}
		_ = json.NewEncoder(w).Encode(events)
	}))
	defer server.Close()

	directory := t.TempDir()
	outputPath := filepath.Join(directory, "audit.jsonl")
	const original = "preserve this prior export\n"
	if err := os.WriteFile(outputPath, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	err := Run([]string{"inspect", "audit", "export", "--out", outputPath, "--url", server.URL, "--data-dir", dataDir}, strings.NewReader(""), io.Discard, &stderr)
	if err == nil || requests != 2 {
		t.Fatalf("export error=%v requests=%d", err, requests)
	}
	contents, readErr := os.ReadFile(outputPath)
	if readErr != nil || string(contents) != original {
		t.Fatalf("previous output changed: %q, err=%v", contents, readErr)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "audit.jsonl" {
		t.Fatalf("temporary export was left behind: %#v", entries)
	}
}

func TestAuditExportHelpUsesFullCommandAndDocumentsFailureBoundary(t *testing.T) {
	var stdout strings.Builder
	if err := Run([]string{"inspect", "audit", "--help"}, strings.NewReader(""), &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, phrase := range []string{"ownbot inspect audit export", "--run RUN_ID", "--event EVENT_TYPE", "newest-first JSONL", "stored audit events", "Tool-call arguments and results are redacted", "caller-supplied idempotency keys", "0600 on Unix", "partial output"} {
		if !strings.Contains(stdout.String(), phrase) {
			t.Errorf("help missing %q: %s", phrase, stdout.String())
		}
	}
}

func TestUnavailableCommandsDoNotClaimSuccessOrCallAPI(t *testing.T) {
	var calls int
	server := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; http.NotFound(w, r) }))
	defer server.Close()
	dataDir := tokenDir(t)
	var out strings.Builder
	err := Run([]string{"inspect", "logs", "--url", server.URL, "--data-dir", dataDir}, strings.NewReader(""), &out, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "no /api/logs endpoint") || calls != 0 || !strings.Contains(out.String(), "inspect the daemon process stdout/stderr") {
		t.Fatalf("logs command should be explicit and offline: err=%v calls=%d out=%s", err, calls, out.String())
	}
	err = Run([]string{"update"}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "unknown command") || calls != 0 {
		t.Fatalf("unlisted root command should be rejected: err=%v calls=%d", err, calls)
	}
}

func TestPromptRollbackCallsDaemonEndpoint(t *testing.T) {
	dataDir := tokenDir(t)
	var gotBody map[string]int
	server := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	err := Run([]string{"inspect", "agents", "prompt", "rollback", "--id", "a1", "--version", "1", "--url", server.URL, "--data-dir", dataDir}, strings.NewReader(""), &out, io.Discard)
	if err != nil || gotBody["version"] != 1 || !strings.Contains(out.String(), "No agent runtime was changed") {
		t.Fatalf("rollback request failed: err=%v body=%v out=%s", err, gotBody, out.String())
	}
}

func TestAgentRunAndRunLifecycleUseDaemonEndpoints(t *testing.T) {
	dataDir := tokenDir(t)
	var paths []string
	server := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "POST /api/agents/a1/runs":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["input"] != "hello from stdin" {
				t.Errorf("unexpected run input: %v err=%v", body, err)
			}
			_, _ = io.WriteString(w, `{"id":"r1","agent_id":"a1","status":"succeeded","prompt_version":3,"output":"done"}`)
		case "GET /api/runs":
			_, _ = io.WriteString(w, `[{"id":"r1","agent_id":"a1","status":"awaiting_approval","prompt_version":3}]`)
		case "GET /api/runs/r1":
			_, _ = io.WriteString(w, `{"id":"r1","status":"awaiting_approval","prompt_version":3,"steps":[],"tainted":true}`)
		case "POST /api/runs/r1/resume":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 0 {
				t.Errorf("resume body should be empty JSON object: %v err=%v", body, err)
			}
			_, _ = io.WriteString(w, `{"id":"r1","status":"succeeded","prompt_version":3,"output":"continued"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	flags := []string{"--url", server.URL, "--data-dir", dataDir}
	var out strings.Builder
	if err := Run(append([]string{"inspect", "agents", "run", "--id", "a1", "--input", "-"}, flags...), strings.NewReader("hello from stdin"), &out, io.Discard); err != nil {
		t.Fatalf("agent run failed: %v", err)
	}
	if !strings.Contains(out.String(), `"status": "succeeded"`) {
		t.Fatalf("run result not printed: %s", out.String())
	}
	for _, command := range [][]string{{"inspect", "runs", "list"}, {"inspect", "runs", "show"}, {"inspect", "runs", "resume"}} {
		args := append(append(append([]string{}, command...), flags...), "r1")
		if command[2] == "list" {
			args = append([]string{}, command...)
			args = append(args, flags...)
		}
		if err := Run(args, strings.NewReader(""), io.Discard, io.Discard); err != nil {
			t.Fatalf("Run(%v): %v", args, err)
		}
	}
	want := []string{"POST /api/agents/a1/runs", "GET /api/runs", "GET /api/runs/r1", "POST /api/runs/r1/resume"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("unexpected API calls: got %v want %v", paths, want)
	}
}

func TestAgentRunSurfacesDisabledAgentRejection(t *testing.T) {
	dataDir := tokenDir(t)
	server := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"agent is disabled"}`, http.StatusConflict)
	}))
	defer server.Close()
	err := Run([]string{"inspect", "agents", "run", "--id", "a1", "--input", "hello", "--url", server.URL, "--data-dir", dataDir}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "agent is disabled") {
		t.Fatalf("disabled agent rejection should be surfaced: %v", err)
	}
}

func TestMCPConnectLifecycleAndAgentCapabilityUpdate(t *testing.T) {
	dataDir := tokenDir(t)
	var paths []string
	var updateBody map[string]any
	server := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "POST /api/mcp/servers/s1/connect":
			_, _ = io.WriteString(w, `{"id":"s1","name":"Notes","transport":"streamable_http","endpoint":"https://notes.example/mcp","enabled":true,"runtime_available":true}`)
		case "GET /api/tools":
			_, _ = io.WriteString(w, `[{"name":"Lookup","description":"Search notes","risk":"read","source":"mcp","server":"s1"}]`)
		case "POST /api/mcp/servers/s1/disconnect":
			_, _ = io.WriteString(w, `{"id":"s1","name":"Notes","enabled":false,"runtime_available":false}`)
		case "PATCH /api/agents/a1":
			if err := json.NewDecoder(r.Body).Decode(&updateBody); err != nil {
				t.Errorf("decode update: %v", err)
			}
			_, _ = io.WriteString(w, `{"id":"a1","name":"Assistant","enabled":true,"web_search":false,"mcp_enabled":true,"tools":["Lookup"]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	flags := []string{"--url", server.URL, "--data-dir", dataDir}
	for _, args := range [][]string{
		{"connect", "connect"},
		{"connect", "disconnect"},
		{"inspect", "agents", "update", "--id", "a1", "--enabled", "--web-search=false", "--mcp=true", "--tool", "Lookup", "--mcp-server", "s1"},
	} {
		args = append(args, flags...)
		if args[1] == "connect" || args[1] == "disconnect" {
			args = append(args, "s1")
		}
		if err := Run(args, strings.NewReader(""), io.Discard, io.Discard); err != nil {
			t.Fatalf("Run(%v): %v", args, err)
		}
	}
	if updateBody["enabled"] != true || updateBody["web_search"] != false || updateBody["mcp_enabled"] != true {
		t.Fatalf("agent capability fields missing: %#v", updateBody)
	}
	if !reflect.DeepEqual(updateBody["tools"], []any{"Lookup"}) || !reflect.DeepEqual(updateBody["mcp_servers"], []any{"s1"}) {
		t.Fatalf("agent tool/server allowlists missing: %#v", updateBody)
	}
	want := []string{"POST /api/mcp/servers/s1/connect", "GET /api/tools", "POST /api/mcp/servers/s1/disconnect", "PATCH /api/agents/a1"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("API call order: got %v want %v", paths, want)
	}
}

func TestMCPConnectAuthRequiredDoesNotPromptOrEchoCredentials(t *testing.T) {
	dataDir := tokenDir(t)
	server := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"code":"auth_required","error":"MCP authentication required"}`)
	}))
	defer server.Close()
	err := Run([]string{"connect", "connect", "--url", server.URL, "--data-dir", dataDir, "s1"}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "connect authorize") || strings.Contains(err.Error(), testToken) {
		t.Fatalf("auth_required should be clearly reported without credentials: %v", err)
	}
}

func TestMCPAuthorizePrintsOnlyTheProviderURL(t *testing.T) {
	dataDir := tokenDir(t)
	var gotPath string
	server := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.Method + " " + r.URL.Path
		_, _ = io.WriteString(w, `{"authorization_url":"https://issuer.example/authorize?client_id=public&state=one-use","callback_port":41999,"connected":false,"grants_created":false}`)
	}))
	defer server.Close()
	var stdout bytes.Buffer
	err := Run([]string{"connect", "authorize", "--ssh-target", "user@server.example", "--url", server.URL, "--data-dir", dataDir, "s1"}, strings.NewReader(""), &stdout, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "POST /api/mcp/servers/s1/oauth/start" || !strings.Contains(stdout.String(), "ssh -L 127.0.0.1:41999:127.0.0.1:41999 -N 'user@server.example'") || !strings.Contains(stdout.String(), "https://issuer.example/authorize") || strings.Contains(stdout.String(), testToken) {
		t.Fatalf("authorize command did not print the safe sign-in URL: path=%q output=%q", gotPath, stdout.String())
	}
}

func TestPublicMCPConnectCreatesDisabledCredentialsOnlyConfig(t *testing.T) {
	dataDir := tokenDir(t)
	var paths []string
	var configured map[string]any
	server := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "GET /api/mcp/servers":
			_, _ = io.WriteString(w, `[]`)
		case "POST /api/mcp/servers":
			if err := json.NewDecoder(r.Body).Decode(&configured); err != nil {
				t.Errorf("decode config: %v", err)
			}
			_, _ = io.WriteString(w, `{"id":"svc1","name":"Notion","transport":"streamable_http","endpoint":"https://mcp.notion.com/mcp","enabled":false,"allowed_tools":[],"oauth_authorized":false}`)
		case "POST /api/mcp/servers/svc1/oauth/start":
			_, _ = io.WriteString(w, `{"authorization_url":"https://issuer.example/authorize?state=one-use","callback_port":42001}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	var stdout bytes.Buffer
	err := Run([]string{"connect", "notion", "--ssh-target", "user@server.example", "--url", server.URL, "--data-dir", dataDir}, strings.NewReader(""), &stdout, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{"GET /api/mcp/servers", "POST /api/mcp/servers", "POST /api/mcp/servers/svc1/oauth/start"}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("unexpected public service flow: %v", paths)
	}
	if configured["enabled"] != false || len(configured["allowed_tools"].([]any)) != 0 || len(configured["read_tools"].([]any)) != 0 || len(configured["write_tools"].([]any)) != 0 {
		t.Fatalf("service alias created tools or runtime grants: %#v", configured)
	}
	if !strings.Contains(stdout.String(), "credentials only") || !strings.Contains(stdout.String(), "ssh -L 127.0.0.1:42001:127.0.0.1:42001") || !strings.Contains(stdout.String(), "https://issuer.example/authorize") {
		t.Fatalf("service alias omitted no-grant or SSH details: %s", stdout.String())
	}
}

func TestGooglePublicMCPConnectExplainsDeferredClientSetup(t *testing.T) {
	err := Run([]string{"connect", "calendar"}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "TODO.md") || !strings.Contains(err.Error(), "Google Cloud OAuth client") {
		t.Fatalf("Google setup was not deferred actionably: %v", err)
	}
}

func TestSearchConfigureAndShowReportExternalRequestBoundary(t *testing.T) {
	dataDir := tokenDir(t)
	var configuredEndpoint string
	server := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /api/search":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode search config: %v", err)
			}
			configuredEndpoint = body["endpoint"]
			_, _ = io.WriteString(w, `{"provider":"searxng","endpoint":"https://search.example/","configured":true,"requests_leave_device":true}`)
		case "GET /api/search":
			_, _ = io.WriteString(w, `{"provider":"searxng","endpoint":"https://search.example/","configured":true,"requests_leave_device":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	var out strings.Builder
	if err := Run([]string{"inspect", "search", "configure", "--endpoint", "https://search.example/", "--url", server.URL, "--data-dir", dataDir}, strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatalf("search configure failed: %v", err)
	}
	if configuredEndpoint != "https://search.example/" || !strings.Contains(out.String(), "requests leave this device") {
		t.Fatalf("configure should save endpoint and disclose network boundary: body endpoint=%q output=%s", configuredEndpoint, out.String())
	}
	out.Reset()
	if err := Run([]string{"inspect", "search", "show", "--url", server.URL, "--data-dir", dataDir}, strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatalf("search show failed: %v", err)
	}
	if !strings.Contains(out.String(), `"requests_leave_device": true`) {
		t.Fatalf("show omitted external request disclosure: %s", out.String())
	}
}

func TestSearchConfigureSurfacesRejectedEndpoint(t *testing.T) {
	dataDir := tokenDir(t)
	server := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"search endpoint must use HTTPS or loopback HTTP without credentials"}`)
	}))
	defer server.Close()
	var out strings.Builder
	err := Run([]string{"inspect", "search", "configure", "--endpoint", "http://remote.example/", "--url", server.URL, "--data-dir", dataDir}, strings.NewReader(""), &out, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "HTTPS or loopback HTTP") || out.Len() != 0 {
		t.Fatalf("invalid search endpoint should fail without a success claim: err=%v out=%s", err, out.String())
	}
}

func TestMissingTokenDoesNotMakeAPIRequest(t *testing.T) {
	var calls int
	server := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer server.Close()
	var stderr strings.Builder
	err := Run([]string{"inspect", "models", "list", "--url", server.URL, "--data-dir", t.TempDir()}, strings.NewReader(""), io.Discard, &stderr)
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
	server := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer server.Close()
	var stderr strings.Builder
	err := Run([]string{"inspect", "models", "list", "--help"}, strings.NewReader(""), io.Discard, &stderr)
	if err != nil || calls != 0 || !strings.Contains(stderr.String(), "loopback URL") {
		t.Fatalf("help should print flags without requesting API: err=%v calls=%d stderr=%s", err, calls, stderr.String())
	}
}

func TestRootCommandsAndBareTaskHarness(t *testing.T) {
	var harness strings.Builder
	if err := Run(nil, strings.NewReader(""), &harness, io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.Count(harness.String(), "OwnBot") != 1 || !strings.Contains(harness.String(), "Task harness") {
		t.Fatalf("bare invocation should show one wordmark and the local task harness: %s", harness.String())
	}
	var help strings.Builder
	if err := Run([]string{"help"}, strings.NewReader(""), &help, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"setup", "connect", "daemon start", "inspect", "help"} {
		if !strings.Contains(help.String(), command) {
			t.Fatalf("root help missing %q: %s", command, help.String())
		}
	}
	for _, hidden := range []string{"  model ", "  agent ", "  runs ", "  start daemon", "ownbotd"} {
		if strings.Contains(help.String(), hidden) {
			t.Fatalf("non-root command %q leaked into root help", hidden)
		}
	}
}

func TestSetupOffersOllamaWithoutGuessingOrInstalling(t *testing.T) {
	var out strings.Builder
	err := offerOllama(bufio.NewReader(strings.NewReader("")), &out, true)
	if err == nil || !strings.Contains(err.Error(), "ollama pull <model-id>") || !strings.Contains(err.Error(), "no model was selected or downloaded") {
		t.Fatalf("--yes should fail with precise user-run next step: %v", err)
	}
	if !strings.Contains(err.Error(), "https://ollama.com/download") {
		t.Fatalf("missing official Ollama download path: %v", err)
	}
}

func TestDetectLocalModelsListsInstalledModelsForEverySupportedRuntime(t *testing.T) {
	server := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ollama/api/tags":
			_, _ = io.WriteString(w, `{"models":[{"name":"local-q"}]}`)
		case "/lm/v1/models", "/llama/v1/models", "/vllm/v1/models":
			_, _ = io.WriteString(w, `{"data":[{"id":"installed-model"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := server.Client()
	base := strings.TrimRight(server.URL, "/")
	got := detectLocalModelsAt(context.Background(), client, []localEndpoint{
		{Runtime: "Ollama", Provider: "ollama", BaseURL: base + "/ollama/v1", Ollama: true},
		{Runtime: "LM Studio", Provider: "lm_studio", BaseURL: base + "/lm/v1"},
		{Runtime: "llama.cpp", Provider: "openai_compatible", BaseURL: base + "/llama/v1"},
		{Runtime: "vLLM", Provider: "openai_compatible", BaseURL: base + "/vllm/v1"},
	})
	if len(got) != 4 {
		t.Fatalf("detected models=%+v", got)
	}
	wantProviders := []string{"ollama", "lm_studio", "openai_compatible", "openai_compatible"}
	for i, provider := range wantProviders {
		if got[i].Provider != provider {
			t.Fatalf("model %d provider=%s", i, got[i].Provider)
		}
	}
}

func TestSetupMultipleModelsUsesFirstOnEnterAndWithYes(t *testing.T) {
	var created []string
	var probes, agentsCreated int
	api := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"data":[{"id":"first-model"},{"id":"second-model"}]}`)
		case "/api/models":
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+testToken {
				t.Errorf("model request not authenticated: method=%s", r.Method)
			}
			var body struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode model request: %v", err)
			}
			created = append(created, body.Model)
			_ = json.NewEncoder(w).Encode(modelConfig{ID: "model-1", Name: body.Model, Model: body.Model})
		case "/api/models/model-1/probe":
			probes++
			_, _ = io.WriteString(w, `{"probe":{"score":100,"json_output":true,"single_tool":true,"tool_choice":true,"argument_accuracy":true,"restricted":false}}`)
		case "/api/agents":
			agentsCreated++
			_, _ = io.WriteString(w, `{"id":"agent-1","name":"Assistant","model":"first-model"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	for _, scenario := range []struct {
		name  string
		args  []string
		input string
	}{
		{name: "enter selects first", input: "\n"},
		{name: "yes selects first", args: []string{"--yes"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			dataDir := tokenDir(t)
			args := append([]string{"setup", "--base-url", api.URL + "/v1", "--url", api.URL, "--data-dir", dataDir}, scenario.args...)
			var output strings.Builder
			if err := Run(args, strings.NewReader(scenario.input), &output, io.Discard); err != nil {
				t.Fatalf("setup failed: %v; output=%s", err, output.String())
			}
			if !strings.Contains(output.String(), "Next: ownbot") {
				t.Fatalf("setup missing next step: %s", output.String())
			}
		})
	}
	if !reflect.DeepEqual(created, []string{"first-model", "first-model"}) {
		t.Fatalf("setup model choices=%v", created)
	}
	if probes != 2 || agentsCreated != 2 {
		t.Fatalf("setup probes=%d agents=%d; expected each model selection to be probed before agent creation", probes, agentsCreated)
	}
}

func TestSetupDoesNotCreateAgentWhenAnyProbeCapabilityFails(t *testing.T) {
	dataDir := tokenDir(t)
	configPath := filepath.Join(t.TempDir(), "setup.toml")
	var agentsCreated bool
	api := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"data":[{"id":"local-model"}]}`)
		case "/api/status":
			_, _ = io.WriteString(w, `{"ok":true,"service":"ownbot"}`)
		case "/api/models":
			_, _ = io.WriteString(w, `{"id":"m1","name":"local","model":"local-model"}`)
		case "/api/models/m1/probe":
			_, _ = io.WriteString(w, `{"probe":{"score":75,"json_output":true,"single_tool":true,"tool_choice":true,"argument_accuracy":false,"restricted":true}}`)
		case "/api/agents":
			agentsCreated = true
			http.Error(w, "unexpected agent creation", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	config := "[model]\nprovider = \"openai_compatible\"\nbase_url = \"" + api.URL + "/v1\"\nmodel = \"local-model\"\n[agent]\nname = \"Assistant\"\n"
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	err := Run([]string{"setup", "--yes", "--config", configPath, "--url", api.URL, "--data-dir", dataDir}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "failed one or more required capability checks") || agentsCreated {
		t.Fatalf("restricted model setup: err=%v created=%t", err, agentsCreated)
	}
}

func TestInspectCollectsDaemonModelAgentAndPermissionState(t *testing.T) {
	dataDir := tokenDir(t)
	var calls []string
	api := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		switch r.URL.Path {
		case "/api/status":
			_, _ = io.WriteString(w, `{"ok":true,"version":"development"}`)
		case "/api/models":
			_, _ = io.WriteString(w, `[{"id":"m1","last_probe":{"score":100,"restricted":false}}]`)
		case "/api/agents":
			_, _ = io.WriteString(w, `[{"id":"a1","enabled":true,"tools":[],"web_search":false,"mcp_enabled":false}]`)
		case "/api/tools", "/api/mcp/servers":
			_, _ = io.WriteString(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	var output strings.Builder
	if err := Run([]string{"inspect", "--url", api.URL, "--data-dir", dataDir}, strings.NewReader(""), &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"daemon", "models", "last_probe", "agents", "enabled", "tool_permissions", "mcp_integrations"} {
		if !strings.Contains(output.String(), `"`+field+`"`) {
			t.Fatalf("inspection missing %q: %s", field, output.String())
		}
	}
	if len(calls) != 4 {
		t.Fatalf("inspection API calls=%v", calls)
	}
}

func TestAuthenticatedCLIClientNeverUsesConfiguredHTTPProxy(t *testing.T) {
	dataDir := tokenDir(t)
	var localCalls, proxyCalls int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localCalls++
		_, _ = io.WriteString(w, `{"ok":true,"service":"ownbot"}`)
	}))
	defer api.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalls++
		http.Error(w, "unexpected proxy", http.StatusBadGateway)
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	client, err := NewClient(api.URL, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := client.Do(context.Background(), http.MethodGet, "/api/status", nil, &result); err != nil {
		t.Fatal(err)
	}
	if localCalls != 2 || proxyCalls != 0 {
		t.Fatalf("local calls=%d proxy calls=%d", localCalls, proxyCalls)
	}
}

func TestClientDoesNotSendOwnerTokenToWrongLocalService(t *testing.T) {
	dataDir := tokenDir(t)
	var authorization string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer api.Close()
	client, err := NewClient(api.URL, dataDir)
	if err == nil || client != nil || !strings.Contains(err.Error(), "does not identify as an OwnBot daemon") {
		t.Fatalf("wrong local service was accepted: client=%v err=%v", client, err)
	}
	if authorization != "" {
		t.Fatalf("sent credentials before service verification: %q", authorization)
	}
}

func TestDaemonStartAddressAndLoopbackStatusDetection(t *testing.T) {
	if got, err := dashboardAPIURL("127.0.0.1:8765", ""); err != nil || got != "http://127.0.0.1:8765" {
		t.Fatalf("dashboard URL=%q err=%v", got, err)
	}
	if _, err := dashboardAPIURL("0.0.0.0:8765", ""); err == nil {
		t.Fatal("accepted wildcard listener for dashboard")
	}
	if _, err := dashboardAPIURL("127.0.0.1:8765", "/tmp/ownbot.sock"); err == nil {
		t.Fatal("claimed browser dashboard support for Unix socket")
	}
	api := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true,"service":"ownbot"}`)
	}))
	defer api.Close()
	if !daemonHealthy(api.URL) {
		t.Fatal("healthy loopback daemon was not detected")
	}
	remote := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true,"service":"ownbot"}`)
	}))
	defer remote.Close()
	if daemonHealthy(strings.Replace(remote.URL, "127.0.0.1", "example.com", 1)) {
		t.Fatal("accepted non-loopback endpoint")
	}
}

func TestTaskHarnessReportsApprovalThroughPersistedRunAPI(t *testing.T) {
	dataDir := tokenDir(t)
	t.Setenv("OWNBOT_DATA_DIR", dataDir)
	api := ownBotTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/agents/a1/runs" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"id":"r1","agent_id":"a1","status":"awaiting_approval","prompt_version":1}`)
	}))
	defer api.Close()
	t.Setenv("OWNBOT_API_URL", api.URL)
	var out strings.Builder
	if err := runTask("a1", "create an event", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "awaiting_approval") || !strings.Contains(out.String(), "Review it in the local dashboard") {
		t.Fatalf("approval status was not surfaced: %s", out.String())
	}
}
