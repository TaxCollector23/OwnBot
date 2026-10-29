package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const auditPageLimit = 500

type auditQuery struct {
	beforeID  int64
	hasBefore bool
	limit     int
	runID     string
	eventType string
}

func parseAuditQuery(raw string) (auditQuery, error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return auditQuery{}, errors.New("invalid audit query")
	}

	allowed := map[string]bool{"before_id": true, "limit": true, "run_id": true, "event_type": true}
	for key, items := range values {
		if !allowed[key] {
			return auditQuery{}, errors.New("unsupported audit query parameter")
		}
		if len(items) != 1 || items[0] == "" {
			return auditQuery{}, errors.New("audit query parameters must appear once with a value")
		}
	}

	query := auditQuery{limit: auditPageLimit}
	if value, ok := values["limit"]; ok {
		limit, err := parsePositiveAuditInteger(value[0])
		if err != nil || limit > auditPageLimit {
			return auditQuery{}, errors.New("limit must be a decimal integer from 1 to 500")
		}
		query.limit = int(limit)
	}
	if value, ok := values["before_id"]; ok {
		query.beforeID, err = parsePositiveAuditInteger(value[0])
		if err != nil {
			return auditQuery{}, errors.New("before_id must be a positive decimal integer")
		}
		query.hasBefore = true
	}
	if value, ok := values["run_id"]; ok {
		if len(value[0]) > 200 {
			return auditQuery{}, errors.New("run_id must be at most 200 characters")
		}
		query.runID = value[0]
	}
	if value, ok := values["event_type"]; ok {
		if len(value[0]) > 100 {
			return auditQuery{}, errors.New("event_type must be at most 100 characters")
		}
		query.eventType = value[0]
	}
	return query, nil
}

func parsePositiveAuditInteger(value string) (int64, error) {
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil || number <= 0 || strconv.FormatInt(number, 10) != value {
		return 0, errors.New("not a canonical positive decimal integer")
	}
	return number, nil
}

func (s *Server) auditEvents(w http.ResponseWriter, r *http.Request) {
	query, err := parseAuditQuery(r.URL.RawQuery)
	if err != nil {
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}

	conditions := make([]string, 0, 3)
	args := make([]any, 0, 4)
	if query.hasBefore {
		conditions = append(conditions, "id<?")
		args = append(args, query.beforeID)
	}
	if query.runID != "" {
		conditions = append(conditions, "run_id=?")
		args = append(args, query.runID)
	}
	if query.eventType != "" {
		conditions = append(conditions, "event_type=?")
		args = append(args, query.eventType)
	}

	statement := "SELECT id,run_id,agent_id,event_type,actor,details_json,created_at FROM audit"
	if len(conditions) > 0 {
		statement += " WHERE " + strings.Join(conditions, " AND ")
	}
	statement += " ORDER BY id DESC LIMIT ?"
	args = append(args, query.limit)
	rows, err := s.db.QueryContext(r.Context(), statement, args...)
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not read audit events")
		return
	}
	defer rows.Close()

	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var run, agent, kind, actor, details, at string
		if err = rows.Scan(&id, &run, &agent, &kind, &actor, &details, &at); err != nil {
			errJSON(w, http.StatusInternalServerError, "could not read audit events")
			return
		}
		var detail any
		_ = json.Unmarshal([]byte(details), &detail)
		out = append(out, map[string]any{"id": id, "run_id": run, "agent_id": agent, "event_type": kind, "actor": actor, "details": detail, "created_at": at})
	}
	if err = rows.Err(); err != nil {
		errJSON(w, http.StatusInternalServerError, "could not read audit events")
		return
	}
	write(w, http.StatusOK, out)
}
