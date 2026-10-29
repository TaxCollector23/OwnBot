package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestAgentListIncludesPerAgentMCPAllowlist(t *testing.T) {
	s, token := testApp(t)
	at := now()
	for _, agent := range []struct{ id, name, tools string }{
		{"agent-with-mcp", "With MCP", `["calendar.create"]`},
		{"agent-without-mcp", "Without MCP", `[]`},
	} {
		if _, err := s.db.Exec(`INSERT INTO agents(id,name,enabled,tools_json,created_at,updated_at)VALUES(?,?,1,?,?,?)`, agent.id, agent.name, agent.tools, at, at); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`INSERT INTO agent_settings(agent_id,mcp_servers_json)VALUES(?,?)`, "agent-with-mcp", `["server-a","server-b"]`); err != nil {
		t.Fatal(err)
	}

	w := request(t, s.Handler(), http.MethodGet, "/api/agents", "", "127.0.0.1:8765", "", token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/agents: %d %s", w.Code, w.Body.String())
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	got := make(map[string][]string, len(rows))
	for _, row := range rows {
		var id string
		if err := json.Unmarshal(row["id"], &id); err != nil {
			t.Fatalf("agent ID missing: %s", row)
		}
		encoded, ok := row["mcp_servers"]
		if !ok {
			t.Fatalf("agent %q has no mcp_servers field: %s", id, row)
		}
		var servers []string
		if err := json.Unmarshal(encoded, &servers); err != nil || servers == nil {
			t.Fatalf("agent %q MCP allowlist must be a JSON array: %s (err %v)", id, encoded, err)
		}
		got[id] = servers
	}
	if len(got) != 2 || len(got["agent-with-mcp"]) != 2 || got["agent-with-mcp"][0] != "server-a" || got["agent-with-mcp"][1] != "server-b" {
		t.Fatalf("MCP allowlist not returned: %#v", got)
	}
	if got["agent-without-mcp"] == nil || len(got["agent-without-mcp"]) != 0 {
		t.Fatalf("agent without settings must return an empty list: %#v", got["agent-without-mcp"])
	}
}
