package server

import (
	"net/http"
	"ownbot/internal/events"
)

func (s *Server) notify(kind, run, approval, status string) {
	s.events.Publish(events.Event{Type: kind, RunID: run, ApprovalID: approval, Status: status})
}

func (s *Server) eventTicket(w http.ResponseWriter, r *http.Request) {
	principal, _ := r.Context().Value(principalContextKey{}).(string)
	if principal == "" {
		principal = "admin"
	}
	ticket, err := s.events.Tickets().IssueFor(principal)
	if err != nil {
		errJSON(w, 429, "too many outstanding event tickets")
		return
	}
	write(w, 201, map[string]any{"ticket": ticket, "expires_in": 30})
}
