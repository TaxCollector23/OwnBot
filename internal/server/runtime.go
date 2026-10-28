package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"ownbot/internal/engine"
)

func (s *Server) migrateRuntime() error {
	_, err := s.db.Exec(`
	CREATE TABLE IF NOT EXISTS config(key TEXT PRIMARY KEY,value TEXT NOT NULL);
	CREATE TABLE IF NOT EXISTS agent_settings(agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,mcp_servers_json TEXT NOT NULL DEFAULT '[]');
 CREATE TABLE IF NOT EXISTS runs(id TEXT PRIMARY KEY,agent_id TEXT NOT NULL,prompt_version INTEGER NOT NULL,status TEXT NOT NULL,state_json TEXT NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS tool_calls(call_key TEXT PRIMARY KEY,run_id TEXT NOT NULL,agent_id TEXT NOT NULL,tool TEXT NOT NULL,status TEXT NOT NULL,result_json TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL);
 CREATE TRIGGER IF NOT EXISTS audit_no_update BEFORE UPDATE ON audit BEGIN SELECT RAISE(ABORT,'audit is append only'); END;
 CREATE TRIGGER IF NOT EXISTS audit_no_delete BEFORE DELETE ON audit BEGIN SELECT RAISE(ABORT,'audit is append only'); END;
 `)
	if err != nil {
		return err
	}
	// Add durable scheduler fields without discarding or interpreting legacy
	// free-form schedule and pipeline metadata.
	for _, migration := range []struct{ table, column, declaration string }{
		{"automations", "agent_id", "TEXT NOT NULL DEFAULT ''"},
		{"automations", "input", "TEXT NOT NULL DEFAULT ''"},
		{"automations", "interval_minutes", "INTEGER NOT NULL DEFAULT 0"},
		{"automations", "time_utc", "TEXT NOT NULL DEFAULT ''"},
		{"automations", "next_run_at", "TEXT NOT NULL DEFAULT ''"},
		{"automations", "validation_error", "TEXT NOT NULL DEFAULT ''"},
		{"jobs", "run_id", "TEXT NOT NULL DEFAULT ''"},
		{"jobs", "step_key", "TEXT NOT NULL DEFAULT ''"},
		{"jobs", "payload_json", "TEXT NOT NULL DEFAULT ''"},
		{"jobs", "lease_until", "TEXT NOT NULL DEFAULT ''"},
		{"jobs", "lease_owner", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := addColumnIfMissing(s.db, migration.table, migration.column, migration.declaration); err != nil {
			return err
		}
	}
	_, err = s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS jobs_step_key_unique ON jobs(step_key) WHERE step_key <> ''`)
	if err == nil {
		_, err = s.db.Exec(`UPDATE automations SET enabled=0,validation_error=CASE WHEN trigger_type NOT IN ('manual','interval','daily_utc') THEN 'Legacy trigger is not executable; recreate as manual, interval, or daily_utc.' WHEN agent_id='' THEN 'No agent is pinned; edit or recreate this automation with an agent and input.' ELSE validation_error END WHERE validation_error='' AND (trigger_type NOT IN ('manual','interval','daily_utc') OR agent_id='')`)
	}
	return err
}

func addColumnIfMissing(db *sql.DB, table, column, declaration string) error {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var dflt any
		if err := rows.Scan(&cid, &name, &kind, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + declaration)
	return err
}
func eventTx(tx *sql.Tx, kind, actor, run, agent string, details any) error {
	b, err := json.Marshal(details)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO audit(run_id,agent_id,event_type,actor,details_json,created_at)VALUES(?,?,?,?,?,?)`, run, agent, kind, actor, string(b), now())
	return err
}
func (s *Server) Save(ctx context.Context, state *engine.State) error {
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO runs(id,agent_id,prompt_version,status,state_json,created_at,updated_at)VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,state_json=excluded.state_json,updated_at=excluded.updated_at`, state.ID, state.Config.AgentID, state.Config.PromptVersion, state.Status, string(b), state.StartedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), now())
	if err != nil {
		return err
	}
	if err = eventTx(tx, "run.checkpoint", "runtime", state.ID, state.Config.AgentID, map[string]any{"status": state.Status, "prompt_version": state.Config.PromptVersion, "steps": state.Steps, "tainted": state.Tainted}); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.notify("run.updated", state.ID, "", state.Status)
	return nil
}

// Before is the only dispatch gate. Decisions and audit entries commit together.
// A persisted dispatched call is an uncertain external outcome after a crash;
// it is never automatically dispatched again.
func (s *Server) Before(ctx context.Context, inv engine.Invocation, allowed bool) (engine.Decision, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return engine.Decision{}, err
	}
	defer tx.Rollback()
	var status, result string
	// Revocation is effective at the next dispatch even for a paused run. Its
	// original prompt/tools snapshot can narrow permissions, never restore them.
	var enabled, web, mcp int
	var toolNames string
	if e := tx.QueryRow(`SELECT enabled,web_search,mcp_enabled,tools_json FROM agents WHERE id=?`, inv.AgentID).Scan(&enabled, &web, &mcp, &toolNames); e != nil {
		allowed = false
	} else {
		var names []string
		_ = json.Unmarshal([]byte(toolNames), &names)
		found := false
		for _, name := range names {
			if name == inv.Call.Name {
				found = true
			}
		}
		allowed = allowed && enabled != 0 && found
		if inv.Tool.Source == "web_search" && web == 0 {
			allowed = false
		}
		if inv.Tool.Source == "mcp" {
			if mcp == 0 {
				allowed = false
			}
			var raw string
			var servers []string
			if tx.QueryRow(`SELECT mcp_servers_json FROM agent_settings WHERE agent_id=?`, inv.AgentID).Scan(&raw) != nil {
				allowed = false
			} else {
				_ = json.Unmarshal([]byte(raw), &servers)
				found = false
				for _, id := range servers {
					if id == inv.Tool.Server {
						found = true
					}
				}
				allowed = allowed && found
			}
		}
	}
	err = tx.QueryRow(`SELECT status,result_json FROM tool_calls WHERE call_key=?`, inv.Key).Scan(&status, &result)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return engine.Decision{}, err
	}
	details := map[string]any{"tool": inv.Call.Name, "call_key": inv.Key, "args": "[redacted]", "args_sha256": hash(inv.Call.Arguments), "tainted": inv.Tainted}
	finish := func(state, kind string, d engine.Decision) (engine.Decision, error) {
		_, e := tx.Exec(`INSERT INTO tool_calls(call_key,run_id,agent_id,tool,status,created_at,updated_at)VALUES(?,?,?,?,?,?,?) ON CONFLICT(call_key) DO UPDATE SET status=excluded.status,updated_at=excluded.updated_at`, inv.Key, inv.RunID, inv.AgentID, inv.Call.Name, state, now(), now())
		if e != nil {
			return engine.Decision{}, e
		}
		details["decision"] = state
		if e = eventTx(tx, kind, "policy", inv.RunID, inv.AgentID, details); e != nil {
			return engine.Decision{}, e
		}
		if e = tx.Commit(); e != nil {
			return engine.Decision{}, e
		}
		s.notify(kind, inv.RunID, inv.Key, state)
		return d, nil
	}
	if !allowed {
		return finish("refused", "tool.refused", engine.Decision{})
	}
	if status == "succeeded" {
		if !json.Valid([]byte(result)) {
			return engine.Decision{}, errors.New("recorded result invalid")
		}
		return finish("succeeded", "tool.replayed", engine.Decision{Allowed: true, Replayed: true, Result: json.RawMessage(result)})
	}
	if status == "dispatched" || status == "failed" || status == "refused" {
		return finish(status, "tool.refused", engine.Decision{})
	}
	if inv.Tool.Risk != engine.Read || inv.Tool.AlwaysApprove {
		var approval string
		err = tx.QueryRow(`SELECT status FROM approvals WHERE id=? AND run_id=? AND tool=?`, inv.Key, inv.RunID, inv.Call.Name).Scan(&approval)
		if errors.Is(err, sql.ErrNoRows) {
			_, err = tx.Exec(`INSERT INTO approvals(id,agent_id,run_id,tool,args_json,status,reason,requested_at)VALUES(?,?,?,?,?,'pending',?,?)`, inv.Key, inv.AgentID, inv.RunID, inv.Call.Name, string(inv.Call.Arguments), "External write requires approval", now())
			if err != nil {
				return engine.Decision{}, err
			}
			return finish("pending", "tool.approval_requested", engine.Decision{Pending: true})
		}
		if err != nil {
			return engine.Decision{}, err
		}
		switch approval {
		case "pending":
			return finish("pending", "tool.awaiting_approval", engine.Decision{Pending: true})
		case "approved":
			details["approver"] = "local-user"
		case "denied":
			return finish("refused", "tool.refused", engine.Decision{})
		default:
			return engine.Decision{}, errors.New("invalid approval state")
		}
	}
	return finish("dispatched", "tool.dispatched", engine.Decision{Allowed: true, Approver: "local-user"})
}
func (s *Server) After(ctx context.Context, inv engine.Invocation, result json.RawMessage, callErr error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	status := "succeeded"
	stored := string(result)
	if callErr != nil || !json.Valid(result) {
		status = "failed"
		stored = ""
	}
	res, err := tx.Exec(`UPDATE tool_calls SET status=?,result_json=?,updated_at=? WHERE call_key=? AND status='dispatched'`, status, stored, now(), inv.Key)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return errors.New("tool outcome has no matching dispatch")
	}
	if err = eventTx(tx, "tool."+status, "runtime", inv.RunID, inv.AgentID, map[string]any{"tool": inv.Call.Name, "call_key": inv.Key, "result": "[redacted]", "result_sha256": hash(result)}); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.notify("tool."+status, inv.RunID, inv.Key, status)
	return nil
}
func hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func (s *Server) agentConfig(id string) (engine.Config, ModelConfig, error) {
	var c engine.Config
	var m ModelConfig
	var names string
	var enabled, web, mcp int
	err := s.db.QueryRow(`SELECT a.id,a.model,a.enabled,a.web_search,a.mcp_enabled,a.tools_json,a.max_steps,a.max_tokens,a.timeout_seconds,coalesce((SELECT version FROM prompt_versions WHERE agent_id=a.id ORDER BY version DESC LIMIT 1),0),coalesce((SELECT prompt FROM prompt_versions WHERE agent_id=a.id ORDER BY version DESC LIMIT 1),'') FROM agents a WHERE a.id=?`, id).Scan(&c.AgentID, &c.Model, &enabled, &web, &mcp, &names, &c.MaxSteps, &c.MaxTokens, &c.TimeoutSeconds, &c.PromptVersion, &c.Prompt)
	if err != nil {
		return c, m, err
	}
	if enabled == 0 {
		return c, m, errors.New("agent is disabled")
	}
	if c.PromptVersion == 0 {
		return c, m, errors.New("save a system prompt before running")
	}
	_ = json.Unmarshal([]byte(names), &c.Tools)
	c.WebSearch = web != 0
	c.MCP = mcp != 0
	c.ContextTokens = 8192
	rows, err := s.db.Query(`SELECT id,name,provider,base_url,model FROM model_configs WHERE id=? OR model=?`, c.Model, c.Model)
	if err != nil {
		return c, m, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		if err = rows.Scan(&m.ID, &m.Name, &m.Provider, &m.BaseURL, &m.Model); err != nil {
			return c, m, err
		}
	}
	if err = rows.Err(); err != nil {
		return c, m, err
	}
	if count != 1 {
		return c, m, errors.New("choose one configured model by its configuration ID")
	}
	rows.Close()
	var restricted int
	err = s.db.QueryRow(`SELECT restricted FROM model_probes WHERE model_config_id=? ORDER BY id DESC LIMIT 1`, m.ID).Scan(&restricted)
	if errors.Is(err, sql.ErrNoRows) {
		return c, m, errors.New("run the model capability probe before agent runs")
	}
	if err != nil {
		return c, m, err
	}
	c.Restricted = restricted != 0
	c.Model = m.Model
	c.ModelConfigID = m.ID
	var servers string
	err = s.db.QueryRow(`SELECT mcp_servers_json FROM agent_settings WHERE agent_id=?`, id).Scan(&servers)
	if err == nil {
		_ = json.Unmarshal([]byte(servers), &c.MCPServers)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return c, m, err
	}
	return c, m, nil
}
func (s *Server) startRun(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Input string `json:"input"`
	}
	if decode(r, &b) != nil || b.Input == "" || len(b.Input) > 64000 {
		errJSON(w, 400, "input must contain 1 to 64000 characters")
		return
	}
	c, m, err := s.agentConfig(r.PathValue("id"))
	if err != nil {
		errJSON(w, 409, err.Error())
		return
	}
	if c.Restricted {
		errJSON(w, 409, "model is restricted: pass all capability checks before agent runs")
		return
	}
	state := engine.NewState(newID(), c, b.Input)
	s.advanceRun(w, r, &state, m)
}
func (s *Server) advanceRun(w http.ResponseWriter, r *http.Request, state *engine.State, m ModelConfig) {
	lock, _ := s.runLocks.LoadOrStore(state.ID, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	if !mu.TryLock() {
		errJSON(w, 409, "run already in progress")
		return
	}
	defer mu.Unlock()
	if latest, err := s.loadRun(state.ID); err == nil {
		if latest.Status != "awaiting_approval" && latest.Status != "recoverable" && latest.Status != "interrupted" {
			errJSON(w, 409, "run is not awaiting approval")
			return
		}
		*state = latest
	} else if !errors.Is(err, sql.ErrNoRows) {
		errJSON(w, 500, "could not load run")
		return
	}
	runner := engine.Runner{Model: s.modelClient(m), Registry: s.registry, Control: s}
	err := runner.Advance(r.Context(), state)
	if err != nil && state.Status != "failed" {
		errJSON(w, 500, "could not persist run")
		return
	}
	write(w, 200, map[string]any{"id": state.ID, "status": state.Status, "prompt_version": state.Config.PromptVersion, "steps": state.Steps, "tainted": state.Tainted, "output": engine.Summary(*state), "error": state.Error})
}
func (s *Server) loadRun(id string) (engine.State, error) {
	var state engine.State
	var raw string
	err := s.db.QueryRow(`SELECT state_json FROM runs WHERE id=?`, id).Scan(&raw)
	if err != nil {
		return state, err
	}
	err = json.Unmarshal([]byte(raw), &state)
	return state, err
}
func (s *Server) resumeRun(w http.ResponseWriter, r *http.Request) {
	state, err := s.loadRun(r.PathValue("id"))
	if err != nil {
		errJSON(w, 404, "run not found")
		return
	}
	if state.Status != "awaiting_approval" && state.Status != "recoverable" && state.Status != "interrupted" {
		errJSON(w, 409, "run is not awaiting approval")
		return
	}
	// Configuration and prompt stay pinned to the original run. Probe eligibility is
	// rechecked without silently changing the recorded model or system prompt.
	var m ModelConfig
	err = s.db.QueryRow(`SELECT id,name,provider,base_url,model FROM model_configs WHERE id=?`, state.Config.ModelConfigID).Scan(&m.ID, &m.Name, &m.Provider, &m.BaseURL, &m.Model)
	if err != nil {
		errJSON(w, 409, "run model configuration unavailable")
		return
	}
	var restricted int
	if err = s.db.QueryRow(`SELECT restricted FROM model_probes WHERE model_config_id=? ORDER BY id DESC LIMIT 1`, m.ID).Scan(&restricted); err != nil || restricted != 0 {
		errJSON(w, 409, "model is restricted: pass every capability check before resuming")
		return
	}
	s.advanceRun(w, r, &state, m)
}
func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	state, err := s.loadRun(r.PathValue("id"))
	if err != nil {
		errJSON(w, 404, "run not found")
		return
	}
	write(w, 200, state)
}
func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(`SELECT id,agent_id,prompt_version,status,created_at,updated_at FROM runs ORDER BY created_at DESC LIMIT 100`)
	if err != nil {
		errJSON(w, 500, "could not read runs")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, agent, status, created, updated string
		var version int
		if rows.Scan(&id, &agent, &version, &status, &created, &updated) != nil {
			errJSON(w, 500, "could not read runs")
			return
		}
		out = append(out, map[string]any{"id": id, "agent_id": agent, "prompt_version": version, "status": status, "created_at": created, "updated_at": updated})
	}
	write(w, 200, out)
}
func (s *Server) rollbackPrompt(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Version int `json:"version"`
	}
	if decode(r, &b) != nil || b.Version < 1 {
		errJSON(w, 400, "version must be positive")
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		errJSON(w, 500, "could not start rollback")
		return
	}
	defer tx.Rollback()
	var prompt string
	err = tx.QueryRow(`SELECT prompt FROM prompt_versions WHERE agent_id=? AND version=?`, r.PathValue("id"), b.Version).Scan(&prompt)
	if err != nil {
		errJSON(w, 404, "prompt version not found")
		return
	}
	var version int
	err = tx.QueryRow(`SELECT max(version)+1 FROM prompt_versions WHERE agent_id=?`, r.PathValue("id")).Scan(&version)
	if err != nil {
		errJSON(w, 500, "could not roll back prompt")
		return
	}
	_, err = tx.Exec(`INSERT INTO prompt_versions(agent_id,version,prompt,created_at)VALUES(?,?,?,?)`, r.PathValue("id"), version, prompt, now())
	if err == nil {
		err = eventTx(tx, "prompt.rolled_back", "local-user", "", r.PathValue("id"), map[string]any{"source_version": b.Version, "version": version})
	}
	if err != nil || tx.Commit() != nil {
		errJSON(w, 500, "could not roll back prompt")
		return
	}
	write(w, 201, map[string]any{"version": version, "source_version": b.Version})
}

// recoverRuns reconciles durable checkpoints without performing network calls.
// The daemon holds the state-directory OS lock before opening this database.
func (s *Server) recoverRuns() error {
	rows, err := s.db.Query(`SELECT state_json FROM runs WHERE status IN ('running','interrupted')`)
	if err != nil {
		return err
	}
	states := []engine.State{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		var state engine.State
		if err = json.Unmarshal([]byte(raw), &state); err != nil {
			rows.Close()
			return err
		}
		states = append(states, state)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, state := range states {
		if !state.ActiveStartedAt.IsZero() {
			state.ActiveNanos += int64(time.Since(state.ActiveStartedAt))
			state.ActiveStartedAt = time.Time{}
		}
		state.Status, state.Error, err = s.pendingRecoveryStatus(state)
		if err != nil {
			return err
		}
		if err = s.Save(context.Background(), &state); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) pendingRecoveryStatus(state engine.State) (string, string, error) {
	var pending, dispatched, failed int
	if err := s.db.QueryRow(`SELECT count(*) FROM tool_calls WHERE run_id=? AND status='pending'`, state.ID).Scan(&pending); err != nil {
		return "", "", err
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM tool_calls WHERE run_id=? AND status='dispatched'`, state.ID).Scan(&dispatched); err != nil {
		return "", "", err
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM tool_calls WHERE run_id=? AND status IN ('failed','refused')`, state.ID).Scan(&failed); err != nil {
		return "", "", err
	}
	if dispatched > 0 {
		return "outcome_unknown", "tool was dispatched before restart; reconcile its external outcome before retrying", nil
	}
	if failed > 0 {
		return "failed", "tool did not complete", nil
	}
	if pending > 0 {
		return "awaiting_approval", "", nil
	}
	for _, call := range state.Pending {
		key := engine.InvocationKey(state.ID, state.Steps, call)
		var callStatus string
		err := s.db.QueryRow(`SELECT status FROM tool_calls WHERE call_key=?`, key).Scan(&callStatus)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", "", err
		}
		if errors.Is(err, sql.ErrNoRows) {
			// The model generated a tool request but the durable policy gate had
			// not recorded it. Keep it interrupted until policy rechecks it.
			return "interrupted", "pending tool request will be rechecked on resume", nil
		}
	}
	return "recoverable", "daemon restarted; resume to continue", nil
}
