package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type auditEventResult struct {
	ID        int64          `json:"id"`
	RunID     string         `json:"run_id"`
	EventType string         `json:"event_type"`
	Details   map[string]any `json:"details"`
}

func insertAuditFixture(t *testing.T, s *Server, runID, eventType, details string) int64 {
	t.Helper()
	result, err := s.db.Exec(`INSERT INTO audit(run_id,event_type,details_json,created_at)VALUES(?,?,?,?)`, runID, eventType, details, now())
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func readAuditPage(t *testing.T, s *Server, token, path string) []auditEventResult {
	t.Helper()
	w := request(t, s.Handler(), http.MethodGet, path, "", "127.0.0.1:8765", "", token)
	if w.Code != http.StatusOK {
		t.Fatalf("audit page %s: %d %s", path, w.Code, w.Body.String())
	}
	var events []auditEventResult
	if err := json.Unmarshal(w.Body.Bytes(), &events); err != nil {
		t.Fatalf("decode audit page: %v", err)
	}
	return events
}

func TestAuditPaginationKeepsDefaultPageAndWalksEveryEvent(t *testing.T) {
	s, token := testApp(t)
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	statement, err := tx.Prepare(`INSERT INTO audit(run_id,event_type,details_json,created_at)VALUES(?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	const total = 1201
	for i := 1; i <= total; i++ {
		if _, err = statement.Exec(fmt.Sprintf("run-%d", i), "fixture.event", fmt.Sprintf(`{"sequence":%d}`, i), now()); err != nil {
			statement.Close()
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err = statement.Close(); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}

	first := readAuditPage(t, s, token, "/api/audit")
	if len(first) != auditPageLimit || first[0].ID != total || first[len(first)-1].ID != total-auditPageLimit+1 {
		t.Fatalf("default page mismatch: len=%d first=%d last=%d", len(first), first[0].ID, first[len(first)-1].ID)
	}

	seen := make(map[int64]bool, total)
	page := first
	for _, event := range page {
		seen[event.ID] = true
	}
	cursor := page[len(page)-1].ID
	for len(page) == auditPageLimit {
		page = readAuditPage(t, s, token, fmt.Sprintf("/api/audit?before_id=%d&limit=500", cursor))
		for _, event := range page {
			if event.ID >= cursor || seen[event.ID] {
				t.Fatalf("cursor returned a duplicate or non-older ID %d before %d", event.ID, cursor)
			}
			seen[event.ID] = true
		}
		if len(page) > 0 {
			cursor = page[len(page)-1].ID
		}
	}
	if len(seen) != total {
		t.Fatalf("pagination returned %d of %d event IDs", len(seen), total)
	}
}

func TestAuditFiltersAreExactAndUseCursor(t *testing.T) {
	s, token := testApp(t)
	ids := []int64{
		insertAuditFixture(t, s, "run-target", "tool.succeeded", `{"n":1}`),
		insertAuditFixture(t, s, "run-other", "tool.succeeded", `{"n":2}`),
		insertAuditFixture(t, s, "run-target", "tool.refused", `{"n":3}`),
		insertAuditFixture(t, s, "run-target", "tool.succeeded", `{"n":4}`),
		insertAuditFixture(t, s, "run-target", "tool.succeeded", `{"n":5}`),
	}

	page := readAuditPage(t, s, token, "/api/audit?run_id=run-target&event_type=tool.succeeded&limit=2")
	if len(page) != 2 || page[0].ID != ids[4] || page[1].ID != ids[3] {
		t.Fatalf("filtered first page mismatch: %#v", page)
	}
	page = readAuditPage(t, s, token, fmt.Sprintf("/api/audit?run_id=run-target&event_type=tool.succeeded&limit=2&before_id=%d", ids[3]))
	if len(page) != 1 || page[0].ID != ids[0] {
		t.Fatalf("filtered cursor page mismatch: %#v", page)
	}

	page = readAuditPage(t, s, token, "/api/audit?run_id=run-target%27%20OR%201%3D1&event_type=tool.succeeded")
	if len(page) != 0 {
		t.Fatalf("filter values must be compared literally, got %#v", page)
	}
}

func TestAuditRejectsMalformedOrUnboundedQueries(t *testing.T) {
	s, token := testApp(t)
	for _, path := range []string{
		"/api/audit?unknown=1",
		"/api/audit?limit=0",
		"/api/audit?limit=501",
		"/api/audit?limit=01",
		"/api/audit?limit=1&limit=2",
		"/api/audit?before_id=0",
		"/api/audit?before_id=-1",
		"/api/audit?run_id=",
		"/api/audit?event_type=",
		"/api/audit?run_id=" + strings.Repeat("x", 201),
		"/api/audit?event_type=" + strings.Repeat("x", 101),
	} {
		w := request(t, s.Handler(), http.MethodGet, path, "", "127.0.0.1:8765", "", token)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d %s, want 400", path, w.Code, w.Body.String())
		}
	}
}
