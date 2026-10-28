package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"ownbot/internal/engine"
	"ownbot/internal/models"
)

type errorOnceModel struct{ calls int }

func (m *errorOnceModel) Chat(_ context.Context, req models.Request) (models.Response, error) {
	m.calls++
	if m.calls == 1 {
		return models.Response{}, errors.New("fixture transport unavailable")
	}
	return models.Response{Message: models.Message{Content: "done"}, Usage: models.Usage{TotalTokens: 1}}, nil
}
func (m *errorOnceModel) Stream(context.Context, models.Request) (<-chan models.StreamEvent, error) {
	panic("not used")
}

func stopTestWorker(t *testing.T, s *Server) {
	t.Helper()
	s.stopAutomationWorker()
	s.automationCancel = nil
}

func createTestAutomation(t *testing.T, s *Server, token, agent, kind, schedule string) string {
	t.Helper()
	extra := ""
	switch kind {
	case "interval":
		extra = `,"interval_minutes":1`
	case "daily_utc":
		extra = `,"time_utc":"12:30"`
	}
	body := fmt.Sprintf(`{"name":"daily fixture","enabled":true,"agent_id":%q,"input":"fixture input","trigger_type":%q%s}`, agent, kind, extra)
	w := request(t, s.Handler(), "POST", "/api/automations", body, "127.0.0.1:8765", "", token)
	if w.Code != 201 {
		t.Fatalf("create automation: %d %s", w.Code, w.Body.String())
	}
	var a Automation
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	return a.ID
}

func TestScheduledRunValidationAndDailyUTC(t *testing.T) {
	s, token := testApp(t)
	stopTestWorker(t, s)
	agent := configuredAgent(t, s, token)
	for _, invalid := range []string{
		fmt.Sprintf(`{"name":"bad","enabled":true,"agent_id":%q,"input":"x","trigger_type":"cron","schedule":"* * * * *"}`, agent),
		fmt.Sprintf(`{"name":"bad","enabled":true,"agent_id":%q,"input":"x","trigger_type":"daily_utc","time_utc":"25:99"}`, agent),
		fmt.Sprintf(`{"name":"bad","enabled":true,"agent_id":%q,"input":"x","trigger_type":"interval","interval_minutes":0}`, agent),
	} {
		w := request(t, s.Handler(), "POST", "/api/automations", invalid, "127.0.0.1:8765", "", token)
		if w.Code != 400 {
			t.Fatalf("invalid schedule accepted: %d %s", w.Code, w.Body.String())
		}
	}
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	s.automationClock = func() time.Time { return now }
	id := createTestAutomation(t, s, token, agent, "daily_utc", "")
	var due string
	if err := s.db.QueryRow(`SELECT next_run_at FROM automations WHERE id=?`, id).Scan(&due); err != nil {
		t.Fatal(err)
	}
	want := "2026-10-27T12:30:00Z"
	if !strings.HasPrefix(due, want) {
		t.Fatalf("daily UTC next run = %s, want %s", due, want)
	}
}

