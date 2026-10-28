package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"ownbot/internal/engine"
)

// Scheduled runs are deliberately a single, pinned agent invocation. The API
// does not accept executable code or arbitrary pipeline JSON.
type jobSnapshot struct {
	Config engine.Config `json:"config"`
	Input  string        `json:"input"`
}

func (s *Server) jobSnapshot(automationID string) (jobSnapshot, error) {
	var a Automation
	var enabled int
	err := s.db.QueryRow(`SELECT id,name,enabled,trigger_type,schedule,agent_id,input,interval_minutes,time_utc,next_run_at,validation_error FROM automations WHERE id=?`, automationID).
		Scan(&a.ID, &a.Name, &enabled, &a.TriggerType, &a.Schedule, &a.AgentID, &a.Input, &a.IntervalMinutes, &a.TimeUTC, &a.NextRunAt, &a.ValidationError)
	if err != nil {
		return jobSnapshot{}, errors.New("automation not found")
	}
	if enabled == 0 {
		return jobSnapshot{}, errors.New("automation is disabled")
	}
	if a.ValidationError != "" {
		return jobSnapshot{}, errors.New("automation is not executable: " + a.ValidationError)
	}
	if a.TriggerType != "manual" && a.TriggerType != "interval" && a.TriggerType != "daily_utc" {
		return jobSnapshot{}, errors.New("legacy trigger is not executable; recreate as manual, interval, or daily_utc")
	}
	if a.AgentID == "" || strings.TrimSpace(a.Input) == "" {
		return jobSnapshot{}, errors.New("automation needs an agent and input")
	}
	c, _, err := s.agentConfig(a.AgentID)
	if err != nil {
		return jobSnapshot{}, fmt.Errorf("agent is not ready: %w", err)
	}
	if c.Restricted {
		return jobSnapshot{}, errors.New("agent model is restricted and cannot run scheduled agents")
	}
	return jobSnapshot{Config: c, Input: a.Input}, nil
}

func validUTCTime(v string) bool {
	if len(v) != 5 || v[2] != ':' {
		return false
	}
	t, err := time.Parse("15:04", v)
	return err == nil && t.Format("15:04") == v
}

func nextSchedule(a Automation, now time.Time) time.Time {
	now = now.UTC()
	switch a.TriggerType {
	case "interval":
		return now.Add(time.Duration(a.IntervalMinutes) * time.Minute).Truncate(time.Second)
	case "daily_utc":
		parsed, _ := time.Parse("15:04", a.TimeUTC)
		candidate := time.Date(now.Year(), now.Month(), now.Day(), parsed.Hour(), parsed.Minute(), 0, 0, time.UTC)
		if !candidate.After(now) {
			candidate = candidate.Add(24 * time.Hour)
		}
		return candidate
	default:
		return time.Time{}
	}
}

func (s *Server) startAutomationWorker() {
	s.automationCtx, s.automationCancel = context.WithCancel(context.Background())
	s.automationDone = make(chan struct{})
	go func() {
		defer close(s.automationDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-s.automationCtx.Done():
				return
			case <-ticker.C:
				_ = s.automationTick(s.automationCtx)
			}
		}
	}()
}

func (s *Server) stopAutomationWorker() {
	if s.automationCancel == nil {
		return
	}
	s.automationCancel()
	<-s.automationDone
}

func (s *Server) scheduleWake() { /* the bounded one-second scheduler tick observes new work */ }

func (s *Server) automationTick(ctx context.Context) error {
	if err := s.enqueueDueSchedules(ctx); err != nil {
		return err
	}
	return s.runOneJob(ctx)
}

