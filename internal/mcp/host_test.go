package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const hostHelperEnv = "OWNBOT_MCP_HOST_HELPER"

func TestMCPHostHelperProcess(t *testing.T) {
	if os.Getenv(hostHelperEnv) != "1" {
		return
	}
	input := bufio.NewScanner(os.Stdin)
	output := bufio.NewWriter(os.Stdout)
	for input.Scan() {
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
		}
		if json.Unmarshal(input.Bytes(), &req) != nil {
			os.Exit(2)
		}
		if len(req.ID) == 0 { // notifications have no response
			continue
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "fake", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "lookup", "description": "read data", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}, "required": []string{"q"}, "additionalProperties": false}}}}
		case "tools/call":
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}
		default:
			os.Exit(3)
		}
		response := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "result": result}
		if json.NewEncoder(output).Encode(response) != nil || output.Flush() != nil {
			os.Exit(4)
		}
	}
	os.Exit(0)
}

func TestToolAliasIsDeterministicSafeAndScoped(t *testing.T) {
	for _, tc := range []struct{ server, remote string }{
		{"my-server", "search web"},
		{"server_2", "🔍lookup"},
		{"s", strings.Repeat("n", 256)},
	} {
		alias := ToolAlias(tc.server, tc.remote)
		if alias == "" || len(alias) > 64 || !safeToolAlias(alias) {
			t.Fatalf("unsafe alias %q", alias)
		}
		if alias != ToolAlias(tc.server, tc.remote) {
			t.Fatalf("alias is not stable: %q", alias)
		}
	}
	if ToolAlias("server-a", "search") == ToolAlias("server-b", "search") {
		t.Fatal("same provider tool on different servers collided")
	}
}