func TestTwentyJobsCompleteOnceWithRedactedAttemptAudit(t *testing.T) {
	s, token := testApp(t)
	stopTestWorker(t, s)
	agent := configuredAgent(t, s, token)
	model := &scriptedModel{}
	s.modelClient = func(ModelConfig) models.Client { return model }
	automation := createTestAutomation(t, s, token, agent, "manual", "")
	for i := 0; i < 20; i++ {
		w := request(t, s.Handler(), "POST", "/api/automations/"+automation+"/jobs", fmt.Sprintf(`{"idempotency_key":"fixture-%02d"}`, i), "127.0.0.1:8765", "", token)
		if w.Code != 201 {
			t.Fatalf("enqueue %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	for i := 0; i < 20; i++ {
		if err := s.runScheduledTick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM jobs WHERE status='completed' AND automation_id=?`, automation).Scan(&count); err != nil || count != 20 {
		t.Fatalf("completed jobs=%d err=%v", count, err)
	}
	if len(model.seen) != 20 {
		t.Fatalf("model invocations=%d, want 20", len(model.seen))
	}
	if err := s.db.QueryRow(`SELECT count(DISTINCT step_key) FROM jobs WHERE automation_id=?`, automation).Scan(&count); err != nil || count != 20 {
		t.Fatalf("unique step keys=%d err=%v", count, err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM audit WHERE event_type='job.attempt_started'`).Scan(&count); err != nil || count != 20 {
		t.Fatalf("attempt audit rows=%d err=%v", count, err)
	}
	w := request(t, s.Handler(), "GET", "/api/audit", "", "127.0.0.1:8765", "", token)
	if strings.Contains(w.Body.String(), "fixture input") {
		t.Fatal("audit leaked automation input")
	}
}

func TestIntervalScheduleCreatesOneStableOccurrence(t *testing.T) {
	s, token := testApp(t)
	stopTestWorker(t, s)
	now := time.Now().UTC().Truncate(time.Second)
	s.automationClock = func() time.Time { return now }
	agent := configuredAgent(t, s, token)
	s.modelClient = func(ModelConfig) models.Client { return &scriptedModel{} }
	automation := createTestAutomation(t, s, token, agent, "interval", "")
	now = now.Add(time.Minute)
	if err := s.runScheduledTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.runScheduledTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM jobs WHERE automation_id=?`, automation).Scan(&count); err != nil || count != 1 {
		t.Fatalf("scheduled occurrence count=%d err=%v", count, err)
	}
	var next string
	if err := s.db.QueryRow(`SELECT next_run_at FROM automations WHERE id=?`, automation).Scan(&next); err != nil {
		t.Fatal(err)
	}
	want := now.Add(time.Minute).Format(time.RFC3339Nano)
	if next != want {
		t.Fatalf("next run=%s want=%s", next, want)
	}
}

func TestSafeModelTransportRetryUsesBackoffAndStableRun(t *testing.T) {
	s, token := testApp(t)
	stopTestWorker(t, s)
	now := time.Now().UTC().Add(time.Minute)
	s.automationClock = func() time.Time { return now }
	agent := configuredAgent(t, s, token)
	model := &errorOnceModel{}
	s.modelClient = func(ModelConfig) models.Client { return model }
	automation := createTestAutomation(t, s, token, agent, "manual", "")
	w := request(t, s.Handler(), "POST", "/api/automations/"+automation+"/jobs", `{"idempotency_key":"safe-retry"}`, "127.0.0.1:8765", "", token)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var job struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &job)
	if err := s.runScheduledTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	var status, originalRun string
	if err := s.db.QueryRow(`SELECT status,run_id FROM jobs WHERE id=?`, job.ID).Scan(&status, &originalRun); err != nil {
		t.Fatal(err)
	}
	if status != "queued" || model.calls != 1 {
		t.Fatalf("status=%s model calls=%d", status, model.calls)
	}
	now = now.Add(2 * time.Second)
	if err := s.runScheduledTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	var finalStatus, finalRun string
	if err := s.db.QueryRow(`SELECT status,run_id FROM jobs WHERE id=?`, job.ID).Scan(&finalStatus, &finalRun); err != nil {
		t.Fatal(err)
	}
	if finalStatus != "completed" || originalRun != finalRun || model.calls != 2 {
		t.Fatalf("status=%s same_run=%v model calls=%d", finalStatus, originalRun == finalRun, model.calls)
	}
}

func TestScheduledWriteWaitsForApprovalAndApprovalIsNotDuplicated(t *testing.T) {
	s, token := testApp(t)
	stopTestWorker(t, s)
	agent := configuredAgent(t, s, token)
	writes := 0
	if err := s.registry.Register(engine.Tool{Schema: models.Tool{Name: "calendar.create", Parameters: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false}`)}, Risk: engine.Reversible, Execute: func(context.Context, json.RawMessage, string) (json.RawMessage, error) {
		writes++
		return json.RawMessage(`{"id":"created"}`), nil
	}}); err != nil {
		t.Fatal(err)
	}
	model := &scriptedModel{responses: []models.Response{{Message: models.Message{ToolCalls: []models.ToolCall{{ID: "write-1", Name: "calendar.create", Arguments: json.RawMessage(`{"title":"private value"}`)}}}, Usage: models.Usage{TotalTokens: 4}}}}
	s.modelClient = func(ModelConfig) models.Client { return model }
	automation := createTestAutomation(t, s, token, agent, "manual", "")
	w := request(t, s.Handler(), "POST", "/api/automations/"+automation+"/jobs", `{"idempotency_key":"approval-fixture"}`, "127.0.0.1:8765", "", token)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var job struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &job)
	if err := s.runScheduledTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.runScheduledTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	var status string
	var approvals int
	if err := s.db.QueryRow(`SELECT status FROM jobs WHERE id=?`, job.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM approvals WHERE run_id=(SELECT run_id FROM jobs WHERE id=?)`, job.ID).Scan(&approvals); err != nil {
		t.Fatal(err)
	}
	if status != "awaiting_approval" || approvals != 1 || writes != 0 {
		t.Fatalf("status=%s approvals=%d writes=%d", status, approvals, writes)
	}
	var approvalID string
	if err := s.db.QueryRow(`SELECT id FROM approvals WHERE run_id=(SELECT run_id FROM jobs WHERE id=?)`, job.ID).Scan(&approvalID); err != nil {
		t.Fatal(err)
	}
	w = request(t, s.Handler(), "POST", "/api/approvals/"+approvalID+"/approve", `{}`, "127.0.0.1:8765", "", token)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := s.runScheduledTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Fatalf("approved tool writes=%d, want one", writes)
	}
	if err := s.runScheduledTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Fatal("completed external write was retried")
	}
	w = request(t, s.Handler(), "GET", "/api/audit", "", "127.0.0.1:8765", "", token)
	if strings.Contains(w.Body.String(), "private value") {
		t.Fatal("audit leaked tool arguments")
	}
}

func TestApprovedQueuedWriteStillHonorsLiveToolRevocation(t *testing.T) {
	s, token := testApp(t)
	stopTestWorker(t, s)
	agent := configuredAgent(t, s, token)
	writes := 0
	if err := s.registry.Register(engine.Tool{Schema: models.Tool{Name: "calendar.create", Parameters: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false}`)}, Risk: engine.Reversible, Execute: func(context.Context, json.RawMessage, string) (json.RawMessage, error) {
		writes++
		return json.RawMessage(`{"id":"created"}`), nil
	}}); err != nil {
		t.Fatal(err)
	}
	model := &scriptedModel{responses: []models.Response{{Message: models.Message{ToolCalls: []models.ToolCall{{ID: "write-1", Name: "calendar.create", Arguments: json.RawMessage(`{"title":"title"}`)}}}}}}
	s.modelClient = func(ModelConfig) models.Client { return model }
	automation := createTestAutomation(t, s, token, agent, "manual", "")
	w := request(t, s.Handler(), "POST", "/api/automations/"+automation+"/jobs", `{"idempotency_key":"revocation-fixture"}`, "127.0.0.1:8765", "", token)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := s.runScheduledTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	var jobID, runID, approvalID string
	if err := s.db.QueryRow(`SELECT id,run_id FROM jobs WHERE automation_id=?`, automation).Scan(&jobID, &runID); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT id FROM approvals WHERE run_id=?`, runID).Scan(&approvalID); err != nil {
		t.Fatal(err)
	}
	w = request(t, s.Handler(), "POST", "/api/approvals/"+approvalID+"/approve", `{}`, "127.0.0.1:8765", "", token)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := s.db.Exec(`UPDATE agents SET tools_json='[]' WHERE id=?`, agent); err != nil {
		t.Fatal(err)
	}
	if err := s.runScheduledTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := s.db.QueryRow(`SELECT status FROM jobs WHERE id=?`, jobID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if writes != 0 || status != "failed" {
		t.Fatalf("revoked approved job dispatched: writes=%d status=%s", writes, status)
	}
	var refused int
	if err := s.db.QueryRow(`SELECT count(*) FROM audit WHERE run_id=? AND event_type='tool.refused'`, runID).Scan(&refused); err != nil || refused == 0 {
		t.Fatalf("refusal audit=%d err=%v", refused, err)
	}
}

func TestRestartRecoveryNeverRetriesUncertainExternalWrite(t *testing.T) {
	s, _ := testApp(t)
	stopTestWorker(t, s)
	if _, err := s.db.Exec(`INSERT INTO agents(id,name,enabled,tools_json,created_at,updated_at)VALUES('agent','test',1,'["read"]',?,?)`, now(), now()); err != nil {
		t.Fatal(err)
	}
	state := engine.NewState("jobrun-restart", engine.Config{AgentID: "agent"}, "input")
	state.Status, state.Steps = "running", 1
	state.Pending = []models.ToolCall{{ID: "write", Name: "calendar.create", Arguments: json.RawMessage(`{"title":"x"}`)}}
	if err := s.Save(context.Background(), &state); err != nil {
		t.Fatal(err)
	}
	key := engine.InvocationKey(state.ID, state.Steps, state.Pending[0])
	if _, err := s.db.Exec(`INSERT INTO tool_calls(call_key,run_id,agent_id,tool,status,created_at,updated_at)VALUES(?,?,?,'calendar.create','dispatched',?,?)`, key, state.ID, "agent", now(), now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO automations(id,name,created_at,updated_at)VALUES('a','fixture',?,?)`, now(), now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO jobs(id,automation_id,idempotency_key,status,run_after,created_at,updated_at,run_id,step_key,payload_json)VALUES('j','a','k','running',?,?,?,?,?,'{}')`, now(), now(), now(), state.ID, "job:j:agent-run:v1"); err != nil {
		t.Fatal(err)
	}
	if err := s.recoverRuns(); err != nil {
		t.Fatal(err)
	}
	if err := s.recoverJobs(); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := s.db.QueryRow(`SELECT status FROM jobs WHERE id='j'`).Scan(&status); err != nil || status != "needs_review" {
		t.Fatalf("job status=%s err=%v", status, err)
	}
}

func TestRestartReplaysRecordedToolResultWithoutCallingProviderAgain(t *testing.T) {
	s, token := testApp(t)
	stopTestWorker(t, s)
	agent := configuredAgent(t, s, token)
	config, _, err := s.agentConfig(agent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO automations(id,name,enabled,trigger_type,created_at,updated_at,agent_id,input)VALUES('replay-auto','replay',1,'manual',?,?,?,?)`, now(), now(), agent, "input"); err != nil {
		t.Fatal(err)
	}
	state := engine.NewState("jobrun-replay", config, "input")
	state.Status, state.Steps = "running", 1
	state.Pending = []models.ToolCall{{ID: "already-done", Name: "calendar.create", Arguments: json.RawMessage(`{"title":"x"}`)}}
	if err := s.Save(context.Background(), &state); err != nil {
		t.Fatal(err)
	}
	key := engine.InvocationKey(state.ID, state.Steps, state.Pending[0])
	if _, err := s.db.Exec(`INSERT INTO tool_calls(call_key,run_id,agent_id,tool,status,result_json,created_at,updated_at)VALUES(?,?,?,'calendar.create','succeeded','{}',?,?)`, key, state.ID, agent, now(), now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO jobs(id,automation_id,idempotency_key,status,run_after,created_at,updated_at,run_id,step_key,payload_json)VALUES('replay-job','replay-auto','replay-key','running',?,?,?,?,?,?)`, now(), now(), now(), state.ID, "job:replay-job:agent-run:v1", `{"config":{},"input":"input"}`); err != nil {
		t.Fatal(err)
	}
	if err := s.recoverRuns(); err != nil {
		t.Fatal(err)
	}
	if err := s.recoverJobs(); err != nil {
		t.Fatal(err)
	}
	dispatches := 0
	if err := s.registry.Register(engine.Tool{Schema: models.Tool{Name: "calendar.create", Parameters: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false}`)}, Risk: engine.Reversible, Execute: func(context.Context, json.RawMessage, string) (json.RawMessage, error) {
		dispatches++
		return json.RawMessage(`{}`), nil
	}}); err != nil {
		t.Fatal(err)
	}
	s.modelClient = func(ModelConfig) models.Client { return &scriptedModel{} }
	if err := s.runScheduledTick(context.Background()); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := s.db.QueryRow(`SELECT status FROM jobs WHERE id='replay-job'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || dispatches != 0 {
		t.Fatalf("status=%s duplicate dispatches=%d", status, dispatches)
	}
	var approvals int
	if err := s.db.QueryRow(`SELECT count(*) FROM approvals WHERE run_id=?`, state.ID).Scan(&approvals); err != nil || approvals != 0 {
		t.Fatalf("duplicate approval intents=%d err=%v", approvals, err)
	}
}
