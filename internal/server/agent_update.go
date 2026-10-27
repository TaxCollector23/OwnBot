package server

import (
	"encoding/json"
	"net/http"
	"strings"
)

func (s *Server) updateAgent(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Name      *string   `json:"name"`
		Model     *string   `json:"model"`
		Enabled   *bool     `json:"enabled"`
		WebSearch *bool     `json:"web_search"`
		MCP       *bool     `json:"mcp_enabled"`
		Tools     *[]string `json:"tools"`
		Servers   *[]string `json:"mcp_servers"`
		Steps     *int      `json:"max_steps"`
		Tokens    *int      `json:"max_tokens"`
		Timeout   *int      `json:"timeout_seconds"`
	}
	if decode(r, &p) != nil {
		errJSON(w, 400, "invalid agent configuration")
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		errJSON(w, 500, "could not update agent")
		return
	}
	defer tx.Rollback()
	var a Agent
	var tools string
	var en, ws, mc int
	err = tx.QueryRow(`SELECT id,name,model,enabled,web_search,mcp_enabled,tools_json,max_steps,max_tokens,timeout_seconds FROM agents WHERE id=?`, r.PathValue("id")).Scan(&a.ID, &a.Name, &a.Model, &en, &ws, &mc, &tools, &a.MaxSteps, &a.MaxTokens, &a.TimeoutSeconds)
	if err != nil {
		errJSON(w, 404, "agent not found")
		return
	}
	_ = json.Unmarshal([]byte(tools), &a.Tools)
	a.Enabled = en != 0
	a.WebSearch = ws != 0
	a.MCPEnabled = mc != 0
	if p.Name != nil {
		a.Name = strings.TrimSpace(*p.Name)
	}
	if p.Model != nil {
		a.Model = strings.TrimSpace(*p.Model)
	}
	if p.Enabled != nil {
		a.Enabled = *p.Enabled
	}
	if p.WebSearch != nil {
		a.WebSearch = *p.WebSearch
	}
	if p.MCP != nil {
		a.MCPEnabled = *p.MCP
	}
	if p.Tools != nil {
		a.Tools = *p.Tools
	}
	if p.Steps != nil {
		a.MaxSteps = *p.Steps
	}
	if p.Tokens != nil {
		a.MaxTokens = *p.Tokens
	}
	if p.Timeout != nil {
		a.TimeoutSeconds = *p.Timeout
	}
	if a.Name == "" || len(a.Name) > 100 || len(a.Model) > 200 || len(a.Tools) > 128 || a.MaxSteps < 1 || a.MaxSteps > 50 || a.MaxTokens < 1 || a.MaxTokens > 200000 || a.TimeoutSeconds < 1 || a.TimeoutSeconds > 3600 {
		errJSON(w, 400, "agent configuration exceeds limits")
		return
	}
	for _, tool := range a.Tools {
		if strings.TrimSpace(tool) == "" || len(tool) > 200 {
			errJSON(w, 400, "invalid tool name")
			return
		}
	}
	raw, _ := json.Marshal(a.Tools)
	_, err = tx.Exec(`UPDATE agents SET name=?,model=?,enabled=?,web_search=?,mcp_enabled=?,tools_json=?,max_steps=?,max_tokens=?,timeout_seconds=?,updated_at=? WHERE id=?`, a.Name, a.Model, boolInt(a.Enabled), boolInt(a.WebSearch), boolInt(a.MCPEnabled), string(raw), a.MaxSteps, a.MaxTokens, a.TimeoutSeconds, now(), a.ID)
	if err != nil {
		errJSON(w, 500, "could not update agent")
		return
	}
	if p.Servers != nil {
		if len(*p.Servers) > 32 {
			errJSON(w, 400, "too many MCP servers")
			return
		}
		for _, id := range *p.Servers {
			var count int
			if tx.QueryRow(`SELECT count(*) FROM mcp_servers WHERE id=?`, id).Scan(&count) != nil || count != 1 {
				errJSON(w, 400, "MCP server not found")
				return
			}
		}
		raw, _ = json.Marshal(*p.Servers)
		_, err = tx.Exec(`INSERT INTO agent_settings(agent_id,mcp_servers_json)VALUES(?,?) ON CONFLICT(agent_id) DO UPDATE SET mcp_servers_json=excluded.mcp_servers_json`, a.ID, string(raw))
		if err != nil {
			errJSON(w, 500, "could not update MCP allowlist")
			return
		}
	}
	if err = eventTx(tx, "agent.updated", "local-user", "", a.ID, map[string]any{"agent_id": a.ID}); err != nil {
		errJSON(w, 500, "could not audit agent update")
		return
	}
	if tx.Commit() != nil {
		errJSON(w, 500, "could not save agent update")
		return
	}
	write(w, 200, a)
}