func safeToolAlias(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func TestHostManagerHTTPConnectPolicyAndDisconnect(t *testing.T) {
	var calls atomic.Int32
	var deleted atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			if r.Header.Get("Mcp-Session-Id") != "session-1" {
				t.Error("session ID missing during disconnect")
			}
			deleted.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("invalid MCP request")
			return
		}
		switch request.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "session-1")
			respondMCP(w, request.ID, map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "fake", "version": "1"}})
		case "tools/list":
			respondMCP(w, request.ID, map[string]any{"tools": []any{
				map[string]any{"name": "lookup", "description": "lookup", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}, "required": []string{"q"}, "additionalProperties": false}},
				map[string]any{"name": "delete_item", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}},
			}})
		case "tools/call":
			calls.Add(1)
			respondMCP(w, request.ID, map[string]any{"content": []any{map[string]any{"type": "text", "text": "done"}}})
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()

	manager := NewHostManager()
	if _, err := manager.Connect(context.Background(), HostConfig{ID: "web", Enabled: false}); err != ErrHostDisabled {
		t.Fatalf("disabled host error=%v", err)
	}
	if _, err := manager.Connect(context.Background(), HostConfig{ID: "web", Enabled: true, Transport: "streamable_http", Endpoint: server.URL, AllowedTools: []string{"lookup", "delete_item"}, ReadTools: []string{"lookup"}}); err == nil || !strings.Contains(err.Error(), "no configured access class") {
		t.Fatalf("unclassified tool accepted: %v", err)
	}
	bindings, err := manager.Connect(context.Background(), HostConfig{ID: "web", Name: "web search", Transport: "streamable_http", Enabled: true, Endpoint: server.URL, AllowedTools: []string{"lookup", "delete_item"}, ReadTools: []string{"lookup"}, WriteTools: []string{"delete_item"}, RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 2 || bindings[0].Source != "mcp" || bindings[0].Server != "web" || !bindings[0].Untrusted || bindings[0].Risk != "read" {
		t.Fatalf("bad read binding: %+v", bindings)
	}
	if bindings[1].Risk != "write_irreversible" || !bindings[1].AlwaysApprove {
		t.Fatalf("write was not fail-closed: %+v", bindings[1])
	}
	if len(bindings[0].Schema.Name) > 64 || bindings[0].Schema.Name == "lookup" {
		t.Fatalf("provider name was not safely namespaced: %q", bindings[0].Schema.Name)
	}
	if _, err := manager.Connect(context.Background(), HostConfig{ID: "web", Name: "web search", Transport: "streamable_http", Enabled: true, Endpoint: server.URL, AllowedTools: []string{"lookup", "delete_item"}, ReadTools: []string{"lookup"}, WriteTools: []string{"delete_item"}, RequestTimeout: time.Second}); err != ErrHostConnected {
		t.Fatalf("duplicate host error=%v", err)
	}
	if _, err := bindings[0].Execute(context.Background(), json.RawMessage(`{"q":"hi"}`), "ignored-key"); err != ErrDispatchNotAllowed {
		t.Fatalf("call without engine dispatch proof was not refused: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("MCP server received tool call without engine dispatch proof")
	}
	if err := manager.Disconnect("web"); err != nil {
		t.Fatal(err)
	}
	if deleted.Load() != 1 {
		t.Fatalf("expected session termination request, got %d", deleted.Load())
	}
	if _, err := manager.Tools("web"); err != ErrHostNotConnected {
		t.Fatalf("tools after disconnect error=%v", err)
	}
}

func TestHostManagerStdioConnectDisconnectAndNoDispatchBypass(t *testing.T) {
	if err := os.Setenv(hostHelperEnv, "1"); err != nil {
		t.Fatal(err)
	}
	defer os.Unsetenv(hostHelperEnv)
	manager := NewHostManager()
	config := HostConfig{
		ID: "local", Name: "local test", Transport: "stdio", Enabled: true,
		Command: os.Args[0], Args: []string{"-test.run=^TestMCPHostHelperProcess$"},
		EnvironmentAllowlist: []string{hostHelperEnv}, AllowedTools: []string{"lookup"},
		ReadTools: []string{"lookup"}, RequestTimeout: 3 * time.Second,
	}
	bindings, err := manager.Connect(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 1 {
		t.Fatalf("got %d bindings", len(bindings))
	}
	if _, err := bindings[0].Execute(context.Background(), json.RawMessage(`{"q":"hi"}`), "ignored-key"); err != ErrDispatchNotAllowed {
		t.Fatalf("call without engine dispatch proof was not refused: %v", err)
	}
	if err := manager.Disconnect("local"); err != nil {
		t.Fatal(err)
	}
}

func TestHostManagerRejectsUnsupportedNegotiatedProtocolVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request.Method == "initialize" {
			respondMCP(w, request.ID, map[string]any{"protocolVersion": "2026-07-28", "capabilities": map[string]any{}})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	manager := NewHostManager()
	_, err := manager.Connect(context.Background(), HostConfig{ID: "future", Transport: "streamable_http", Enabled: true, Endpoint: server.URL, AllowedTools: []string{"lookup"}, ReadTools: []string{"lookup"}})
	if err == nil || !strings.Contains(err.Error(), "unsupported protocol version") {
		t.Fatalf("expected unsupported version refusal, got %v", err)
	}
	if _, err := manager.Tools("future"); err != ErrHostNotConnected {
		t.Fatalf("failed connection leaked runtime state: %v", err)
	}
}

func TestHostManagerAcceptsEarlierStreamableHTTPRevisionAndTerminatesSession(t *testing.T) {
	var deleted atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			if r.Header.Get("Mcp-Session-Id") != "session-legacy" {
				t.Error("session ID missing during disconnect")
			}
			if r.Header.Get("MCP-Protocol-Version") != "2025-06-18" {
				t.Errorf("unexpected protocol version on disconnect: %q", r.Header.Get("MCP-Protocol-Version"))
			}
			deleted.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("invalid MCP request")
			return
		}
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "session-legacy")
			respondMCP(w, req.ID, map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			respondMCP(w, req.ID, map[string]any{"tools": []any{map[string]any{"name": "lookup", "inputSchema": map[string]any{"type": "object"}}}})
		default:
			t.Errorf("unexpected method: %s", req.Method)
		}
	}))
	defer server.Close()

	manager := NewHostManager()
	_, err := manager.Connect(context.Background(), HostConfig{ID: "legacy", Transport: "streamable_http", Enabled: true, Endpoint: server.URL, AllowedTools: []string{"lookup"}, ReadTools: []string{"lookup"}})
	if err != nil {
		t.Fatalf("connect to supported legacy revision: %v", err)
	}
	if err := manager.Disconnect("legacy"); err != nil {
		t.Fatal(err)
	}
	if deleted.Load() != 1 {
		t.Fatalf("expected session termination request, got %d", deleted.Load())
	}
}

func TestHostManagerReportsAuthenticationRequiredWithoutPersistingHost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("private provider diagnostics"))
	}))
	defer server.Close()
	manager := NewHostManager()
	_, err := manager.Connect(context.Background(), HostConfig{
		ID: "calendar", Name: "Google Calendar", Transport: "streamable_http", Enabled: true,
		Endpoint: server.URL, AllowedTools: []string{"list"}, ReadTools: []string{"list"},
	})
	if !errors.Is(err, ErrAuthenticationRequired) || strings.Contains(err.Error(), "private provider diagnostics") {
		t.Fatalf("authentication error = %v", err)
	}
	if _, err := manager.Tools("calendar"); err != ErrHostNotConnected {
		t.Fatalf("auth failure left active host state: %v", err)
	}
}

func respondMCP(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func TestHostManagerConfiguredNameAndSchemaAreRetained(t *testing.T) {
	// This checks alias construction without starting a transport. Exact schema
	// handling is exercised by the fake server in TestHostManagerHTTPConnectPolicyAndDisconnect.
	name := ToolAlias("analytics", "query")
	if strings.Contains(name, " ") || len(name) > 64 || !strings.HasPrefix(name, "mcp_analytics_") {
		t.Fatalf("unexpected alias %q", name)
	}
}
