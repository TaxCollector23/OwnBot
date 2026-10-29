package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMCPDiscoveryAndExplicitPermissionsHaveSeparateGates(t *testing.T) {
	var toolCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("invalid MCP request")
			return
		}
		if len(request.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "fixture", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{
				map[string]any{"name": "lookup", "description": "Read profile", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{"readOnlyHint": false}},
				map[string]any{"name": "mutate", "description": "<script>untrusted</script>", "inputSchema": map[string]any{"type": "object"}},
			}}
		case "tools/call":
			toolCalls.Add(1)
			result = map[string]any{}
		default:
			t.Errorf("unexpected MCP method %q", request.Method)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	defer provider.Close()

	s, token := testApp(t)
	h := s.Handler()
	create := `{"name":"Fixture","transport":"streamable_http","endpoint":"` + provider.URL + `","allowed_tools":[],"read_tools":[],"write_tools":[]}`
	w := request(t, h, http.MethodPost, "/api/mcp/servers", create, "127.0.0.1:8765", "http://127.0.0.1:8765", token)
	if w.Code != http.StatusCreated {
		t.Fatalf("create config: %d %s", w.Code, w.Body.String())
	}
	var config MCPServerConfig
	if err := json.Unmarshal(w.Body.Bytes(), &config); err != nil {
		t.Fatal(err)
	}
	path := "/api/mcp/servers/" + config.ID
	if w = request(t, h, http.MethodPost, path+"/tools/discover", `{}`, "127.0.0.1:8765", "http://127.0.0.1:8765", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated discovery: %d", w.Code)
	}
	w = request(t, h, http.MethodPost, path+"/tools/discover", `{}`, "127.0.0.1:8765", "http://127.0.0.1:8765", token)
	var discovered struct {
		Tools []mcpToolMetadata `json:"tools"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &discovered) != nil || len(discovered.Tools) != 2 {
		t.Fatalf("discover metadata: %d %s", w.Code, w.Body.String())
	}
	if discovered.Tools[1].Description != "<script>untrusted</script>" || strings.Contains(w.Body.String(), "readOnlyHint") {
		t.Fatalf("metadata should be bounded plain text with no trusted annotations: %s", w.Body.String())
	}
	if toolCalls.Load() != 0 {
		t.Fatalf("metadata discovery made %d tool calls", toolCalls.Load())
	}
	for _, tc := range []struct{ body string }{
		{`{"read_tools":["lookup"],"write_tools":["lookup"]}`},
		{`{"read_tools":["unknown"],"write_tools":[]}`},
		{`{"read_tools":["lookup","lookup"],"write_tools":[]}`},
	} {
		w = request(t, h, http.MethodPatch, path+"/permissions", tc.body, "127.0.0.1:8765", "http://127.0.0.1:8765", token)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid permission update should fail: %d %s", w.Code, w.Body.String())
		}
	}
	w = request(t, h, http.MethodPatch, path+"/permissions", `{"read_tools":["lookup"],"write_tools":["mutate"]}`, "127.0.0.1:8765", "http://127.0.0.1:8765", token)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"agent_grants_changed":false`) {
		t.Fatalf("save permissions: %d %s", w.Code, w.Body.String())
	}
	saved, err := s.loadMCP(config.ID)
	if err != nil || len(saved.AllowedTools) != 2 || len(saved.ReadTools) != 1 || len(saved.WriteTools) != 1 {
		t.Fatalf("permission config not saved: %+v err=%v", saved, err)
	}
	var agentGrants int
	if err = s.db.QueryRow(`SELECT count(*) FROM agent_settings`).Scan(&agentGrants); err == nil && agentGrants != 0 {
		t.Fatalf("permissions created agent grants: %d", agentGrants)
	}
	if toolCalls.Load() != 0 {
		t.Fatalf("metadata/permission updates made %d tool calls", toolCalls.Load())
	}
}
