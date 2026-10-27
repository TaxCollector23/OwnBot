package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestHTTPClientSessionAndAuthorization(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("invalid request")
			return
		}
		if req.Method != "initialize" && r.Header.Get("Mcp-Session-Id") != "session" {
			t.Error("missing session")
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "session")
			w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"test","version":"1"}}}`))
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":{"tools":[{"name":"read","inputSchema":{"type":"object"}},{"name":"write","inputSchema":{"type":"object"}}]}}`))
		case "tools/call":
			calls.Add(1)
			w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":{"content":[]}}`))
		}
	}))
	defer srv.Close()
	c := &HTTPClient{Endpoint: srv.URL, Enabled: true, AllowedTools: []string{"read", "write"}}
	ctx := context.Background()
	if err := c.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	tools, err := c.Tools(ctx)
	if err != nil || len(tools) != 2 {
		t.Fatalf("tools: %v %v", tools, err)
	}
	if _, err = c.CallTool(ctx, "write", json.RawMessage(`{}`)); err == nil {
		t.Fatal("call without authorization succeeded")
	}
	if calls.Load() != 0 {
		t.Fatal("unauthorized call reached server")
	}
	c.Authorize = func(context.Context, string, json.RawMessage) error { return nil }
	if _, err = c.CallTool(ctx, "write", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("authorized call did not reach server")
	}
}
func TestHTTPClientRejectsInsecureRemote(t *testing.T) {
	c := &HTTPClient{Endpoint: "http://example.com/mcp", Enabled: true}
	if err := c.Connect(context.Background()); err == nil {
		t.Fatal("remote HTTP accepted")
	}
}

func TestHTTPClientHandlesSSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		var result string
		switch req.Method {
		case "initialize":
			result = `{"protocolVersion":"2025-06-18"}`
		case "tools/list":
			result = `{"tools":[]}`
		default:
			t.Errorf("unexpected method %s", req.Method)
			return
		}
		_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":" + string(req.ID) + ",\"result\":" + result + "}\n\n"))
	}))
	defer srv.Close()
	c := &HTTPClient{Endpoint: srv.URL, Enabled: true}
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Tools(context.Background()); err != nil {
		t.Fatal(err)
	}
}
