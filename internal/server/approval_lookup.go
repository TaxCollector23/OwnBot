package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
)

func (s *Server) getApproval(w http.ResponseWriter, r *http.Request) {
	var approval Approval
	var args string
	err := s.db.QueryRowContext(r.Context(), `SELECT id,agent_id,run_id,tool,args_json,status,reason,requested_at,decided_at,approver FROM approvals WHERE id=?`, r.PathValue("id")).Scan(
		&approval.ID,
		&approval.AgentID,
		&approval.RunID,
		&approval.Tool,
		&args,
		&approval.Status,
		&approval.Reason,
		&approval.RequestedAt,
		&approval.DecidedAt,
		&approval.Approver,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			errJSON(w, http.StatusNotFound, "approval not found")
			return
		}
		errJSON(w, http.StatusInternalServerError, "could not read approval")
		return
	}
	if !json.Valid([]byte(args)) {
		errJSON(w, http.StatusInternalServerError, "approval record contains invalid stored arguments")
		return
	}
	approval.Args = json.RawMessage(args)
	write(w, http.StatusOK, approval)
}
