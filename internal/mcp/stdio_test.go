package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

const helperEnv = "OWNBOT_TEST_MCP_HELPER"
const secretEnv = "OWNBOT_TEST_SECRET_TOKEN"

// TestMCPHelperProcess becomes a tiny local MCP stdio server when launched by the tests.
func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req struct {
			ID     json.RawMessage            `json:"id"`
			Method string                     `json:"method"`
			Params map[string]json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			os.Exit(3)
		}
		if len(req.ID) == 0 {
			continue
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "test", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{
				map[string]any{"name": "read_info", "inputSchema": map[string]any{"type": "object"}},
				map[string]any{"name": "write_data", "inputSchema": map[string]any{"type": "object"}},
			}}
		case "tools/call":
			if os.Getenv(secretEnv) != "" {
				result = map[string]any{"content": []any{map[string]string{"type": "text", "text": "secret inherited"}}}
			} else {
				result = map[string]any{"content": []any{map[string]string{"type": "text", "text": "ok"}}}
			}
		default:
			os.Exit(4)
		}
		response := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "result": result}
		raw, _ := json.Marshal(response)
		fmt.Fprintln(os.Stdout, string(raw))
	}
	os.Exit(0)
}

func helperConfig(t *testing.T) Config {
	t.Helper()
	old, had := os.LookupEnv(helperEnv)
	_ = os.Setenv(helperEnv, "1")
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(helperEnv, old)
		} else {
			_ = os.Unsetenv(helperEnv)
		}
	})
	return Config{Name: "test", Enabled: true, Command: os.Args[0], Args: []string{"-test.run=^TestMCPHelperProcess$"}, EnvironmentAllowlist: []string{helperEnv}, AllowedTools: []string{"read_info", "write_data"}, ReadTools: []string{"read_info"}, WriteTools: []string{"write_data"}, RequestTimeout: 2_000_000_000}
}

func TestDisabledByDefaultAndNoAllowlistFailsClosed(t *testing.T) {
	cfg := helperConfig(t)
	cfg.Enabled = false
	if err := New(cfg).Connect(context.Background()); err != ErrDisabled {
		t.Fatalf("want disabled, got %v", err)
	}
	cfg.Enabled = true
	cfg.AllowedTools = nil
	if err := New(cfg).Connect(context.Background()); err == nil {
		t.Fatal("expected empty allowlist to fail")
	}
}

func TestStdioDiscoveryAndAuthorizationForEveryCall(t *testing.T) {
	cfg := helperConfig(t)
	client := New(cfg)
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	tools, err := client.Tools()
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 {
		t.Fatalf("got %d tools", len(tools))
	}
	if _, err := client.CallTool(context.Background(), "read_info", json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "policy decision") {
		t.Fatalf("call without policy should fail closed: %v", err)
	}
	cfg.AuthorizeCall = func(context.Context, string, json.RawMessage) error { return nil }
	client = New(cfg)
	if err = client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result, err := client.CallTool(context.Background(), "read_info", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Content), "ok") {
		t.Fatalf("unexpected tool result %s", result.Content)
	}
	if _, err = client.CallTool(context.Background(), "unlisted", json.RawMessage(`{}`)); err != ErrToolNotAllowed {
		t.Fatalf("unlisted tool allowed: %v", err)
	}
}

func TestStdioScrubsSecretEnvironment(t *testing.T) {
	old, had := os.LookupEnv(secretEnv)
	_ = os.Setenv(secretEnv, "fake-test-secret")
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(secretEnv, old)
		} else {
			_ = os.Unsetenv(secretEnv)
		}
	})
	cfg := helperConfig(t)
	cfg.AuthorizeCall = func(context.Context, string, json.RawMessage) error { return nil }
	client := New(cfg)
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result, err := client.CallTool(context.Background(), "read_info", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(result.Content), "secret inherited") {
		t.Fatal("secret environment variable reached child process")
	}
}
