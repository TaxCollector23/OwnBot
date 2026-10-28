// Package engine runs bounded agents through one policy and audit boundary.
package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"ownbot/internal/models"
)

type Risk string

const (
	Read         Risk = "read"
	Reversible   Risk = "write_reversible"
	Irreversible Risk = "write_irreversible"
)

type Tool struct {
	Schema        models.Tool
	Risk          Risk
	Source        string
	Server        string
	Untrusted     bool
	AlwaysApprove bool
	Execute       func(context.Context, json.RawMessage, string) (json.RawMessage, error)
}
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewRegistry() *Registry { return &Registry{tools: map[string]Tool{}} }
func (r *Registry) Register(t Tool) error {
	if t.Schema.Name == "" || t.Execute == nil {
		return errors.New("tool name and handler required")
	}
	if t.Risk != Read && t.Risk != Reversible && t.Risk != Irreversible {
		return errors.New("invalid tool risk")
	}
	if !json.Valid(t.Schema.Parameters) {
		return errors.New("invalid tool schema")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[t.Schema.Name]; ok {
		return errors.New("tool already registered")
	}
	r.tools[t.Schema.Name] = t
	return nil
}
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

func (r *Registry) RemoveServer(server string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, tool := range r.tools {
		if tool.Source == "mcp" && tool.Server == server {
			delete(r.tools, name)
		}
	}
}
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.tools))
	for _, tool := range r.tools {
		out = append(out, tool)
	}
	return out
}

type dispatchContextKey struct{}
type dispatchProof struct {
	name   string
	digest string
}

// IsAuthorizedDispatch verifies that the runner committed its dispatch gate for
// these exact arguments. Transport adapters cannot mint this capability.
func IsAuthorizedDispatch(ctx context.Context, name string, args json.RawMessage) bool {
	proof, ok := ctx.Value(dispatchContextKey{}).(dispatchProof)
	sum := sha256.Sum256(args)
	return ok && proof.name == name && proof.digest == hex.EncodeToString(sum[:])
}