func (s *Server) enqueueDueSchedules(ctx context.Context) error {
	now := s.automationClock().UTC()
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,enabled,trigger_type,schedule,agent_id,input,interval_minutes,time_utc,next_run_at,validation_error FROM automations WHERE enabled=1 AND trigger_type IN ('interval','daily_utc') AND next_run_at<>'' AND next_run_at<=? ORDER BY next_run_at LIMIT 20`, now.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	var due []Automation
	for rows.Next() {
		var a Automation
		var enabled int
		if err = rows.Scan(&a.ID, &a.Name, &enabled, &a.TriggerType, &a.Schedule, &a.AgentID, &a.Input, &a.IntervalMinutes, &a.TimeUTC, &a.NextRunAt, &a.ValidationError); err != nil {
			rows.Close()
			return err
		}
		a.Enabled = enabled != 0
		due = append(due, a)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, a := range due {
		if err := ctx.Err(); err != nil {
			return err
		}
		snap, e := s.jobSnapshot(a.ID)
		if e != nil {
			_, _ = s.db.ExecContext(ctx, `UPDATE automations SET enabled=0,validation_error=?,updated_at=? WHERE id=?`, e.Error(), now.Format(time.RFC3339Nano), a.ID)
			continue
		}
		payload, e := json.Marshal(snap)
		if e != nil {
			return e
		}
		occurrence, e := time.Parse(time.RFC3339Nano, a.NextRunAt)
		if e != nil {
			_, _ = s.db.ExecContext(ctx, `UPDATE automations SET enabled=0,validation_error='Invalid stored next_run_at; fix the schedule.' WHERE id=?`, a.ID)
			continue
		}
		jid := newID()
		step := "job:" + jid + ":agent-run:v1"
		run := "jobrun-" + jid
		idem := "schedule:" + a.ID + ":" + occurrence.UTC().Format(time.RFC3339)
		next := nextSchedule(a, now)
		tx, e := s.db.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		res, e := tx.ExecContext(ctx, `INSERT OR IGNORE INTO jobs(id,automation_id,idempotency_key,status,run_after,created_at,updated_at,run_id,step_key,payload_json) VALUES(?,?,?,'queued',?,?,?,?,?,?)`, jid, a.ID, idem, occurrence.UTC().Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), run, step, string(payload))
		if e == nil {
			_, e = tx.ExecContext(ctx, `UPDATE automations SET next_run_at=?,updated_at=? WHERE id=? AND next_run_at=?`, next.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), a.ID, a.NextRunAt)
		}
		if e != nil {
			_ = tx.Rollback()
			return e
		}
		if e = tx.Commit(); e != nil {
			return e
		}
		if n, _ := res.RowsAffected(); n == 1 {
			s.event("job.queued", "scheduler", map[string]any{"automation_id": a.ID, "job_id": jid, "step_key": step, "idempotency_key": idem})
		}
	}
	return nil
}

func (s *Server) runOneJob(ctx context.Context) error {
	owner := randomLeaseOwner()
	if owner == "" {
		return errors.New("could not create job lease")
	}
	now := s.automationClock().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id, runID, payload string
	err = tx.QueryRowContext(ctx, `SELECT j.id,j.run_id,j.payload_json FROM jobs j WHERE (j.status='queued' AND j.run_after<=?) OR (j.status='awaiting_approval' AND NOT EXISTS(SELECT 1 FROM approvals a WHERE a.run_id=j.run_id AND a.status='pending')) ORDER BY j.run_after,j.created_at LIMIT 1`, now.Format(time.RFC3339Nano)).Scan(&id, &runID, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	until := now.Add(30 * time.Second).Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, `UPDATE jobs SET status='running',attempts=attempts+1,lease_until=?,lease_owner=?,updated_at=? WHERE id=? AND status IN ('queued','awaiting_approval') AND (status<>'queued' OR run_after<=?)`, until, owner, now.Format(time.RFC3339Nano), id, now.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.event("job.attempt_started", "scheduler", map[string]any{"job_id": id, "run_id": runID, "step_key": "job:" + id + ":agent-run:v1"})
	return s.executeJob(ctx, id, runID, payload, owner)
}

func randomLeaseOwner() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

func (s *Server) executeJob(parent context.Context, id, runID, payload, owner string) error {
	var snap jobSnapshot
	if err := json.Unmarshal([]byte(payload), &snap); err != nil {
		return s.finishJob(id, owner, "failed", "stored run snapshot is invalid")
	}
	state, err := s.loadRun(runID)
	if errors.Is(err, sql.ErrNoRows) {
		state = engine.NewState(runID, snap.Config, snap.Input)
		if err = s.Save(parent, &state); err != nil {
			return s.finishJob(id, owner, "failed", "could not persist scheduled run")
		}
	} else if err != nil {
		return s.finishJob(id, owner, "needs_review", "stored run state is unreadable; manual review required")
	}
	switch state.Status {
	case "completed":
		return s.finishJob(id, owner, "completed", "")
	case "outcome_unknown":
		return s.finishJob(id, owner, "needs_review", "external tool outcome is unknown; reconcile before retry")
	case "failed":
		return s.finishJob(id, owner, "failed", state.Error)
	case "awaiting_approval", "recoverable":
	case "queued":
	default:
		return s.finishJob(id, owner, "needs_review", "run is in an unexpected state; manual review required")
	}
	var m ModelConfig
	if err = s.db.QueryRow(`SELECT id,name,provider,base_url,model FROM model_configs WHERE id=?`, state.Config.ModelConfigID).Scan(&m.ID, &m.Name, &m.Provider, &m.BaseURL, &m.Model); err != nil {
		return s.finishJob(id, owner, "failed", "pinned model configuration is unavailable")
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stop := make(chan struct{})
	var heartbeat sync.WaitGroup
	heartbeat.Add(1)
	go func() {
		defer heartbeat.Done()
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				until := s.automationClock().UTC().Add(30 * time.Second).Format(time.RFC3339Nano)
				_, _ = s.db.ExecContext(ctx, `UPDATE jobs SET lease_until=?,updated_at=? WHERE id=? AND status='running' AND lease_owner=?`, until, s.automationClock().UTC().Format(time.RFC3339Nano), id, owner)
			}
		}
	}()
	runner := engine.Runner{Model: s.modelClient(m), Registry: s.registry, Control: s}
	runErr := runner.Advance(ctx, &state)
	close(stop)
	heartbeat.Wait()
	var uncertainCalls int
	if state.Status == "failed" {
		if err := s.db.QueryRow(`SELECT count(*) FROM tool_calls WHERE run_id=? AND status IN ('dispatched','failed')`, state.ID).Scan(&uncertainCalls); err != nil {
			return err
		}
	}
	if uncertainCalls > 0 {
		return s.finishJob(id, owner, "needs_review", "tool outcome may be uncertain; reconcile before any retry")
	}
	if runErr != nil && state.Status == "failed" && state.Error == "model request failed" && parent.Err() == nil {
		if scheduled, retryErr := s.retryModelOnly(id, owner, &state); retryErr == nil && scheduled {
			return nil
		} else if retryErr != nil {
			return retryErr
		}
	}
	status, detail := jobState(state.Status, runErr)
	return s.finishJob(id, owner, status, detail)
}

// A model transport error can be retried only before any tool intent exists.
// Tool dispatches, approvals, and uncertain outcomes stay terminal/reviewable.
func (s *Server) retryModelOnly(id, owner string, state *engine.State) (bool, error) {
	var calls, attempts int
	if err := s.db.QueryRow(`SELECT count(*) FROM tool_calls WHERE run_id=?`, state.ID).Scan(&calls); err != nil {
		return false, err
	}
	if calls != 0 {
		return false, nil
	}
	if err := s.db.QueryRow(`SELECT attempts FROM jobs WHERE id=? AND status='running' AND lease_owner=?`, id, owner).Scan(&attempts); err != nil {
		return false, err
	}
	if attempts >= 3 {
		return false, nil
	}
	state.Status = "recoverable"
	state.Error = "model request failed; safe retry scheduled"
	state.StartedAt = s.automationClock().UTC()
	if err := s.Save(context.Background(), state); err != nil {
		return false, err
	}
	delay := time.Duration(1<<attempts) * time.Second
	if delay > time.Minute {
		delay = time.Minute
	}
	when := s.automationClock().UTC().Add(delay).Format(time.RFC3339Nano)
	res, err := s.db.Exec(`UPDATE jobs SET status='queued',run_after=?,last_error='model request failed; safe retry scheduled',lease_until='',lease_owner='',updated_at=? WHERE id=? AND status='running' AND lease_owner=?`, when, s.automationClock().UTC().Format(time.RFC3339Nano), id, owner)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func jobState(state string, runErr error) (string, string) {
	switch state {
	case "completed":
		return "completed", ""
	case "awaiting_approval":
		return "awaiting_approval", ""
	case "outcome_unknown":
		return "needs_review", "external tool outcome is unknown; reconcile before retry"
	case "failed":
		if runErr == nil {
			return "failed", "scheduled run failed"
		}
		return "failed", runErr.Error()
	default:
		return "needs_review", "run stopped without a terminal state"
	}
}

func (s *Server) finishJob(id, owner, status, detail string) error {
	now := s.automationClock().UTC().Format(time.RFC3339Nano)
	res, err := s.db.Exec(`UPDATE jobs SET status=?,last_error=?,lease_until='',lease_owner='',updated_at=? WHERE id=? AND lease_owner=?`, status, detail, now, id, owner)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		s.event("job."+status, "scheduler", map[string]any{"job_id": id, "status": status})
	}
	return nil
}

func (s *Server) recoverJobs() error {
	rows, err := s.db.Query(`SELECT id,run_id FROM jobs WHERE status='running'`)
	if err != nil {
		return err
	}
	type staleJob struct{ id, runID string }
	var jobs []staleJob
	for rows.Next() {
		var j staleJob
		if err = rows.Scan(&j.id, &j.runID); err != nil {
			rows.Close()
			return err
		}
		jobs = append(jobs, j)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, j := range jobs {
		state, e := s.loadRun(j.runID)
		switch {
		case errors.Is(e, sql.ErrNoRows):
			_, err = s.db.Exec(`UPDATE jobs SET status='queued',lease_until='',lease_owner='',run_after=?,updated_at=? WHERE id=?`, now(), now(), j.id)
		case e != nil:
			_, err = s.db.Exec(`UPDATE jobs SET status='needs_review',last_error='Stored run state is unreadable; manual review required.',lease_until='',lease_owner='',updated_at=? WHERE id=?`, now(), j.id)
		case state.Status == "completed":
			_, err = s.db.Exec(`UPDATE jobs SET status='completed',last_error='',lease_until='',lease_owner='',updated_at=? WHERE id=?`, now(), j.id)
		case state.Status == "awaiting_approval":
			_, err = s.db.Exec(`UPDATE jobs SET status='awaiting_approval',lease_until='',lease_owner='',updated_at=? WHERE id=?`, now(), j.id)
		case state.Status == "outcome_unknown":
			_, err = s.db.Exec(`UPDATE jobs SET status='needs_review',last_error='External tool outcome is unknown; reconcile before retry.',lease_until='',lease_owner='',updated_at=? WHERE id=?`, now(), j.id)
		case state.Status == "failed":
			var uncertain int
			if err = s.db.QueryRow(`SELECT count(*) FROM tool_calls WHERE run_id=? AND status IN ('dispatched','failed')`, state.ID).Scan(&uncertain); err != nil {
				return err
			}
			if uncertain > 0 {
				_, err = s.db.Exec(`UPDATE jobs SET status='needs_review',last_error='Tool outcome may be uncertain; reconcile before any retry.',lease_until='',lease_owner='',updated_at=? WHERE id=?`, now(), j.id)
			} else {
				_, err = s.db.Exec(`UPDATE jobs SET status='failed',last_error='Scheduled run failed before restart.',lease_until='',lease_owner='',updated_at=? WHERE id=?`, now(), j.id)
			}
		case state.Status == "recoverable":
			_, err = s.db.Exec(`UPDATE jobs SET status='queued',last_error='',lease_until='',lease_owner='',run_after=?,updated_at=? WHERE id=?`, now(), now(), j.id)
		case state.Status == "queued":
			_, err = s.db.Exec(`UPDATE jobs SET status='queued',last_error='',lease_until='',lease_owner='',run_after=?,updated_at=? WHERE id=?`, now(), now(), j.id)
		default:
			_, err = s.db.Exec(`UPDATE jobs SET status='needs_review',last_error='Run state cannot be safely resumed; manual review required.',lease_until='',lease_owner='',updated_at=? WHERE id=?`, now(), j.id)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Support direct scheduler checks in deterministic tests without sleeping.
func (s *Server) runScheduledTick(ctx context.Context) error { return s.automationTick(ctx) }
