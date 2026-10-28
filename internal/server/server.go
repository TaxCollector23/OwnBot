package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
	"ownbot/internal/engine"
	"ownbot/internal/events"
	"ownbot/internal/mcp"
	"ownbot/internal/models"
	"ownbot/web"
)

type Server struct {
	db               *sql.DB
	token            string
	mux              *http.ServeMux
	registry         *engine.Registry
	runLocks         sync.Map
	modelClient      func(ModelConfig) models.Client
	hosts            *mcp.HostManager
	mcpMu            sync.Mutex
	events           *events.Hub
	lanMu            sync.RWMutex
	lanAuthority     string
	lanFingerprint   string
	pairMu           sync.Mutex
	pairAttempts     map[string]pairAttempt
	shutdownMu       sync.RWMutex
	shutdown         func()
	localAuthMu      sync.Mutex
	bootstrapTickets map[string]time.Time
	localSessions    map[string]time.Time
	automationCtx    context.Context
	automationCancel context.CancelFunc
	automationDone   chan struct{}
	automationClock  func() time.Time
}

func New(dir string) (*Server, error) {
	if err := privateDirectory(dir); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "ownbot.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;`); err != nil {
		db.Close()
		return nil, err
	}
	s := &Server{db: db, mux: http.NewServeMux(), registry: engine.NewRegistry(), hosts: mcp.NewHostManager(), events: events.NewHub(nil), pairAttempts: make(map[string]pairAttempt), bootstrapTickets: make(map[string]time.Time), localSessions: make(map[string]time.Time), automationClock: time.Now}
	if err = s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	if err = s.migrateRuntime(); err != nil {
		db.Close()
		return nil, err
	}
	if err = s.recoverRuns(); err != nil {
		db.Close()
		return nil, err
	}
	if err = s.recoverJobs(); err != nil {
		db.Close()
		return nil, err
	}
	if err = s.loadSearch(); err != nil {
		db.Close()
		return nil, err
	}
	s.token, err = tokenFile(dir)
	if err != nil {
		db.Close()
		return nil, err
	}
	s.routes()
	s.events.SetPrincipalValidator(s.validEventPrincipal)
	s.modelClient = func(c ModelConfig) models.Client {
		return &models.OpenAICompatible{BaseURL: c.BaseURL, Client: &http.Client{Timeout: 4 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	}
	s.startAutomationWorker()
	return s, nil
}
func (s *Server) Close() error {
	s.stopAutomationWorker()
	s.events.Close()
	return errors.Join(s.hosts.Close(), s.db.Close())
}
func (s *Server) Handler() http.Handler { return s.security(s.mux) }
func tokenFile(dir string) (string, error) {
	p := filepath.Join(dir, "api.token")
	if err := privateToken(p); err != nil {
		return "", err
	}
	b, e := os.ReadFile(p)
	if e == nil {
		t := strings.TrimSpace(string(b))
		if len(t) != 64 {
			return "", errors.New("api.token is malformed; remove it to generate a new token")
		}
		if _, e := hex.DecodeString(t); e != nil {
			return "", errors.New("api.token is malformed; remove it to generate a new token")
		}
		return t, nil
	}
	if !errors.Is(e, os.ErrNotExist) {
		return "", e
	}
	b = make([]byte, 32)
	if _, e = rand.Read(b); e != nil {
		return "", e
	}
	t := hex.EncodeToString(b)
	f, e := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return "", e
	}
	if _, e = io.WriteString(f, t+"\n"); e != nil {
		f.Close()
		return "", e
	}
	return t, f.Close()
}
func (s *Server) migrate() error {
	if _, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS mcp_servers(id TEXT PRIMARY KEY,name TEXT NOT NULL,enabled INTEGER NOT NULL DEFAULT 0,transport TEXT NOT NULL DEFAULT 'stdio',endpoint TEXT NOT NULL DEFAULT '',command TEXT NOT NULL,args_json TEXT NOT NULL DEFAULT '[]',working_directory TEXT NOT NULL DEFAULT '',environment_allowlist_json TEXT NOT NULL DEFAULT '[]',allowed_tools_json TEXT NOT NULL DEFAULT '[]',read_tools_json TEXT NOT NULL DEFAULT '[]',write_tools_json TEXT NOT NULL DEFAULT '[]',request_timeout_seconds INTEGER NOT NULL DEFAULT 30,created_at TEXT NOT NULL,updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS model_configs(id TEXT PRIMARY KEY,name TEXT NOT NULL DEFAULT '',provider TEXT NOT NULL,base_url TEXT NOT NULL,model TEXT NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS model_probes(id INTEGER PRIMARY KEY AUTOINCREMENT,model_config_id TEXT NOT NULL REFERENCES model_configs(id) ON DELETE CASCADE,score INTEGER NOT NULL,json_output INTEGER NOT NULL,single_tool INTEGER NOT NULL,tool_choice INTEGER NOT NULL,argument_accuracy INTEGER NOT NULL,restricted INTEGER NOT NULL,probed_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS agents(id TEXT PRIMARY KEY,name TEXT NOT NULL,model TEXT NOT NULL DEFAULT '',enabled INTEGER NOT NULL DEFAULT 1,web_search INTEGER NOT NULL DEFAULT 0,mcp_enabled INTEGER NOT NULL DEFAULT 0,tools_json TEXT NOT NULL DEFAULT '[]',max_steps INTEGER NOT NULL DEFAULT 8,max_tokens INTEGER NOT NULL DEFAULT 4096,timeout_seconds INTEGER NOT NULL DEFAULT 120,created_at TEXT NOT NULL,updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS prompt_versions(id INTEGER PRIMARY KEY AUTOINCREMENT,agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,version INTEGER NOT NULL,prompt TEXT NOT NULL,created_at TEXT NOT NULL,UNIQUE(agent_id,version));
CREATE TABLE IF NOT EXISTS approvals(id TEXT PRIMARY KEY,agent_id TEXT NOT NULL DEFAULT '',run_id TEXT NOT NULL DEFAULT '',tool TEXT NOT NULL,args_json TEXT NOT NULL,status TEXT NOT NULL DEFAULT 'pending',reason TEXT NOT NULL DEFAULT '',requested_at TEXT NOT NULL,decided_at TEXT NOT NULL DEFAULT '',approver TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS audit(id INTEGER PRIMARY KEY AUTOINCREMENT,run_id TEXT NOT NULL DEFAULT '',agent_id TEXT NOT NULL DEFAULT '',event_type TEXT NOT NULL,actor TEXT NOT NULL DEFAULT '',details_json TEXT NOT NULL,created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS automations(id TEXT PRIMARY KEY,name TEXT NOT NULL,enabled INTEGER NOT NULL DEFAULT 0,trigger_type TEXT NOT NULL DEFAULT 'manual',schedule TEXT NOT NULL DEFAULT '',pipeline_json TEXT NOT NULL DEFAULT '[]',created_at TEXT NOT NULL,updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS jobs(id TEXT PRIMARY KEY,automation_id TEXT NOT NULL REFERENCES automations(id) ON DELETE CASCADE,idempotency_key TEXT NOT NULL UNIQUE,status TEXT NOT NULL DEFAULT 'queued',attempts INTEGER NOT NULL DEFAULT 0,run_after TEXT NOT NULL,last_error TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS paired_devices(id TEXT PRIMARY KEY,name TEXT NOT NULL,token_hash TEXT NOT NULL UNIQUE,created_at TEXT NOT NULL,revoked_at TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS device_pairings(secret_hash TEXT PRIMARY KEY,expires_at TEXT NOT NULL,created_at TEXT NOT NULL,used_at TEXT NOT NULL DEFAULT '');
`); err != nil {
		return err
	}
	// SQLite CREATE TABLE IF NOT EXISTS does not add columns to an earlier schema.
	// Apply the MCP transport migration atomically to existing databases.
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`PRAGMA table_info(mcp_servers)`)
	if err != nil {
		return err
	}
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var dflt any
		if err = rows.Scan(&cid, &name, &kind, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		columns[name] = true
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if !columns["transport"] {
		if _, err = tx.Exec(`ALTER TABLE mcp_servers ADD COLUMN transport TEXT NOT NULL DEFAULT 'stdio'`); err != nil {
			return err
		}
	}
	if !columns["endpoint"] {
		if _, err = tx.Exec(`ALTER TABLE mcp_servers ADD COLUMN endpoint TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/status", s.status)
	s.mux.HandleFunc("GET /api/access", s.access)
	s.mux.HandleFunc("POST /api/access/bootstrap", s.createLocalBootstrap)
	s.mux.HandleFunc("POST /api/access/bootstrap/exchange", s.exchangeLocalBootstrap)
	s.mux.HandleFunc("GET /api/devices", s.devices)
	s.mux.HandleFunc("POST /api/devices/pairing", s.createPairing)
	s.mux.HandleFunc("POST /api/devices/pair", s.pairDevice)
	s.mux.HandleFunc("DELETE /api/devices/{id}", s.revokeDevice)
	s.mux.HandleFunc("POST /api/daemon/shutdown", s.shutdownDaemon)
	s.mux.Handle("GET /api/events", s.events)
	s.mux.HandleFunc("POST /api/events/ticket", s.eventTicket)
	s.mux.HandleFunc("GET /api/mcp/servers", s.mcpServers)
	s.mux.HandleFunc("POST /api/mcp/servers", s.createMCPServer)
	s.mux.HandleFunc("DELETE /api/mcp/servers/{id}", s.revokeMCPServer)
	s.mux.HandleFunc("POST /api/mcp/servers/{id}/connect", s.connectMCP)
	s.mux.HandleFunc("POST /api/mcp/servers/{id}/disconnect", s.disconnectMCP)
	s.mux.HandleFunc("GET /api/tools", s.listTools)
	s.mux.HandleFunc("GET /api/models", s.models)
	s.mux.HandleFunc("POST /api/models", s.createModel)
	s.mux.HandleFunc("POST /api/models/{id}/probe", s.probeModel)
	s.mux.HandleFunc("GET /api/agents", s.agents)
	s.mux.HandleFunc("POST /api/agents", s.createAgent)
	s.mux.HandleFunc("PATCH /api/agents/{id}", s.updateAgent)
	s.mux.HandleFunc("GET /api/agents/{id}/prompts", s.prompts)
	s.mux.HandleFunc("POST /api/agents/{id}/prompts", s.addPrompt)
	s.mux.HandleFunc("GET /api/approvals", s.approvals)
	s.mux.HandleFunc("POST /api/approvals", s.createApproval)
	s.mux.HandleFunc("POST /api/approvals/{id}/approve", s.decide(true))
	s.mux.HandleFunc("POST /api/approvals/{id}/deny", s.decide(false))
	s.mux.HandleFunc("GET /api/audit", s.audit)
	s.mux.HandleFunc("GET /api/automations", s.automations)
	s.mux.HandleFunc("POST /api/automations", s.createAutomation)
	s.mux.HandleFunc("POST /api/automations/{id}/jobs", s.enqueue)
	s.mux.HandleFunc("GET /api/jobs", s.jobs)
	s.mux.HandleFunc("GET /api/search", s.searchConfig)
	s.mux.HandleFunc("POST /api/search", s.configureSearch)
	s.mux.HandleFunc("POST /api/agents/{id}/runs", s.startRun)
	s.mux.HandleFunc("GET /api/runs", s.listRuns)
	s.mux.HandleFunc("GET /api/runs/{id}", s.getRun)
	s.mux.HandleFunc("POST /api/runs/{id}/resume", s.resumeRun)
	s.mux.HandleFunc("POST /api/agents/{id}/prompts/rollback", s.rollbackPrompt)
	s.mux.HandleFunc("/", s.static)
}
func loopbackHost(h string) bool {
	host, _, e := net.SplitHostPort(h)
	if e != nil {
		host = h
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
func (s *Server) security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if !loopbackHost(r.Host) {
				http.Error(w, "loopback host required", 403)
				return
			}
			if (r.Method != "GET" && r.Method != "HEAD") || r.URL.Path == "/api/events" {
				if o := r.Header.Get("Origin"); o != "" {
					u, e := url.Parse(o)
					if e != nil || !loopbackHost(u.Host) || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") || u.Path != "" {
						http.Error(w, "origin rejected", 403)
						return
					}
				}
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/status" {
			if r.URL.Path == "/api/access/bootstrap/exchange" && r.Method == http.MethodPost {
				if !loopbackPeer(r.RemoteAddr) {
					http.Error(w, "loopback peer required", http.StatusForbidden)
					return
				}
				origin := r.Header.Get("Origin")
				if origin == "" || !matchingLoopbackOrigin(origin, r.Host, r.TLS != nil) {
					http.Error(w, "same-origin request required", http.StatusForbidden)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			if r.URL.Path == "/api/events" && r.Method == "GET" {
				next.ServeHTTP(w, r)
				return
			}
			header := r.Header.Get("Authorization")
			a := strings.TrimPrefix(header, "Bearer ")
			isMaster := strings.HasPrefix(header, "Bearer ") && subtle.ConstantTimeCompare([]byte(a), []byte(s.token)) == 1
			if r.URL.Path == "/api/access/bootstrap" && !isMaster {
				http.Error(w, "unauthorized", 401)
				return
			}
			if r.URL.Path == "/api/daemon/shutdown" && !isMaster {
				http.Error(w, "unauthorized", 401)
				return
			}
			if !isMaster && (!strings.HasPrefix(header, "Bearer ") || !loopbackPeer(r.RemoteAddr) || !s.validLocalSession(a)) {
				http.Error(w, "unauthorized", 401)
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), principalContextKey{}, "admin"))
		}
		next.ServeHTTP(w, r)
	})
}
func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func write(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func errJSON(w http.ResponseWriter, code int, msg string) {
	write(w, code, map[string]string{"error": msg})
}
func decode(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON object")
	}
	return nil
}
func newID() string { b := make([]byte, 16); _, _ = rand.Read(b); return hex.EncodeToString(b) }
func (s *Server) event(kind, actor string, details any) {
	b, e := json.Marshal(details)
	if e == nil {
		_, _ = s.db.Exec(`INSERT INTO audit(event_type,actor,details_json,created_at)VALUES(?,?,?,?)`, kind, actor, string(b), now())
	}
}
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	var a, p, m, j int
	_ = s.db.QueryRow(`SELECT count(*) FROM agents`).Scan(&a)
	_ = s.db.QueryRow(`SELECT count(*) FROM approvals WHERE status='pending'`).Scan(&p)
	_ = s.db.QueryRow(`SELECT count(*) FROM automations WHERE enabled=1`).Scan(&m)
	_ = s.db.QueryRow(`SELECT count(*) FROM jobs WHERE status IN('queued','retry')`).Scan(&j)
	write(w, 200, map[string]any{"ok": true, "service": "ownbot", "version": "development", "agents": a, "pending_approvals": p, "active_automations": m, "queued_jobs": j, "api_token_required": true})
}

type Agent struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Model          string   `json:"model"`
	Enabled        bool     `json:"enabled"`
	WebSearch      bool     `json:"web_search"`
	MCPEnabled     bool     `json:"mcp_enabled"`
	Tools          []string `json:"tools"`
	MaxSteps       int      `json:"max_steps"`
	MaxTokens      int      `json:"max_tokens"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
	PromptVersion  int      `json:"prompt_version"`
	Prompt         string   `json:"prompt,omitempty"`
}

func (s *Server) agents(w http.ResponseWriter, r *http.Request) {
	rows, e := s.db.Query(`SELECT a.id,a.name,a.model,a.enabled,a.web_search,a.mcp_enabled,a.tools_json,a.max_steps,a.max_tokens,a.timeout_seconds,a.created_at,a.updated_at,coalesce((SELECT max(version)FROM prompt_versions WHERE agent_id=a.id),0),coalesce((SELECT prompt FROM prompt_versions WHERE agent_id=a.id ORDER BY version DESC LIMIT 1),'')FROM agents a ORDER BY a.created_at DESC`)
	if e != nil {
		errJSON(w, 500, e.Error())
		return
	}
	defer rows.Close()
	out := []Agent{}
	for rows.Next() {
		var a Agent
		var en, ws, mc int
		var tools string
		if e = rows.Scan(&a.ID, &a.Name, &a.Model, &en, &ws, &mc, &tools, &a.MaxSteps, &a.MaxTokens, &a.TimeoutSeconds, &a.CreatedAt, &a.UpdatedAt, &a.PromptVersion, &a.Prompt); e != nil {
			errJSON(w, 500, e.Error())
			return
		}
		a.Enabled = en != 0
		a.WebSearch = ws != 0
		a.MCPEnabled = mc != 0
		_ = json.Unmarshal([]byte(tools), &a.Tools)
		out = append(out, a)
	}
	write(w, 200, out)
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) {
	var a Agent
	if e := decode(r, &a); e != nil {
		errJSON(w, 400, "invalid JSON")
		return
	}
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" || len(a.Name) > 100 || len(a.Prompt) > 32000 {
		errJSON(w, 400, "name is required; name and prompt have length limits")
		return
	}
	if a.MaxSteps == 0 {
		a.MaxSteps = 8
	}
	if a.MaxTokens == 0 {
		a.MaxTokens = 4096
	}
	if a.TimeoutSeconds == 0 {
		a.TimeoutSeconds = 120
	}
	if a.MaxSteps < 1 || a.MaxSteps > 50 || a.MaxTokens < 1 || a.MaxTokens > 200000 || a.TimeoutSeconds < 1 || a.TimeoutSeconds > 3600 {
		errJSON(w, 400, "limits are outside supported bounds")
		return
	}
	a.ID = newID()
	a.CreatedAt = now()
	a.UpdatedAt = a.CreatedAt
	t, _ := json.Marshal(a.Tools)
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		errJSON(w, 500, "could not save agent")
		return
	}
	defer tx.Rollback()
	_, e = tx.Exec(`INSERT INTO agents(id,name,model,enabled,web_search,mcp_enabled,tools_json,max_steps,max_tokens,timeout_seconds,created_at,updated_at)VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, a.ID, a.Name, a.Model, boolInt(a.Enabled), boolInt(a.WebSearch), boolInt(a.MCPEnabled), string(t), a.MaxSteps, a.MaxTokens, a.TimeoutSeconds, a.CreatedAt, a.UpdatedAt)
	if e != nil {
		errJSON(w, 500, "could not save agent")
		return
	}
	if a.Prompt != "" {
		_, e = tx.Exec(`INSERT INTO prompt_versions(agent_id,version,prompt,created_at)VALUES(?,1,?,?)`, a.ID, a.Prompt, now())
		if e != nil {
			errJSON(w, 500, "could not save prompt")
			return
		}
		a.PromptVersion = 1
	}
	if e = eventTx(tx, "agent.created", "local-user", "", a.ID, map[string]any{"agent_id": a.ID}); e != nil || tx.Commit() != nil {
		errJSON(w, 500, "could not audit agent creation")
		return
	}
	write(w, 201, a)
}
func (s *Server) prompts(w http.ResponseWriter, r *http.Request) {
	rows, e := s.db.Query(`SELECT id,version,prompt,created_at FROM prompt_versions WHERE agent_id=? ORDER BY version DESC`, r.PathValue("id"))
	if e != nil {
		errJSON(w, 500, e.Error())
		return
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		var i, v int
		var p, t string
		if e = rows.Scan(&i, &v, &p, &t); e != nil {
			errJSON(w, 500, e.Error())
			return
		}
		out = append(out, map[string]any{"id": i, "version": v, "prompt": p, "created_at": t})
	}
	write(w, 200, out)
}
func (s *Server) addPrompt(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Prompt string `json:"prompt"`
	}
	if e := decode(r, &b); e != nil {
		errJSON(w, 400, "invalid JSON")
		return
	}
	if strings.TrimSpace(b.Prompt) == "" || len(b.Prompt) > 32000 {
		errJSON(w, 400, "prompt must be 1 to 32000 characters")
		return
	}
	tx, e := s.db.Begin()
	if e != nil {
		errJSON(w, 500, e.Error())
		return
	}
	defer tx.Rollback()
	var v int
	if e = tx.QueryRow(`SELECT coalesce(max(version),0)+1 FROM prompt_versions WHERE agent_id=?`, r.PathValue("id")).Scan(&v); e != nil {
		errJSON(w, 500, e.Error())
		return
	}
	if _, e = tx.Exec(`INSERT INTO prompt_versions(agent_id,version,prompt,created_at)VALUES(?,?,?,?)`, r.PathValue("id"), v, b.Prompt, now()); e != nil {
		errJSON(w, 404, "agent not found")
		return
	}
	if e = eventTx(tx, "prompt.version_created", "local-user", "", r.PathValue("id"), map[string]any{"version": v}); e != nil {
		errJSON(w, 500, "could not audit prompt")
		return
	}
	if e = tx.Commit(); e != nil {
		errJSON(w, 500, e.Error())
		return
	}
	write(w, 201, map[string]any{"version": v, "prompt": b.Prompt})
}

type Approval struct {
	ID          string          `json:"id"`
	AgentID     string          `json:"agent_id"`
	RunID       string          `json:"run_id"`
	Tool        string          `json:"tool"`
	Args        json.RawMessage `json:"args"`
	Status      string          `json:"status"`
	Reason      string          `json:"reason"`
	RequestedAt string          `json:"requested_at"`
	DecidedAt   string          `json:"decided_at"`
	Approver    string          `json:"approver"`
}

func (s *Server) approvals(w http.ResponseWriter, r *http.Request) {
	rows, e := s.db.Query(`SELECT id,agent_id,run_id,tool,args_json,status,reason,requested_at,decided_at,approver FROM approvals ORDER BY requested_at DESC LIMIT 500`)
	if e != nil {
		errJSON(w, 500, e.Error())
		return
	}
	defer rows.Close()
	out := []Approval{}
	for rows.Next() {
		var a Approval
		var args string
		if e = rows.Scan(&a.ID, &a.AgentID, &a.RunID, &a.Tool, &args, &a.Status, &a.Reason, &a.RequestedAt, &a.DecidedAt, &a.Approver); e != nil {
			errJSON(w, 500, e.Error())
			return
		}
		a.Args = json.RawMessage(args)
		out = append(out, a)
	}
	write(w, 200, out)
}
func (s *Server) createApproval(w http.ResponseWriter, r *http.Request) {
	var a Approval
	if e := decode(r, &a); e != nil {
		errJSON(w, 400, "invalid JSON")
		return
	}
	if strings.TrimSpace(a.Tool) == "" {
		errJSON(w, 400, "tool is required")
		return
	}
	if len(a.Args) == 0 {
		a.Args = json.RawMessage(`{}`)
	}
	if !json.Valid(a.Args) {
		errJSON(w, 400, "args must be valid JSON")
		return
	}
	a.ID = newID()
	a.Status = "pending"
	a.RequestedAt = now()
	tx, e := s.db.BeginTx(r.Context(), nil)
	if e != nil {
		errJSON(w, 500, "could not record approval")
		return
	}
	defer tx.Rollback()
	_, e = tx.Exec(`INSERT INTO approvals(id,agent_id,run_id,tool,args_json,status,reason,requested_at)VALUES(?,?,?,?,?,?,?,?)`, a.ID, a.AgentID, a.RunID, a.Tool, string(a.Args), a.Status, a.Reason, a.RequestedAt)
	if e != nil {
		errJSON(w, 500, "could not record approval")
		return
	}
	if e = eventTx(tx, "approval.requested", "policy", a.RunID, a.AgentID, map[string]any{"approval_id": a.ID, "tool": a.Tool, "args_redacted": true}); e != nil || tx.Commit() != nil {
		errJSON(w, 500, "could not audit approval")
		return
	}
	write(w, 201, a)
}
func (s *Server) decide(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		state, event := "denied", "approval.denied"
		if approve {
			state, event = "approved", "approval.approved"
		}
		at := now()
		tx, e := s.db.BeginTx(r.Context(), nil)
		if e != nil {
			errJSON(w, 500, "could not record decision")
			return
		}
		defer tx.Rollback()
		var runID, agentID string
		if e = tx.QueryRow(`SELECT run_id,agent_id FROM approvals WHERE id=?`, r.PathValue("id")).Scan(&runID, &agentID); e != nil {
			errJSON(w, 409, "approval not found or already decided")
			return
		}
		res, e := tx.Exec(`UPDATE approvals SET status=?,decided_at=?,approver='local-user' WHERE id=? AND status='pending'`, state, at, r.PathValue("id"))
		if e != nil {
			errJSON(w, 500, e.Error())
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			errJSON(w, 409, "approval not found or already decided")
			return
		}
		if e = eventTx(tx, event, "local-user", runID, agentID, map[string]any{"approval_id": r.PathValue("id")}); e != nil {
			errJSON(w, 500, "could not audit decision")
			return
		}
		if e = tx.Commit(); e != nil {
			errJSON(w, 500, "could not record decision")
			return
		}
		s.notify(event, runID, r.PathValue("id"), state)
		write(w, 200, map[string]any{"id": r.PathValue("id"), "status": state, "decided_at": at, "approver": "local-user"})
	}
}
func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	rows, e := s.db.Query(`SELECT id,run_id,agent_id,event_type,actor,details_json,created_at FROM audit ORDER BY id DESC LIMIT 500`)
	if e != nil {
		errJSON(w, 500, e.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int
		var run, agent, kind, actor, details, at string
		if e = rows.Scan(&id, &run, &agent, &kind, &actor, &details, &at); e != nil {
			errJSON(w, 500, e.Error())
			return
		}
		var d any
		_ = json.Unmarshal([]byte(details), &d)
		out = append(out, map[string]any{"id": id, "run_id": run, "agent_id": agent, "event_type": kind, "actor": actor, "details": d, "created_at": at})
	}
	write(w, 200, out)
}

type Automation struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Enabled         bool            `json:"enabled"`
	TriggerType     string          `json:"trigger_type"`
	Schedule        string          `json:"schedule,omitempty"`
	AgentID         string          `json:"agent_id"`
	Input           string          `json:"input"`
	IntervalMinutes int             `json:"interval_minutes,omitempty"`
	TimeUTC         string          `json:"time_utc,omitempty"`
	NextRunAt       string          `json:"next_run_at,omitempty"`
	ValidationError string          `json:"validation_error,omitempty"`
	Pipeline        json.RawMessage `json:"pipeline,omitempty"`
	CreatedAt       string          `json:"created_at"`
	UpdatedAt       string          `json:"updated_at"`
}

func (s *Server) automations(w http.ResponseWriter, r *http.Request) {
	rows, e := s.db.Query(`SELECT id,name,enabled,trigger_type,schedule,pipeline_json,created_at,updated_at,agent_id,input,interval_minutes,time_utc,next_run_at,validation_error FROM automations ORDER BY created_at DESC`)
	if e != nil {
		errJSON(w, 500, e.Error())
		return
	}
	defer rows.Close()
	out := []Automation{}
	for rows.Next() {
		var a Automation
		var en int
		var p string
		if e = rows.Scan(&a.ID, &a.Name, &en, &a.TriggerType, &a.Schedule, &p, &a.CreatedAt, &a.UpdatedAt, &a.AgentID, &a.Input, &a.IntervalMinutes, &a.TimeUTC, &a.NextRunAt, &a.ValidationError); e != nil {
			errJSON(w, 500, e.Error())
			return
		}
		a.Enabled = en != 0
		a.Pipeline = json.RawMessage(p)
		out = append(out, a)
	}
	write(w, 200, out)
}
func (s *Server) createAutomation(w http.ResponseWriter, r *http.Request) {
	var a Automation
	if e := decode(r, &a); e != nil {
		errJSON(w, 400, "invalid JSON")
		return
	}
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" || len(a.Name) > 100 {
		errJSON(w, 400, "name is required and must be at most 100 characters")
		return
	}
	if a.TriggerType == "" {
		a.TriggerType = "manual"
	}
	if a.Schedule != "" || (len(a.Pipeline) != 0 && string(a.Pipeline) != `[]`) {
		errJSON(w, 400, "free-form schedules and pipelines are not executable; use a manual, interval, or daily_utc scheduled agent run")
		return
	}
	if a.AgentID == "" || a.Input == "" || len(a.Input) > 64000 {
		errJSON(w, 400, "agent_id and input (1 to 64000 characters) are required")
		return
	}
	switch a.TriggerType {
	case "manual":
		if a.IntervalMinutes != 0 || a.TimeUTC != "" {
			errJSON(w, 400, "manual runs do not accept a schedule")
			return
		}
	case "interval":
		if a.IntervalMinutes < 1 || a.IntervalMinutes > 1440 || a.TimeUTC != "" {
			errJSON(w, 400, "interval_minutes must be 1 to 1440")
			return
		}
		a.Schedule = fmt.Sprintf("every %d minutes", a.IntervalMinutes)
	case "daily_utc":
		if !validUTCTime(a.TimeUTC) || a.IntervalMinutes != 0 {
			errJSON(w, 400, "time_utc must use HH:MM (UTC)")
			return
		}
		a.Schedule = "daily at " + a.TimeUTC + " UTC"
	default:
		errJSON(w, 400, "trigger_type must be manual, interval, or daily_utc; legacy cron, polling, and webhook triggers are not executed")
		return
	}
	config, _, e := s.agentConfig(a.AgentID)
	if e != nil {
		errJSON(w, 409, "agent is not ready for scheduled runs: "+e.Error())
		return
	}
	if config.Restricted {
		errJSON(w, 409, "agent model must pass capability checks before scheduled runs")
		return
	}
	a.ID = newID()
	a.CreatedAt = now()
	a.UpdatedAt = a.CreatedAt
	if a.Enabled && a.TriggerType != "manual" {
		a.NextRunAt = nextSchedule(a, s.automationClock()).Format(time.RFC3339Nano)
	}
	// Keep the old metadata column empty; scheduled runs execute one pinned
	// agent call in code and do not interpret caller-supplied pipeline JSON.
	a.Pipeline = json.RawMessage(`[]`)
	_, e = s.db.Exec(`INSERT INTO automations(id,name,enabled,trigger_type,schedule,pipeline_json,created_at,updated_at,agent_id,input,interval_minutes,time_utc,next_run_at,validation_error)VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, a.ID, a.Name, boolInt(a.Enabled), a.TriggerType, a.Schedule, string(a.Pipeline), a.CreatedAt, a.UpdatedAt, a.AgentID, a.Input, a.IntervalMinutes, a.TimeUTC, a.NextRunAt, "")
	if e != nil {
		errJSON(w, 500, "could not save automation")
		return
	}
	s.event("automation.created", "local-user", map[string]any{"automation_id": a.ID, "enabled": a.Enabled})
	if a.Enabled && a.TriggerType != "manual" {
		s.scheduleWake()
	}
	write(w, 201, a)
}
func (s *Server) enqueue(w http.ResponseWriter, r *http.Request) {
	var b struct {
		IdempotencyKey string `json:"idempotency_key"`
	}
	if e := decode(r, &b); e != nil {
		errJSON(w, 400, "invalid JSON")
		return
	}
	if strings.TrimSpace(b.IdempotencyKey) == "" || len(b.IdempotencyKey) > 200 {
		errJSON(w, 400, "idempotency_key is required and at most 200 characters")
		return
	}
	var existing, existingAutomation string
	if err := s.db.QueryRow(`SELECT id,automation_id FROM jobs WHERE idempotency_key=?`, b.IdempotencyKey).Scan(&existing, &existingAutomation); err == nil {
		if existingAutomation != r.PathValue("id") {
			errJSON(w, 409, "idempotency_key is already used by another automation")
			return
		}
		write(w, 200, map[string]any{"id": existing, "deduplicated": true})
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		errJSON(w, 500, "could not check job idempotency")
		return
	}
	snap, err := s.jobSnapshot(r.PathValue("id"))
	if err != nil {
		errJSON(w, 409, err.Error())
		return
	}
	payload, err := json.Marshal(snap)
	if err != nil {
		errJSON(w, 500, "could not pin scheduled run")
		return
	}
	at := now()
	jid := newID()
	stepKey := "job:" + jid + ":agent-run:v1"
	runID := "jobrun-" + jid
	res, e := s.db.Exec(`INSERT OR IGNORE INTO jobs(id,automation_id,idempotency_key,status,run_after,created_at,updated_at,run_id,step_key,payload_json)SELECT ?,id,?,'queued',?,?,?,?,?,? FROM automations WHERE id=? AND enabled=1 AND agent_id<>''`, jid, b.IdempotencyKey, at, at, at, runID, stepKey, string(payload), r.PathValue("id"))
	if e != nil {
		errJSON(w, 500, "could not queue job")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		e = s.db.QueryRow(`SELECT id,automation_id FROM jobs WHERE idempotency_key=?`, b.IdempotencyKey).Scan(&existing, &existingAutomation)
		if e == nil && existingAutomation == r.PathValue("id") {
			write(w, 200, map[string]any{"id": existing, "deduplicated": true})
			return
		}
		if e == nil {
			errJSON(w, 409, "idempotency_key is already used by another automation")
			return
		}
		errJSON(w, 404, "automation not found")
		return
	}
	_, _ = s.db.Exec(`UPDATE automations SET validation_error='' WHERE id=? AND validation_error<>''`, r.PathValue("id"))
	s.event("job.queued", "local-user", map[string]any{"job_id": jid, "automation_id": r.PathValue("id"), "idempotency_key": b.IdempotencyKey, "step_key": stepKey})
	s.scheduleWake()
	write(w, 201, map[string]any{"id": jid, "status": "queued", "run_after": at, "deduplicated": false})
}
func (s *Server) jobs(w http.ResponseWriter, r *http.Request) {
	rows, e := s.db.Query(`SELECT id,automation_id,idempotency_key,status,attempts,run_after,last_error,created_at,updated_at,run_id,step_key FROM jobs ORDER BY created_at DESC LIMIT 500`)
	if e != nil {
		errJSON(w, 500, e.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, aid, key, state, after, last, created, updated, runID, stepKey string
		var attempts int
		if e = rows.Scan(&id, &aid, &key, &state, &attempts, &after, &last, &created, &updated, &runID, &stepKey); e != nil {
			errJSON(w, 500, e.Error())
			return
		}
		out = append(out, map[string]any{"id": id, "automation_id": aid, "idempotency_key": key, "status": state, "attempts": attempts, "run_after": after, "next_run_at": after, "last_error": last, "run_id": runID, "step_key": stepKey, "created_at": created, "updated_at": updated})
	}
	write(w, 200, out)
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/")
	if p == "" {
		p = "index.html"
	} else if p == "dashboard" {
		p = "dashboard.html"
	}
	b, err := web.Files.ReadFile(p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch filepath.Ext(p) {
	case ".html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	case ".css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case ".js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	}
	_, _ = w.Write(b)
}

type ModelConfig struct {
	ID        string              `json:"id"`
	Name      string              `json:"name"`
	Provider  string              `json:"provider"`
	BaseURL   string              `json:"base_url"`
	Model     string              `json:"model"`
	CreatedAt string              `json:"created_at"`
	UpdatedAt string              `json:"updated_at"`
	LastProbe *models.ProbeResult `json:"last_probe,omitempty"`
	ProbedAt  string              `json:"probed_at,omitempty"`
}

func normalizeModelConfig(provider, baseURL string) (string, error) {
	provider = strings.TrimSpace(strings.ToLower(provider))
	if provider != "ollama" && provider != "lm_studio" && provider != "openai" && provider != "openai_compatible" {
		return "", errors.New("unsupported provider")
	}
	if strings.TrimSpace(baseURL) == "" {
		switch provider {
		case "ollama":
			baseURL = "http://127.0.0.1:11434/v1"
		case "lm_studio":
			baseURL = "http://127.0.0.1:1234/v1"
		case "openai":
			baseURL = "https://api.openai.com/v1"
		}
	}
	u, e := url.Parse(strings.TrimSpace(baseURL))
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errors.New("base_url must be a valid URL without credentials, query, or fragment")
	}
	if u.Scheme == "http" {
		host := strings.ToLower(u.Hostname())
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return "", errors.New("HTTP model endpoints must use localhost or a loopback IP; remote endpoints require HTTPS")
		}
	} else if u.Scheme != "https" {
		return "", errors.New("model endpoint must use HTTPS or loopback HTTP")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", errors.New("invalid model endpoint port")
		}
	}
	u.Scheme = strings.ToLower(u.Scheme)
	return strings.TrimRight(u.String(), "/"), nil
}

func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	rows, e := s.db.Query(`SELECT c.id,c.name,c.provider,c.base_url,c.model,c.created_at,c.updated_at,p.score,p.json_output,p.single_tool,p.tool_choice,p.argument_accuracy,p.restricted,p.probed_at FROM model_configs c LEFT JOIN model_probes p ON p.id=(SELECT id FROM model_probes WHERE model_config_id=c.id ORDER BY probed_at DESC,id DESC LIMIT 1) ORDER BY c.created_at DESC`)
	if e != nil {
		errJSON(w, 500, "could not read model configuration")
		return
	}
	defer rows.Close()
	out := []ModelConfig{}
	for rows.Next() {
		var c ModelConfig
		var score, jsonOK, single, choice, args, restricted sql.NullInt64
		var probed sql.NullString
		if e = rows.Scan(&c.ID, &c.Name, &c.Provider, &c.BaseURL, &c.Model, &c.CreatedAt, &c.UpdatedAt, &score, &jsonOK, &single, &choice, &args, &restricted, &probed); e != nil {
			errJSON(w, 500, "could not read model configuration")
			return
		}
		if score.Valid {
			c.LastProbe = &models.ProbeResult{Score: int(score.Int64), JSONOutput: jsonOK.Int64 != 0, SingleTool: single.Int64 != 0, ToolChoice: choice.Int64 != 0, ArgumentAccuracy: args.Int64 != 0, Restricted: restricted.Int64 != 0}
			c.ProbedAt = probed.String
		}
		out = append(out, c)
	}
	write(w, 200, out)
}

func (s *Server) createModel(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name     string `json:"name"`
		Provider string `json:"provider"`
		BaseURL  string `json:"base_url"`
		Model    string `json:"model"`
	}
	if e := decode(r, &b); e != nil {
		errJSON(w, 400, "invalid model configuration")
		return
	}
	b.Name = strings.TrimSpace(b.Name)
	b.Model = strings.TrimSpace(b.Model)
	if b.Model == "" || len(b.Model) > 200 || len(b.Name) > 100 {
		errJSON(w, 400, "model is required and names must be at most 100 characters")
		return
	}
	base, e := normalizeModelConfig(b.Provider, b.BaseURL)
	if e != nil {
		errJSON(w, 400, e.Error())
		return
	}
	b.Provider = strings.ToLower(strings.TrimSpace(b.Provider))
	if b.Name == "" {
		b.Name = b.Provider + " · " + b.Model
	}
	c := ModelConfig{ID: newID(), Name: b.Name, Provider: b.Provider, BaseURL: base, Model: b.Model, CreatedAt: now()}
	c.UpdatedAt = c.CreatedAt
	if _, e = s.db.Exec(`INSERT INTO model_configs(id,name,provider,base_url,model,created_at,updated_at)VALUES(?,?,?,?,?,?,?)`, c.ID, c.Name, c.Provider, c.BaseURL, c.Model, c.CreatedAt, c.UpdatedAt); e != nil {
		errJSON(w, 500, "could not save model configuration")
		return
	}
	s.event("model.configured", "local-user", map[string]any{"model_config_id": c.ID, "provider": c.Provider, "model": c.Model})
	write(w, 201, c)
}

func (s *Server) probeModel(w http.ResponseWriter, r *http.Request) {
	var b struct {
		APIKey string `json:"api_key"`
	}
	if e := decode(r, &b); e != nil {
		errJSON(w, 400, "invalid probe request")
		return
	}
	if len(b.APIKey) > 4096 {
		errJSON(w, 400, "api_key is too long")
		return
	}
	var c ModelConfig
	e := s.db.QueryRow(`SELECT id,name,provider,base_url,model,created_at,updated_at FROM model_configs WHERE id=?`, r.PathValue("id")).Scan(&c.ID, &c.Name, &c.Provider, &c.BaseURL, &c.Model, &c.CreatedAt, &c.UpdatedAt)
	if e == sql.ErrNoRows {
		errJSON(w, 404, "model configuration not found")
		return
	}
	if e != nil {
		errJSON(w, 500, "could not read model configuration")
		return
	}
	// The optional key is kept only in this request's client object and is never persisted, logged, or returned.
	client := &models.OpenAICompatible{BaseURL: c.BaseURL, APIKey: b.APIKey, Client: &http.Client{Timeout: 4 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()
	result, e := models.Probe(ctx, client, c.Model, 100)
	if e != nil {
		errJSON(w, 502, "model capability probe failed; verify the local endpoint, model name, and provider availability")
		return
	}
	at := now()
	_, e = s.db.Exec(`INSERT INTO model_probes(model_config_id,score,json_output,single_tool,tool_choice,argument_accuracy,restricted,probed_at)VALUES(?,?,?,?,?,?,?,?)`, c.ID, result.Score, boolInt(result.JSONOutput), boolInt(result.SingleTool), boolInt(result.ToolChoice), boolInt(result.ArgumentAccuracy), boolInt(result.Restricted), at)
	if e != nil {
		errJSON(w, 500, "could not save model probe result")
		return
	}
	s.event("model.probed", "local-user", map[string]any{"model_config_id": c.ID, "score": result.Score, "restricted": result.Restricted})
	write(w, 200, map[string]any{"model_id": c.ID, "provider": c.Provider, "model": c.Model, "probe": result, "probed_at": at})
}

type MCPServerConfig struct {
	ID                    string   `json:"id"`
	Transport             string   `json:"transport"`
	Endpoint              string   `json:"endpoint,omitempty"`
	Name                  string   `json:"name"`
	Enabled               bool     `json:"enabled"`
	Command               string   `json:"command"`
	Args                  []string `json:"args"`
	WorkingDirectory      string   `json:"working_directory"`
	EnvironmentAllowlist  []string `json:"environment_allowlist"`
	AllowedTools          []string `json:"allowed_tools"`
	ReadTools             []string `json:"read_tools"`
	WriteTools            []string `json:"write_tools"`
	RequestTimeoutSeconds int      `json:"request_timeout_seconds"`
	CreatedAt             string   `json:"created_at"`
	UpdatedAt             string   `json:"updated_at"`
	RuntimeAvailable      bool     `json:"runtime_available"`
}

func (s *Server) mcpServers(w http.ResponseWriter, r *http.Request) {
	rows, e := s.db.Query(`SELECT id,name,enabled,transport,endpoint,command,args_json,working_directory,environment_allowlist_json,allowed_tools_json,read_tools_json,write_tools_json,request_timeout_seconds,created_at,updated_at FROM mcp_servers ORDER BY created_at DESC`)
	if e != nil {
		errJSON(w, 500, "could not read MCP configuration")
		return
	}
	defer rows.Close()
	out := []MCPServerConfig{}
	for rows.Next() {
		var c MCPServerConfig
		var en int
		var args, env, allowed, reads, writes string
		if e = rows.Scan(&c.ID, &c.Name, &en, &c.Transport, &c.Endpoint, &c.Command, &args, &c.WorkingDirectory, &env, &allowed, &reads, &writes, &c.RequestTimeoutSeconds, &c.CreatedAt, &c.UpdatedAt); e != nil {
			errJSON(w, 500, "could not read MCP configuration")
			return
		}
		_, connectedErr := s.hosts.Tools(c.ID)
		c.Enabled = connectedErr == nil
		c.RuntimeAvailable = c.Enabled
		_ = json.Unmarshal([]byte(args), &c.Args)
		_ = json.Unmarshal([]byte(env), &c.EnvironmentAllowlist)
		_ = json.Unmarshal([]byte(allowed), &c.AllowedTools)
		_ = json.Unmarshal([]byte(reads), &c.ReadTools)
		_ = json.Unmarshal([]byte(writes), &c.WriteTools)
		out = append(out, c)
	}
	write(w, 200, out)
}

func validateMCPEndpoint(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errors.New("endpoint must be a valid URL without credentials, query, or fragment")
	}
	if u.Scheme == "http" {
		host := strings.ToLower(u.Hostname())
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return "", errors.New("HTTP MCP endpoints must use localhost or a loopback IP; remote endpoints require HTTPS")
		}
	} else if u.Scheme != "https" {
		return "", errors.New("MCP endpoint must use HTTPS or loopback HTTP")
	}
	if port := u.Port(); port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return "", errors.New("invalid MCP endpoint port")
		}
	}
	u.Scheme = strings.ToLower(u.Scheme)
	return strings.TrimRight(u.String(), "/"), nil
}
func secretLikeName(v string) bool {
	u := strings.ToUpper(v)
	for _, part := range []string{"TOKEN", "SECRET", "PASSWORD", "CREDENTIAL", "API_KEY", "APIKEY", "AUTH"} {
		if strings.Contains(u, part) {
			return true
		}
	}
	return false
}
func cleanNames(values []string, allowSecret bool) ([]string, error) {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	if len(values) > 500 {
		return nil, errors.New("too many entries")
	}
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" || len(v) > 256 || strings.ContainsAny(v, "\x00\r\n") {
			return nil, errors.New("invalid empty or oversized entry")
		}
		if !allowSecret && secretLikeName(v) {
			return nil, errors.New("credential-like names are not allowed")
		}
		if seen[v] {
			return nil, errors.New("duplicate entries are not allowed")
		}
		seen[v] = true
		out = append(out, v)
	}
	return out, nil
}
func safeMCPArg(v string, previous string) bool {
	low := strings.ToLower(v)
	for _, needle := range []string{"token", "secret", "password", "credential", "api_key", "api-key", "apikey", "authorization", "bearer ", "sk-", "ghp_", "gho_", "github_pat_", "akia"} {
		if strings.Contains(low, needle) {
			return false
		}
	}
	if strings.Contains(v, "=") {
		parts := strings.SplitN(v, "=", 2)
		if secretLikeName(parts[0]) {
			return false
		}
	}
	if strings.HasPrefix(previous, "-") && secretLikeName(previous) {
		return false
	}
	return len(v) <= 4096 && !strings.ContainsRune(v, 0)
}
func (s *Server) createMCPServer(w http.ResponseWriter, r *http.Request) {
	var c MCPServerConfig
	if e := decode(r, &c); e != nil {
		errJSON(w, 400, "invalid MCP configuration; credential values and unknown fields are not accepted")
		return
	}
	// Runtime enablement is intentionally not configurable until policy, approval, and audit gates are connected.
	if c.Enabled {
		errJSON(w, 409, "MCP servers remain disabled until runtime policy and approval enforcement are available")
		return
	}
	c.Transport = strings.ToLower(strings.TrimSpace(c.Transport))
	if c.Transport == "" {
		c.Transport = "stdio"
	}
	if c.Transport != "stdio" && c.Transport != "streamable_http" {
		errJSON(w, 400, "transport must be stdio or streamable_http")
		return
	}
	if c.Transport == "streamable_http" {
		endpoint, endpointErr := validateMCPEndpoint(c.Endpoint)
		if endpointErr != nil {
			errJSON(w, 400, endpointErr.Error())
			return
		}
		c.Endpoint = endpoint
		if strings.TrimSpace(c.Command) != "" || len(c.Args) > 0 || strings.TrimSpace(c.WorkingDirectory) != "" || len(c.EnvironmentAllowlist) > 0 {
			errJSON(w, 400, "streamable_http configuration cannot include process settings")
			return
		}
	} else if strings.TrimSpace(c.Endpoint) != "" {
		errJSON(w, 400, "stdio configuration cannot include endpoint")
		return
	}
	c.Name = strings.TrimSpace(c.Name)
	c.Command = strings.TrimSpace(c.Command)
	if c.Name == "" || len(c.Name) > 100 || (c.Transport == "stdio" && c.Command == "") || len(c.Command) > 512 || strings.ContainsAny(c.Command, "\x00\r\n") {
		errJSON(w, 400, "name and command are required and must be within limits")
		return
	}
	if len(c.Args) > 128 {
		errJSON(w, 400, "too many command arguments")
		return
	}
	for i, arg := range c.Args {
		prev := ""
		if i > 0 {
			prev = c.Args[i-1]
		}
		if !safeMCPArg(arg, prev) {
			errJSON(w, 400, "command arguments that may contain credentials are not accepted")
			return
		}
	}
	if len(c.WorkingDirectory) > 4096 || strings.ContainsRune(c.WorkingDirectory, 0) {
		errJSON(w, 400, "invalid working_directory")
		return
	}
	var e error
	c.EnvironmentAllowlist, e = cleanNames(c.EnvironmentAllowlist, false)
	if e != nil {
		errJSON(w, 400, "invalid environment_allowlist: "+e.Error())
		return
	}
	c.AllowedTools, e = cleanNames(c.AllowedTools, true)
	if e != nil {
		errJSON(w, 400, "invalid allowed_tools: "+e.Error())
		return
	}
	c.ReadTools, e = cleanNames(c.ReadTools, true)
	if e != nil {
		errJSON(w, 400, "invalid read_tools: "+e.Error())
		return
	}
	c.WriteTools, e = cleanNames(c.WriteTools, true)
	if e != nil {
		errJSON(w, 400, "invalid write_tools: "+e.Error())
		return
	}
	allow := map[string]bool{}
	for _, n := range c.AllowedTools {
		allow[n] = true
	}
	for _, n := range append(append([]string{}, c.ReadTools...), c.WriteTools...) {
		if !allow[n] {
			errJSON(w, 400, "read_tools and write_tools must be a subset of allowed_tools")
			return
		}
	}
	if c.RequestTimeoutSeconds == 0 {
		c.RequestTimeoutSeconds = 30
	}
	if c.RequestTimeoutSeconds < 1 || c.RequestTimeoutSeconds > 300 {
		errJSON(w, 400, "request_timeout_seconds must be between 1 and 300")
		return
	}
	c.ID = newID()
	c.Enabled = false
	c.RuntimeAvailable = false
	c.CreatedAt = now()
	c.UpdatedAt = c.CreatedAt
	args, _ := json.Marshal(c.Args)
	env, _ := json.Marshal(c.EnvironmentAllowlist)
	allowed, _ := json.Marshal(c.AllowedTools)
	reads, _ := json.Marshal(c.ReadTools)
	writes, _ := json.Marshal(c.WriteTools)
	_, e = s.db.Exec(`INSERT INTO mcp_servers(id,name,enabled,transport,endpoint,command,args_json,working_directory,environment_allowlist_json,allowed_tools_json,read_tools_json,write_tools_json,request_timeout_seconds,created_at,updated_at)VALUES(?,?,0,?,?,?,?,?,?,?,?,?,?,?,?)`, c.ID, c.Name, c.Transport, c.Endpoint, c.Command, string(args), c.WorkingDirectory, string(env), string(allowed), string(reads), string(writes), c.RequestTimeoutSeconds, c.CreatedAt, c.UpdatedAt)
	if e != nil {
		errJSON(w, 500, "could not save MCP configuration")
		return
	}
	s.event("mcp.server_configured", "local-user", map[string]any{"server_id": c.ID, "name": c.Name, "transport": c.Transport, "enabled": false})
	write(w, 201, c)
}
func (s *Server) revokeMCPServer(w http.ResponseWriter, r *http.Request) {
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	s.registry.RemoveServer(r.PathValue("id"))
	_ = s.hosts.Disconnect(r.PathValue("id"))
	res, e := s.db.Exec(`DELETE FROM mcp_servers WHERE id=?`, r.PathValue("id"))
	if e != nil {
		errJSON(w, 500, "could not revoke MCP configuration")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		errJSON(w, 404, "MCP configuration not found")
		return
	}
	s.event("mcp.server_revoked", "local-user", map[string]any{"server_id": r.PathValue("id")})
	w.WriteHeader(http.StatusNoContent)
}