type Config struct {
	AgentID        string   `json:"agent_id"`
	Model          string   `json:"model"`
	ModelConfigID  string   `json:"model_config_id"`
	Prompt         string   `json:"prompt"`
	PromptVersion  int      `json:"prompt_version"`
	Tools          []string `json:"tools"`
	MCPServers     []string `json:"mcp_servers"`
	WebSearch      bool     `json:"web_search"`
	MCP            bool     `json:"mcp"`
	MaxSteps       int      `json:"max_steps"`
	MaxTokens      int      `json:"max_tokens"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	ContextTokens  int      `json:"context_tokens"`
	Restricted     bool     `json:"restricted"`
}

func (c Config) Allowed(t Tool) bool {
	found := false
	for _, name := range c.Tools {
		if name == t.Schema.Name {
			found = true
			break
		}
	}
	if !found {
		return false
	}
	if t.Source == "web_search" && !c.WebSearch {
		return false
	}
	if t.Source == "mcp" {
		if !c.MCP {
			return false
		}
		found = false
		for _, id := range c.MCPServers {
			if id == t.Server {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

type State struct {
	ID              string            `json:"id"`
	Config          Config            `json:"config"`
	Status          string            `json:"status"`
	Messages        []models.Message  `json:"messages"`
	Pending         []models.ToolCall `json:"pending,omitempty"`
	Steps           int               `json:"steps"`
	Tokens          int               `json:"tokens"`
	Tainted         bool              `json:"tainted"`
	StartedAt       time.Time         `json:"started_at"`
	ActiveNanos     int64             `json:"active_nanos,omitempty"`
	ActiveStartedAt time.Time         `json:"active_started_at,omitempty"`
	Error           string            `json:"error,omitempty"`
}
type Invocation struct {
	RunID   string
	AgentID string
	Tool    Tool
	Call    models.ToolCall
	Key     string
	Tainted bool
}
type Decision struct {
	Allowed  bool
	Pending  bool
	Approver string
	Replayed bool
	Result   json.RawMessage
}

// Control must durably record every decision before a tool is dispatched. Save
// checkpoints local run data; audit records must redact arguments and results.
type Control interface {
	Save(context.Context, *State) error
	Before(context.Context, Invocation, bool) (Decision, error)
	After(context.Context, Invocation, json.RawMessage, error) error
}

type Runner struct {
	Model    models.Client
	Registry *Registry
	Control  Control
}

func NewState(id string, c Config, input string) State {
	return State{ID: id, Config: c, Status: "queued", StartedAt: time.Now().UTC(), Messages: []models.Message{{Role: "system", Content: c.Prompt}, {Role: "user", Content: input}}}
}
func (r *Runner) Advance(ctx context.Context, s *State) (retErr error) {
	if r.Model == nil || r.Registry == nil || r.Control == nil {
		return errors.New("runner is not configured")
	}
	if s.Status != "queued" && s.Status != "awaiting_approval" && s.Status != "recoverable" && s.Status != "interrupted" {
		return errors.New("run cannot be resumed in its current state")
	}
	c := s.Config
	if c.Restricted {
		return r.fail(ctx, s, "model is restricted: pass every capability check before agent runs")
	}
	if c.MaxSteps < 1 || c.MaxTokens < 1 || c.TimeoutSeconds < 1 {
		return r.fail(ctx, s, "run limits must be positive")
	}
	limit := time.Duration(c.TimeoutSeconds) * time.Second
	activeElapsed := time.Duration(s.ActiveNanos)
	if !s.ActiveStartedAt.IsZero() {
		// A persisted active segment means the previous process stopped mid-run.
		// Account it conservatively through recovery, then clear it before waiting
		// for any user approval.
		activeElapsed += time.Since(s.ActiveStartedAt)
	}
	if activeElapsed >= limit {
		s.ActiveNanos = int64(activeElapsed)
		s.ActiveStartedAt = time.Time{}
		return r.fail(ctx, s, "run active-time limit reached")
	}
	segmentStart := time.Now()
	s.ActiveStartedAt = segmentStart.UTC()
	s.ActiveNanos = int64(activeElapsed)
	ctx, cancel := context.WithTimeout(ctx, limit-activeElapsed)
	defer cancel()
	defer func() {
		s.ActiveNanos += int64(time.Since(segmentStart))
		s.ActiveStartedAt = time.Time{}
		if err := r.Control.Save(context.WithoutCancel(ctx), s); err != nil {
			retErr = errors.Join(retErr, err)
		}
	}()
	s.Status = "running"
	s.Error = ""
	if err := r.Control.Save(ctx, s); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			if errors.Is(err, context.Canceled) {
				return r.interrupted(context.WithoutCancel(ctx), s)
			}
			return r.fail(context.WithoutCancel(ctx), s, "run time limit reached")
		}
		if len(s.Pending) > 0 {
			call := s.Pending[0]
			tool, exists := r.Registry.Get(call.Name)
			allowed := exists && c.Allowed(tool)
			if !exists {
				tool = Tool{Schema: models.Tool{Name: call.Name}, Risk: Irreversible}
			}
			inv := Invocation{RunID: s.ID, AgentID: c.AgentID, Tool: tool, Call: call, Key: InvocationKey(s.ID, s.Steps, call), Tainted: s.Tainted}
			if allowed {
				if err := models.ValidateJSONSchema(tool.Schema.Parameters, call.Arguments); err != nil {
					allowed = false
				}
			}
			d, err := r.Control.Before(ctx, inv, allowed)
			if err != nil {
				if errors.Is(ctx.Err(), context.Canceled) {
					return r.interrupted(context.WithoutCancel(ctx), s)
				}
				return r.fail(context.WithoutCancel(ctx), s, "could not record tool decision")
			}
			if !allowed || (!d.Allowed && !d.Pending) {
				return r.fail(ctx, s, "tool call refused by policy")
			}
			if d.Pending {
				s.Status = "awaiting_approval"
				return r.Control.Save(ctx, s)
			}
			result := d.Result
			var callErr error
			if !d.Replayed {
				sum := sha256.Sum256(call.Arguments)
				dispatchCtx := context.WithValue(ctx, dispatchContextKey{}, dispatchProof{name: call.Name, digest: hex.EncodeToString(sum[:])})
				result, callErr = tool.Execute(dispatchCtx, call.Arguments, inv.Key)
			}
			// Once dispatched, an audit failure is an uncertain external outcome. Never retry automatically.
			if !d.Replayed {
				if err := r.Control.After(context.WithoutCancel(ctx), inv, result, callErr); err != nil {
					return r.fail(context.WithoutCancel(ctx), s, "tool outcome could not be recorded; reconcile before retrying")
				}
			}
			if callErr != nil {
				return r.fail(context.WithoutCancel(ctx), s, "tool failed; inspect its audited outcome")
			}
			if !json.Valid(result) {
				return r.fail(ctx, s, "tool returned invalid JSON")
			}
			if tool.Untrusted || tool.Source == "mcp" || tool.Source == "web_search" {
				s.Tainted = true
			}
			s.Messages = append(s.Messages, models.Message{Role: "tool", ToolCallID: call.ID, Name: call.Name, Content: string(result)})
			s.Pending = s.Pending[1:]
			if err := r.Control.Save(ctx, s); err != nil {
				return err
			}
			continue
		}
		if s.Steps >= c.MaxSteps {
			return r.fail(ctx, s, "maximum model steps reached")
		}
		if s.Tokens >= c.MaxTokens {
			return r.fail(ctx, s, "token budget exhausted")
		}
		tools := []models.Tool{}
		for _, name := range c.Tools {
			if t, ok := r.Registry.Get(name); ok && c.Allowed(t) {
				tools = append(tools, t.Schema)
			}
		}
		messages, trimErr := trim(s.Messages, c.ContextTokens)
		if trimErr != nil {
			return r.fail(ctx, s, "trusted input or tool arguments exceed the context limit")
		}
		response, err := r.Model.Chat(ctx, models.Request{Model: c.Model, Messages: messages, Tools: tools, MaxTokens: c.MaxTokens - s.Tokens})
		if err != nil {
			if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
				return r.interrupted(context.WithoutCancel(ctx), s)
			}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return r.fail(context.WithoutCancel(ctx), s, "run active-time limit reached")
			}
			return r.fail(context.WithoutCancel(ctx), s, "model request failed")
		}
		s.Steps++
		used := response.Usage.TotalTokens
		if used <= 0 {
			raw, _ := json.Marshal(messages)
			out, _ := json.Marshal(response.Message)
			used = (len(raw) + len(out) + 3) / 4
		}
		s.Tokens += used
		if s.Tokens > c.MaxTokens {
			return r.fail(ctx, s, "token budget exhausted")
		}
		response.Message.Role = "assistant"
		seen := map[string]bool{}
		for _, call := range response.Message.ToolCalls {
			if call.ID == "" || call.Name == "" || seen[call.ID] {
				return r.fail(ctx, s, "model returned invalid tool-call identities")
			}
			seen[call.ID] = true
		}
		s.Messages = append(s.Messages, response.Message)
		s.Pending = append([]models.ToolCall(nil), response.Message.ToolCalls...)
		if len(s.Pending) == 0 {
			s.Status = "completed"
			return r.Control.Save(ctx, s)
		}
		if err := r.Control.Save(ctx, s); err != nil {
			return err
		}
	}
}
func (r *Runner) interrupted(ctx context.Context, s *State) error {
	if len(s.Pending) == 0 {
		s.Status = "recoverable"
		s.Error = "run interrupted before tool dispatch; it can resume safely"
	} else {
		// The model has a pending tool request. Keep it distinct until the
		// durable policy journal proves whether it needs approval or review.
		s.Status = "interrupted"
		s.Error = "run interrupted with a pending tool request; policy will be rechecked on resume"
	}
	if err := r.Control.Save(ctx, s); err != nil {
		return err
	}
	return context.Canceled
}
func (r *Runner) fail(ctx context.Context, s *State, reason string) error {
	s.Status = "failed"
	s.Error = reason
	if err := r.Control.Save(ctx, s); err != nil {
		return err
	}
	return errors.New(reason)
}
func InvocationKey(run string, step int, c models.ToolCall) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s:%s:%s", run, step, c.ID, c.Name, c.Arguments)))
	return hex.EncodeToString(h[:])
}

// Trim whole conversation turns; preserve the system prompt and all tool results
// belonging to a retained assistant turn, so the adapter never sees orphan calls.
func trim(messages []models.Message, budget int) ([]models.Message, error) {
	if budget <= 0 {
		budget = 8192
	}
	limit := budget * 4
	out := append([]models.Message(nil), messages...)
	size := func() int { b, _ := json.Marshal(out); return len(b) }
	lastUser := 1
	for i, m := range out {
		if m.Role == "user" {
			lastUser = i
		}
	}
	if lastUser > 1 {
		out = append(out[:1], out[lastUser:]...)
	}
	for i := 1; i < len(out) && size() > limit; i++ {
		if out[i].Role == "tool" {
			out[i].Content = `{"truncated":true}`
		}
	}
	for size() > limit && len(out) > 3 {
		next := 3
		for next < len(out) && out[next].Role != "assistant" {
			next++
		}
		if next == len(out) {
			break
		}
		out = append(out[:2], out[next:]...)
	}
	if size() > limit {
		return nil, errors.New("context limit exceeded")
	}
	return out, nil
}
func Summary(s State) string {
	if len(s.Messages) == 0 {
		return ""
	}
	m := s.Messages[len(s.Messages)-1]
	if m.Role == "assistant" {
		return strings.TrimSpace(m.Content)
	}
	return ""
}
