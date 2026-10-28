package server

import (
	"net/http"

	"ownbot/internal/mcp"
)

func (s *Server) mcpServices(w http.ResponseWriter, r *http.Request) {
	write(w, http.StatusOK, mcp.Services())
}
