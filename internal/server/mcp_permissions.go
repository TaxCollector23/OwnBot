package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"ownbot/internal/mcp"
)

const maxMCPDescription = 2048

type mcpToolMetadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (s *Server) discoverMCPTools(w http.ResponseWriter, r *http.Request) {
	c, err := s.loadMCP(r.PathValue("id"))
	if err != nil {
		errJSON(w, http.StatusNotFound, "MCP server not found")
		return
	}
	if c.Transport != "streamable_http" {
		errJSON(w, http.StatusConflict, "tool discovery is available only for HTTP MCP servers")
		return
	}
	tools, err := s.fetchMCPToolMetadata(r.Context(), c)
	if err != nil {
		if errors.Is(err, mcp.ErrAuthenticationRequired) {
			errJSON(w, http.StatusConflict, "provider sign-in is required")
			return
		}
		errJSON(w, http.StatusBadGateway, "could not discover MCP tool metadata; verify the provider is available and signed in")
		return
	}
	write(w, http.StatusOK, map[string]any{"tools": tools})
}

func (s *Server) fetchMCPToolMetadata(ctx context.Context, c MCPServerConfig) ([]mcpToolMetadata, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.RequestTimeoutSeconds)*time.Second)
	defer cancel()
	client := &mcp.HTTPClient{
		Endpoint: c.Endpoint,
		Enabled:  true,
		Token:    func(ctx context.Context) (string, error) { return s.accessTokenForMCP(ctx, c.ID) },
	}
	if err := client.Connect(ctx); err != nil {
		return nil, err
	}
	remote, err := client.DiscoverTools(ctx)
	if err != nil {
		return nil, err
	}
	metadata := make([]mcpToolMetadata, 0, len(remote))
	for _, tool := range remote {
		name := strings.TrimSpace(tool.Name)
		if name == "" || len(name) > 256 || strings.ContainsAny(name, "\x00\r\n") {
			continue
		}
		description := strings.TrimSpace(tool.Description)
		if len(description) > maxMCPDescription {
			description = description[:maxMCPDescription]
		}
		metadata = append(metadata, mcpToolMetadata{Name: name, Description: description})
	}
	return metadata, nil
}

type mcpPermissionUpdate struct {
	ReadTools  []string `json:"read_tools"`
	WriteTools []string `json:"write_tools"`
}

func (s *Server) updateMCPPermissions(w http.ResponseWriter, r *http.Request) {
	var update mcpPermissionUpdate
	if err := decode(r, &update); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid MCP permission update")
		return
	}
	var err error
	if update.ReadTools, err = cleanNames(update.ReadTools, true); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid read tool list")
		return
	}
	if update.WriteTools, err = cleanNames(update.WriteTools, true); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid write tool list")
		return
	}
	readSet := make(map[string]bool, len(update.ReadTools))
	for _, name := range update.ReadTools {
		readSet[name] = true
	}
	for _, name := range update.WriteTools {
		if readSet[name] {
			errJSON(w, http.StatusBadRequest, "a tool can be classified as read or write, not both")
			return
		}
	}

	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	c, err := s.loadMCP(r.PathValue("id"))
	if err != nil {
		errJSON(w, http.StatusNotFound, "MCP server not found")
		return
	}
	if c.Transport != "streamable_http" {
		errJSON(w, http.StatusConflict, "tool permissions are available only for HTTP MCP servers")
		return
	}
	metadata, err := s.fetchMCPToolMetadata(r.Context(), c)
	if err != nil {
		if errors.Is(err, mcp.ErrAuthenticationRequired) {
			errJSON(w, http.StatusConflict, "provider sign-in is required")
			return
		}
		errJSON(w, http.StatusBadGateway, "could not verify provider tool metadata; no permissions were changed")
		return
	}
	known := make(map[string]bool, len(metadata))
	for _, tool := range metadata {
		known[tool.Name] = true
	}
	for _, names := range [][]string{update.ReadTools, update.WriteTools} {
		for _, name := range names {
			if !known[name] {
				errJSON(w, http.StatusBadRequest, "permission lists may include only tools returned by the provider")
				return
			}
		}
	}

	allowed := append(append([]string(nil), update.ReadTools...), update.WriteTools...)
	allowedJSON, _ := json.Marshal(allowed)
	readsJSON, _ := json.Marshal(update.ReadTools)
	writesJSON, _ := json.Marshal(update.WriteTools)
	updated := now()
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not save MCP permissions")
		return
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE mcp_servers SET allowed_tools_json=?,read_tools_json=?,write_tools_json=?,updated_at=? WHERE id=?`, string(allowedJSON), string(readsJSON), string(writesJSON), updated, c.ID); err != nil {
		errJSON(w, http.StatusInternalServerError, "could not save MCP permissions")
		return
	}
	if err = eventTx(tx, "mcp.permissions_updated", "local-user", "", "", map[string]any{
		"server_id": c.ID, "read_tool_count": len(update.ReadTools), "write_tool_count": len(update.WriteTools), "agent_grants_changed": false,
	}); err != nil || tx.Commit() != nil {
		errJSON(w, http.StatusInternalServerError, "could not audit MCP permission change")
		return
	}
	s.registry.RemoveServer(c.ID)
	_ = s.hosts.Disconnect(c.ID)
	write(w, http.StatusOK, map[string]any{
		"server_id": c.ID, "allowed_tools": allowed, "read_tools": update.ReadTools,
		"write_tools": update.WriteTools, "connected": false, "agent_grants_changed": false,
	})
}
