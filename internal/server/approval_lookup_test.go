package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
)

func TestGetApprovalFindsRecordsOlderThanListLimit(t *testing.T) {
	s, token := testApp(t)
	for i := 0; i < 501; i++ {
		id := fmt.Sprintf("approval-%03d", i)
		requestedAt := fmt.Sprintf("2020-%03d", i)
		_, err := s.db.Exec(`INSERT INTO approvals(id,agent_id,run_id,tool,args_json,status,reason,requested_at,decided_at,approver) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, "agent", "run", "calendar.read", `{"query":"q"}`, "pending", "reason", requestedAt, "", "")
		if err != nil {
			t.Fatalf("insert approval %s: %v", id, err)
		}
	}

	listResponse := request(t, s.Handler(), http.MethodGet, "/api/approvals", "", "127.0.0.1:8765", "", token)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listResponse.Code, listResponse.Body.String())
	}
	var latest []Approval
	if err := json.Unmarshal(listResponse.Body.Bytes(), &latest); err != nil {
		t.Fatalf("decode approval list: %v", err)
	}
	if len(latest) != 500 {
		t.Fatalf("list returned %d records, want latest 500", len(latest))
	}
	for _, approval := range latest {
		if approval.ID == "approval-000" {
			t.Fatal("oldest approval unexpectedly appeared in the latest-500 list")
		}
	}

	response := request(t, s.Handler(), http.MethodGet, "/api/approvals/approval-000", "", "127.0.0.1:8765", "", token)
	if response.Code != http.StatusOK {
		t.Fatalf("show older record status=%d body=%s", response.Code, response.Body.String())
	}
	var found Approval
	if err := json.Unmarshal(response.Body.Bytes(), &found); err != nil {
		t.Fatalf("decode approval detail: %v", err)
	}
	if found.ID != "approval-000" || found.Tool != "calendar.read" || found.Status != "pending" || string(found.Args) != `{"query":"q"}` {
		t.Fatalf("unexpected approval detail: %+v", found)
	}
}

func TestGetApprovalNotFoundUsesBoundIDQuery(t *testing.T) {
	s, token := testApp(t)
	for _, id := range []string{"missing", "' OR 1=1 --"} {
		response := request(t, s.Handler(), http.MethodGet, "/api/approvals/"+url.PathEscape(id), "", "127.0.0.1:8765", "", token)
		if response.Code != http.StatusNotFound {
			t.Errorf("id %q status=%d body=%s, want 404", id, response.Code, response.Body.String())
		}
		if response.Body.String() != "{\"error\":\"approval not found\"}\n" {
			t.Errorf("id %q returned unexpected error body: %s", id, response.Body.String())
		}
	}
}
