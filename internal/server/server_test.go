package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"ownbot/internal/models"
	"path/filepath"
	"strings"
	"testing"
)

func testApp(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	s, e := New(dir)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	b, e := os.ReadFile(filepath.Join(dir, "api.token"))
	if e != nil {
		t.Fatal(e)
	}
	return s, strings.TrimSpace(string(b))
}
func request(t *testing.T, h http.Handler, method, path, body, host, origin, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if host != "" {
		r.Host = host
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestSensitiveReadRequiresTokenAndStatusIsLoopbackOnly(t *testing.T) {
	s, token := testApp(t)
	if w := request(t, s.Handler(), "GET", "/api/agents", "", "127.0.0.1:8765", "", ""); w.Code != 401 {
		t.Fatalf("missing token: got %d", w.Code)
	}
	if w := request(t, s.Handler(), "GET", "/api/status", "", "evil.example", "", ""); w.Code != 403 {
		t.Fatalf("hostile host: got %d", w.Code)
	}
	if w := request(t, s.Handler(), "GET", "/api/agents", "", "127.0.0.1:8765", "", token); w.Code != 200 {
		t.Fatalf("authenticated read: got %d", w.Code)
	}
}
func TestMutationsRejectHostAndOrigin(t *testing.T) {
	s, token := testApp(t)
	for _, tc := range []struct{ name, host, origin string }{{"host", "evil.example", ""}, {"origin", "127.0.0.1:8765", "http://evil.example"}} {
		t.Run(tc.name, func(t *testing.T) {
			w := request(t, s.Handler(), "POST", "/api/agents", `{"name":"x"}`, tc.host, tc.origin, token)
			if w.Code != 403 {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}
func TestApprovalDecisionIsSingleUseAndAudited(t *testing.T) {
	s, token := testApp(t)
	h := s.Handler()
	w := request(t, h, "POST", "/api/approvals", `{"tool":"calendar.create","args":{"title":"x"}}`, "127.0.0.1:8765", "http://127.0.0.1:8765", token)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var a Approval
	if e := json.Unmarshal(w.Body.Bytes(), &a); e != nil {
		t.Fatal(e)
	}
	w = request(t, h, "POST", "/api/approvals/"+a.ID+"/approve", "{}", "127.0.0.1:8765", "http://127.0.0.1:8765", token)
	if w.Code != 200 {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	w = request(t, h, "POST", "/api/approvals/"+a.ID+"/deny", "{}", "127.0.0.1:8765", "http://127.0.0.1:8765", token)
	if w.Code != 409 {
		t.Fatalf("second decision: %d", w.Code)
	}
	var count int
	if e := s.db.QueryRow(`SELECT count(*) FROM audit WHERE event_type='approval.approved'`).Scan(&count); e != nil || count != 1 {
		t.Fatalf("audit count %d err %v", count, e)
	}
}
func TestAgentPersistence(t *testing.T) {
	dir := t.TempDir()
	s, e := New(dir)
	if e != nil {
		t.Fatal(e)
	}
	token := s.token
	w := request(t, s.Handler(), "POST", "/api/agents", `{"name":"Persisted","enabled":true,"prompt":"hello"}`, "127.0.0.1:8765", "http://127.0.0.1:8765", token)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = New(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	w = request(t, s.Handler(), "GET", "/api/agents", "", "127.0.0.1:8765", "", token)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Persisted") {
		t.Fatalf("persistence: %d %s", w.Code, w.Body.String())
	}
}

func TestDashboardRouteServesDashboard(t *testing.T) {
	s, _ := testApp(t)
	w := request(t, s.Handler(), "GET", "/dashboard", "", "127.0.0.1:8765", "", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Local dashboard") {
		t.Fatalf("dashboard route: %d %s", w.Code, w.Body.String())
	}
}

func TestModelConfigRejectsSecretsAndNonLocalHTTP(t *testing.T) {
	s, token := testApp(t)
	h := s.Handler()
	w := request(t, h, "POST", "/api/models", `{"provider":"openai","model":"gpt-test","api_key":"should-not-be-accepted"}`, "127.0.0.1:8765", "http://127.0.0.1:8765", token)
	if w.Code != 400 {
		t.Fatalf("secret config field accepted: %d %s", w.Code, w.Body.String())
	}
	w = request(t, h, "POST", "/api/models", `{"provider":"openai_compatible","model":"m","base_url":"http://example.com/v1"}`, "127.0.0.1:8765", "http://127.0.0.1:8765", token)
	if w.Code != 400 {
		t.Fatalf("non-local HTTP endpoint accepted: %d %s", w.Code, w.Body.String())
	}
	w = request(t, h, "POST", "/api/models", `{"provider":"ollama","model":"local"}`, "127.0.0.1:8765", "http://127.0.0.1:8765", token)
	if w.Code != 201 {
		t.Fatalf("local config: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "api_key") {
		t.Fatal("configuration response exposed a credential field")
	}
}

func TestModelProbeUsesEphemeralKeyAndPersistsCapability(t *testing.T) {
	s, token := testApp(t)
	var calls int
	var leaked bool
	modelMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer ephemeral-secret" {
			leaked = true
		}
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if e := json.NewDecoder(r.Body).Decode(&req); e != nil {
			t.Errorf("decode probe request: %v", e)
			return
		}
		msg := map[string]any{"role": "assistant", "content": ""}
		prompt := ""
		if len(req.Messages) > 0 {
			prompt = req.Messages[0].Content
		}
		switch {
		case strings.Contains(prompt, "Return only JSON"):
			msg["content"] = `{"ok":true}`
		case strings.Contains(prompt, "probe_one"):
			msg["tool_calls"] = []any{map[string]any{"id": "c1", "type": "function", "function": map[string]any{"name": "probe_one", "arguments": `{"value":"alpha"}`}}}
		default:
			msg["tool_calls"] = []any{map[string]any{"id": "c2", "type": "function", "function": map[string]any{"name": "probe_two", "arguments": `{"value":"beta"}`}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "mock", "model": "mock", "choices": []any{map[string]any{"message": msg}}})
	}))
	defer modelMock.Close()
	h := s.Handler()
	body, _ := json.Marshal(map[string]any{"provider": "openai_compatible", "name": "Mock local", "base_url": modelMock.URL + "/v1", "model": "mock-model"})
	w := request(t, h, "POST", "/api/models", string(body), "127.0.0.1:8765", "http://127.0.0.1:8765", token)
	if w.Code != 201 {
		t.Fatalf("configure: %d %s", w.Code, w.Body.String())
	}
	var cfg ModelConfig
	if e := json.Unmarshal(w.Body.Bytes(), &cfg); e != nil {
		t.Fatal(e)
	}
	probeBody := `{"api_key":"ephemeral-secret"}`
	w = request(t, h, "POST", "/api/models/"+cfg.ID+"/probe", probeBody, "127.0.0.1:8765", "http://127.0.0.1:8765", token)
	if w.Code != 200 {
		t.Fatalf("probe: %d %s", w.Code, w.Body.String())
	}
	if calls != 3 || leaked {
		t.Fatalf("probe calls=%d credential/header failure=%t", calls, leaked)
	}
	if strings.Contains(w.Body.String(), "ephemeral-secret") {
		t.Fatal("probe response exposed ephemeral key")
	}
	var result struct {
		Probe models.ProbeResult `json:"probe"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	if result.Probe.Score != 100 || result.Probe.Restricted {
		t.Fatalf("unexpected probe result: %+v", result.Probe)
	}
	w = request(t, h, "GET", "/api/models", "", "127.0.0.1:8765", "", token)
	if w.Code != 200 || strings.Contains(w.Body.String(), "ephemeral-secret") {
		t.Fatalf("model listing leaked key or failed: %d %s", w.Code, w.Body.String())
	}
}

func TestModelProbeFailsClosedForUnavailableProvider(t *testing.T) {
	s, token := testApp(t)
	h := s.Handler()
	w := request(t, h, "POST", "/api/models", `{"provider":"openai_compatible","model":"m","base_url":"http://127.0.0.1:1/v1"}`, "127.0.0.1:8765", "http://127.0.0.1:8765", token)
	if w.Code != 201 {
		t.Fatalf("configure: %d %s", w.Code, w.Body.String())
	}
	var c ModelConfig
	if e := json.Unmarshal(w.Body.Bytes(), &c); e != nil {
		t.Fatal(e)
	}
	w = request(t, h, "POST", "/api/models/"+c.ID+"/probe", `{}`, "127.0.0.1:8765", "http://127.0.0.1:8765", token)
	if w.Code != 502 {
		t.Fatalf("unavailable endpoint: %d %s", w.Code, w.Body.String())
	}
	var n int
	if e := s.db.QueryRow(`SELECT count(*) FROM model_probes WHERE model_config_id=?`, c.ID).Scan(&n); e != nil || n != 0 {
		t.Fatalf("failed probe persisted: count=%d err=%v", n, e)
	}
}

func TestMCPConfigurationIsDisabledMetadataOnlyAndRevocable(t *testing.T) {
	s, token := testApp(t)
	h := s.Handler()
	body := `{"name":"Calendar MCP","command":"/path/that/does/not/exist","args":["--stdio"],"environment_allowlist":[],"allowed_tools":["events.list","events.create"],"read_tools":["events.list"],"write_tools":["events.create"]}`
	w := request(t, h, "POST", "/api/mcp/servers", body, "127.0.0.1:8765", "http://127.0.0.1:8765", token)
	if w.Code != 201 {
		t.Fatalf("configure: %d %s", w.Code, w.Body.String())
	}
	var c MCPServerConfig
	if e := json.Unmarshal(w.Body.Bytes(), &c); e != nil {
		t.Fatal(e)
	}
	if c.Enabled || c.RuntimeAvailable {
		t.Fatalf("MCP config must stay disabled and disconnected: %+v", c)
	}
	w = request(t, h, "GET", "/api/mcp/servers", "", "127.0.0.1:8765", "", token)
	if w.Code != 200 || strings.Contains(w.Body.String(), "enabled\\\":true") {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	// No process is started by creating a record; the nonexistent command remains inert metadata.
	w = request(t, h, "POST", "/api/mcp/servers", `{"name":"Bad","command":"echo","enabled":true}`, "127.0.0.1:8765", "http://127.0.0.1:8765", token)
	if w.Code != 409 {
		t.Fatalf("enabled config accepted: %d %s", w.Code, w.Body.String())
	}
	w = request(t, h, "POST", "/api/mcp/servers", `{"name":"Bad","command":"echo","environment_allowlist":["API_TOKEN"]}`, "127.0.0.1:8765", "http://127.0.0.1:8765", token)
	if w.Code != 400 {
		t.Fatalf("secret env allowlist accepted: %d", w.Code)
	}
	w = request(t, h, "POST", "/api/mcp/servers", `{"name":"Bad","command":"echo","args":["--api-key=secret"]}`, "127.0.0.1:8765", "http://127.0.0.1:8765", token)
	if w.Code != 400 {
		t.Fatalf("credential-like arg accepted: %d", w.Code)
	}
	w = request(t, h, "DELETE", "/api/mcp/servers/"+c.ID, "", "127.0.0.1:8765", "http://127.0.0.1:8765", token)
	if w.Code != 204 {
		t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
	}
	w = request(t, h, "GET", "/api/mcp/servers", "", "127.0.0.1:8765", "", token)
	if w.Code != 200 || w.Body.String() != "[]\n" {
		t.Fatalf("list after revoke: %d %s", w.Code, w.Body.String())
	}
}

func TestModelProbeRestrictsWhenAnyCapabilityFails(t *testing.T) {
	s, token := testApp(t)
	var call int
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		prompt := ""
		if len(req.Messages) > 0 {
			prompt = req.Messages[0].Content
		}
		msg := map[string]any{"role": "assistant", "content": ""}
		if strings.Contains(prompt, "Return only JSON") {
			msg["content"] = `{"ok":false}`
		} else if strings.Contains(prompt, "probe_one") {
			msg["tool_calls"] = []any{map[string]any{"id": "c1", "type": "function", "function": map[string]any{"name": "probe_one", "arguments": `{"value":"alpha"}`}}}
		} else {
			msg["tool_calls"] = []any{map[string]any{"id": "c2", "type": "function", "function": map[string]any{"name": "probe_two", "arguments": `{"value":"beta"}`}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": msg}}})
	}))
	defer mock.Close()
	h := s.Handler()
	payload, _ := json.Marshal(map[string]any{"provider": "openai_compatible", "base_url": mock.URL + "/v1", "model": "m"})
	w := request(t, h, "POST", "/api/models", string(payload), "127.0.0.1:8765", "http://127.0.0.1:8765", token)
	if w.Code != 201 {
		t.Fatalf("configure: %d %s", w.Code, w.Body.String())
	}
	var c ModelConfig
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	w = request(t, h, "POST", "/api/models/"+c.ID+"/probe", "{}", "127.0.0.1:8765", "http://127.0.0.1:8765", token)
	if w.Code != 200 {
		t.Fatalf("probe: %d %s", w.Code, w.Body.String())
	}
	var result struct {
		Probe models.ProbeResult `json:"probe"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if call != 3 || result.Probe.Score != 75 || !result.Probe.Restricted {
		t.Fatalf("one failed capability must restrict: calls=%d result=%+v", call, result.Probe)
	}
}

func TestStreamableHTTPMCPIsHTTPSMetadataOnly(t *testing.T) {
	s, token := testApp(t)
	h := s.Handler()
	w := request(t, h, "POST", "/api/mcp/servers", `{"name":"Hosted Calendar MCP","transport":"streamable_http","endpoint":"https://calendar.example.invalid/mcp","allowed_tools":["list","create"],"read_tools":["list"],"write_tools":["create"]}`, "127.0.0.1:8765", "http://127.0.0.1:8765", token)
	if w.Code != 201 {
		t.Fatalf("configure hosted endpoint: %d %s", w.Code, w.Body.String())
	}
	var c MCPServerConfig
	if e := json.Unmarshal(w.Body.Bytes(), &c); e != nil {
		t.Fatal(e)
	}
	if c.Enabled || c.RuntimeAvailable || c.Transport != "streamable_http" || c.Endpoint != "https://calendar.example.invalid/mcp" {
		t.Fatalf("unexpected inert config: %+v", c)
	}
	for _, bad := range []string{`{"name":"bad","transport":"streamable_http","endpoint":"https://user:pass@example.com/mcp"}`, `{"name":"bad","transport":"streamable_http","endpoint":"https://example.com/mcp?token=secret"}`, `{"name":"bad","transport":"streamable_http","endpoint":"http://example.com/mcp"}`} {
		w = request(t, h, "POST", "/api/mcp/servers", bad, "127.0.0.1:8765", "http://127.0.0.1:8765", token)
		if w.Code != 400 {
			t.Fatalf("unsafe endpoint accepted (%s): %d %s", bad, w.Code, w.Body.String())
		}
	}
}

func TestMCPTransportMigrationAddsColumnsToExistingDatabase(t *testing.T) {
	dir := t.TempDir()
	db, e := sql.Open("sqlite", filepath.Join(dir, "ownbot.db"))
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(`CREATE TABLE mcp_servers(id TEXT PRIMARY KEY,name TEXT NOT NULL,enabled INTEGER NOT NULL DEFAULT 0,command TEXT NOT NULL,args_json TEXT NOT NULL DEFAULT '[]',working_directory TEXT NOT NULL DEFAULT '',environment_allowlist_json TEXT NOT NULL DEFAULT '[]',allowed_tools_json TEXT NOT NULL DEFAULT '[]',read_tools_json TEXT NOT NULL DEFAULT '[]',write_tools_json TEXT NOT NULL DEFAULT '[]',request_timeout_seconds INTEGER NOT NULL DEFAULT 30,created_at TEXT NOT NULL,updated_at TEXT NOT NULL)`)
	if e != nil {
		t.Fatal(e)
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	s, e := New(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	var transport, endpoint string
	if e = s.db.QueryRow(`SELECT transport,endpoint FROM mcp_servers LIMIT 1`).Scan(&transport, &endpoint); e != sql.ErrNoRows {
		t.Fatalf("expected empty migrated table; got transport=%q endpoint=%q err=%v", transport, endpoint, e)
	}
}
