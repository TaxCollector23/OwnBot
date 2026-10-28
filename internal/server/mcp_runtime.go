package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"ownbot/internal/mcp"
)

func (s *Server) loadMCP(id string) (MCPServerConfig, error) {
	var c MCPServerConfig
	var args, env, allowed, reads, writes string
	err := s.db.QueryRow(`SELECT id,name,transport,endpoint,command,args_json,working_directory,environment_allowlist_json,allowed_tools_json,read_tools_json,write_tools_json,request_timeout_seconds,created_at,updated_at FROM mcp_servers WHERE id=?`, id).Scan(&c.ID, &c.Name, &c.Transport, &c.Endpoint, &c.Command, &args, &c.WorkingDirectory, &env, &allowed, &reads, &writes, &c.RequestTimeoutSeconds, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return c, err
	}
	for _, field := range []struct {
		raw    string
		target *[]string
	}{{args, &c.Args}, {env, &c.EnvironmentAllowlist}, {allowed, &c.AllowedTools}, {reads, &c.ReadTools}, {writes, &c.WriteTools}} {
		if err = json.Unmarshal([]byte(field.raw), field.target); err != nil {
			return c, err
		}
	}
	_, err = s.hosts.Tools(id)
	c.Enabled = err == nil
	c.RuntimeAvailable = c.Enabled
	return c, nil
}
func (s *Server) connectMCP(w http.ResponseWriter, r *http.Request) {
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	c, err := s.loadMCP(r.PathValue("id"))
	if err != nil {
		errJSON(w, 404, "MCP server not found")
		return
	}
	if c.Transport != "streamable_http" {
		errJSON(w, 409, "stdio execution remains disabled until OS isolation can protect local credentials")
		return
	}
	if _, stateErr := s.hosts.Tools(c.ID); errors.Is(stateErr, mcp.ErrAuthenticationRequired) {
		// A provider rejected the connected session. Revoke its stale tool bindings
		// before allowing a fresh connection attempt.
		s.registry.RemoveServer(c.ID)
		_ = s.hosts.Disconnect(c.ID)
	}
	tools, err := s.hosts.Connect(r.Context(), mcp.HostConfig{ID: c.ID, Name: c.Name, Transport: c.Transport, Enabled: true, Endpoint: c.Endpoint, AllowedTools: c.AllowedTools, ReadTools: c.ReadTools, WriteTools: c.WriteTools, RequestTimeout: time.Duration(c.RequestTimeoutSeconds) * time.Second})
	if err != nil {
		if errors.Is(err, mcp.ErrAuthenticationRequired) {
			write(w, 409, map[string]any{
				"error": "Provider sign-in is required, but OAuth sign-in is not wired in this build",
				"code":  "auth_required", "auth_status": "required", "connected": false,
				"runtime_available": false, "grants_created": false,
			})
			return
		}
		errJSON(w, 409, "MCP connection failed: verify server availability, supported protocol, and configured tool names")
		return
	}
	for _, tool := range tools {
		if err = s.registry.Register(tool); err != nil {
			s.registry.RemoveServer(c.ID)
			_ = s.hosts.Disconnect(c.ID)
			errJSON(w, 409, "MCP tool registration failed")
			return
		}
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.registry.RemoveServer(c.ID)
		_ = s.hosts.Disconnect(c.ID)
		errJSON(w, 500, "could not audit connection")
		return
	}
	defer tx.Rollback()
	if err = eventTx(tx, "mcp.connected", "local-user", "", "", map[string]any{"server_id": c.ID, "tool_count": len(tools)}); err != nil || tx.Commit() != nil {
		s.registry.RemoveServer(c.ID)
		_ = s.hosts.Disconnect(c.ID)
		errJSON(w, 500, "could not audit connection")
		return
	}
	c.Enabled = true
	c.RuntimeAvailable = true
	write(w, 200, c)
}
func (s *Server) disconnectMCP(w http.ResponseWriter, r *http.Request) {
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	c, err := s.loadMCP(r.PathValue("id"))
	if err != nil {
		errJSON(w, 404, "MCP server not found")
		return
	}
	s.registry.RemoveServer(c.ID)
	err = s.hosts.Disconnect(c.ID)
	if err != nil && !errors.Is(err, mcp.ErrHostNotConnected) {
		errJSON(w, 502, "local MCP access revoked; provider session cleanup failed")
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		errJSON(w, 500, "could not audit disconnect")
		return
	}
	defer tx.Rollback()
	if err = eventTx(tx, "mcp.disconnected", "local-user", "", "", map[string]any{"server_id": c.ID}); err != nil || tx.Commit() != nil {
		errJSON(w, 500, "could not audit disconnect")
		return
	}
	c.Enabled = false
	c.RuntimeAvailable = false
	write(w, 200, c)
}
func (s *Server) listTools(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	for _, tool := range s.registry.List() {
		if tool.Source == "mcp" {
			if _, err := s.hosts.Tools(tool.Server); err != nil {
				s.registry.RemoveServer(tool.Server)
				continue
			}
		}
		item := map[string]any{"name": tool.Schema.Name, "description": tool.Schema.Description, "risk": tool.Risk, "source": tool.Source, "server": tool.Server}
		if tool.Source == "mcp" {
			if providerName, ok := s.hosts.ProviderToolName(tool.Server, tool.Schema.Name); ok {
				item["provider_tool"] = providerName
			}
		}
		out = append(out, item)
	}
	write(w, 200, out)
}
