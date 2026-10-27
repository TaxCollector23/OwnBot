package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"ownbot/internal/engine"
	"ownbot/internal/models"
)

type scriptedModel struct {
	responses []models.Response
	seen      []models.Request
}

func (m *scriptedModel) Chat(_ context.Context, r models.Request) (models.Response, error) {
	m.seen = append(m.seen, r)
	if len(m.responses) == 0 {
		return models.Response{Message: models.Message{Content: r.Messages[0].Content}, Usage: models.Usage{TotalTokens: 5}}, nil
	}
	x := m.responses[0]
	m.responses = m.responses[1:]
	return x, nil
}
func (m *scriptedModel) Stream(context.Context, models.Request) (<-chan models.StreamEvent, error) {
	panic("not used")
}
func configuredAgent(t *testing.T, s *Server, token string) string {
	t.Helper()
	w := request(t, s.Handler(), "POST", "/api/models", `{"provider":"ollama","model":"fake"}`, "127.0.0.1:8765", "", token)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var model ModelConfig
	if json.Unmarshal(w.Body.Bytes(), &model) != nil {
		t.Fatal("model decode")
	}
	if _, err := s.db.Exec(`INSERT INTO model_probes(model_config_id,score,json_output,single_tool,tool_choice,argument_accuracy,restricted,probed_at)VALUES(?,100,1,1,1,1,0,?)`, model.ID, now()); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"name": "test", "model": model.ID, "enabled": true, "tools": []string{"calendar.create"}, "prompt": "first system prompt"})
	w = request(t, s.Handler(), "POST", "/api/agents", string(raw), "127.0.0.1:8765", "", token)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var a Agent
	if json.Unmarshal(w.Body.Bytes(), &a) != nil {
		t.Fatal("agent decode")
	}
	return a.ID
}
func TestRunsUseNextPromptVersionAndRollback(t *testing.T) {
	s, token := testApp(t)
	id := configuredAgent(t, s, token)
	m := &scriptedModel{}
	s.modelClient = func(ModelConfig) models.Client { return m }
	run := func(version int, prompt string) {
		t.Helper()
		w := request(t, s.Handler(), "POST", "/api/agents/"+id+"/runs", `{"input":"hi"}`, "127.0.0.1:8765", "", token)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var result struct {
			ID      string `json:"id"`
			Status  string `json:"status"`
			Version int    `json:"prompt_version"`
			Output  string `json:"output"`
		}
		if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Status != "completed" || result.Version != version || result.Output != prompt {
			t.Fatal(w.Body.String())
		}
		state, err := s.loadRun(result.ID)
		if err != nil || state.Config.PromptVersion != version || state.Config.Prompt != prompt {
			t.Fatal("run did not persist prompt snapshot")
		}
	}
	run(1, "first system prompt")
	w := request(t, s.Handler(), "POST", "/api/agents/"+id+"/prompts", `{"prompt":"second system prompt"}`, "127.0.0.1:8765", "", token)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	run(2, "second system prompt")
	w = request(t, s.Handler(), "POST", "/api/agents/"+id+"/prompts/rollback", `{"version":1}`, "127.0.0.1:8765", "", token)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	run(3, "first system prompt")
}
func TestApprovedToolRunsOnceAndAuditRedactsData(t *testing.T) {
	s, token := testApp(t)
	id := configuredAgent(t, s, token)
	writes := 0
	err := s.registry.Register(engine.Tool{Schema: models.Tool{Name: "calendar.create", Parameters: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false}`)}, Risk: engine.Reversible, Execute: func(context.Context, json.RawMessage, string) (json.RawMessage, error) {
		writes++
		return json.RawMessage(`{"id":"external-event","private_content":"do not log this"}`), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	m := &scriptedModel{responses: []models.Response{{Message: models.Message{ToolCalls: []models.ToolCall{{ID: "one", Name: "calendar.create", Arguments: json.RawMessage(`{"title":"private calendar content"}`)}}}, Usage: models.Usage{TotalTokens: 10}}}}
	s.modelClient = func(ModelConfig) models.Client { return m }
	w := request(t, s.Handler(), "POST", "/api/agents/"+id+"/runs", `{"input":"create"}`, "127.0.0.1:8765", "", token)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var result struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if result.Status != "awaiting_approval" || writes != 0 {
		t.Fatal("unapproved write")
	}
	var approval string
	if err = s.db.QueryRow(`SELECT id FROM approvals WHERE run_id=?`, result.ID).Scan(&approval); err != nil {
		t.Fatal(err)
	}
	w = request(t, s.Handler(), "POST", "/api/approvals/"+approval+"/approve", "", "127.0.0.1:8765", "", token)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = request(t, s.Handler(), "POST", "/api/runs/"+result.ID+"/resume", `{}`, "127.0.0.1:8765", "", token)
	if w.Code != 200 || writes != 1 {
		t.Fatal(w.Code, w.Body.String(), writes)
	}
	w = request(t, s.Handler(), "POST", "/api/runs/"+result.ID+"/resume", `{}`, "127.0.0.1:8765", "", token)
	if w.Code != 409 || writes != 1 {
		t.Fatal("run replayed a write")
	}
	w = request(t, s.Handler(), "GET", "/api/audit", "", "127.0.0.1:8765", "", token)
	body := w.Body.String()
	if strings.Contains(body, "private calendar content") || strings.Contains(body, "do not log this") || !strings.Contains(body, "tool.dispatched") || !strings.Contains(body, "tool.succeeded") {
		t.Fatal("audit missing or contains private data")
	}
}
func TestApprovalAuditFailureRollsBackDecision(t *testing.T) {
	s, token := testApp(t)
	w := request(t, s.Handler(), "POST", "/api/approvals", `{"tool":"test"}`, "127.0.0.1:8765", "", token)
	var a Approval
	_ = json.Unmarshal(w.Body.Bytes(), &a)
	_, err := s.db.Exec(`CREATE TRIGGER fail_decision_audit BEFORE INSERT ON audit WHEN NEW.event_type='approval.approved' BEGIN SELECT RAISE(ABORT,'injected storage fault'); END`)
	if err != nil {
		t.Fatal(err)
	}
	w = request(t, s.Handler(), "POST", "/api/approvals/"+a.ID+"/approve", "", "127.0.0.1:8765", "", token)
	if w.Code != http.StatusInternalServerError {
		t.Fatal(w.Code)
	}
	var status string
	if err = s.db.QueryRow(`SELECT status FROM approvals WHERE id=?`, a.ID).Scan(&status); err != nil || status != "pending" {
		t.Fatal("decision committed without audit")
	}
}
func TestDispatchedCrashOutcomeIsNeverRetried(t *testing.T) {
	s, _ := testApp(t)
	if _, err := s.db.Exec(`INSERT INTO agents(id,name,enabled,tools_json,created_at,updated_at)VALUES('agent','test',1,'["read"]',?,?)`, now(), now()); err != nil {
		t.Fatal(err)
	}
	inv := engine.Invocation{RunID: "run", AgentID: "agent", Key: "unique", Tool: engine.Tool{Risk: engine.Read}, Call: models.ToolCall{Name: "read"}}
	first, err := s.Before(context.Background(), inv, true)
	if err != nil || !first.Allowed {
		t.Fatal(err)
	}
	again, err := s.Before(context.Background(), inv, true)
	if err != nil || again.Allowed || again.Pending {
		t.Fatal("uncertain dispatch allowed a retry")
	}
}
func TestAuditAppendOnly(t *testing.T) {
	s, _ := testApp(t)
	s.event("test", "local-user", map[string]any{})
	if _, err := s.db.Exec(`DELETE FROM audit`); err == nil {
		t.Fatal("audit deleted")
	}
	if _, err := s.db.Exec(`UPDATE audit SET actor='other'`); err == nil {
		t.Fatal("audit changed")
	}
}

func TestRecoveryReconcilesPendingAndUncertainCalls(t *testing.T) {
	for _, status := range []string{"pending", "dispatched", "succeeded"} {
		t.Run(status, func(t *testing.T) {
			s, _ := testApp(t)
			state := engine.NewState("recovery", engine.Config{AgentID: "agent"}, "input")
			state.Status = "running"
			state.Steps = 1
			state.Pending = []models.ToolCall{{ID: "call", Name: "write", Arguments: json.RawMessage(`{}`)}}
			if err := s.Save(context.Background(), &state); err != nil {
				t.Fatal(err)
			}
			key := engine.InvocationKey(state.ID, state.Steps, state.Pending[0])
			if _, err := s.db.Exec(`INSERT INTO tool_calls(call_key,run_id,agent_id,tool,status,result_json,created_at,updated_at)VALUES(?,?,?,?,?,'{}',?,?)`, key, state.ID, "agent", "write", status, now(), now()); err != nil {
				t.Fatal(err)
			}
			if err := s.recoverRuns(); err != nil {
				t.Fatal(err)
			}
			got, err := s.loadRun(state.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"pending": "awaiting_approval", "dispatched": "outcome_unknown", "succeeded": "recoverable"}[status]
			if got.Status != want {
				t.Fatalf("got %s want %s", got.Status, want)
			}
		})
	}
}
