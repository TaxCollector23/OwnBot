package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"ownbot/internal/engine"
	"ownbot/internal/models"
)

var (
	ErrHostDisabled           = errors.New("MCP host is disabled")
	ErrHostConnected          = errors.New("MCP host is already connected")
	ErrHostNotConnected       = errors.New("MCP host is not connected")
	ErrDispatchNotAllowed     = errors.New("MCP call lacks an engine dispatch authorization")
	ErrAuthenticationRequired = errors.New("MCP provider authentication is required")
)

const maxHostConnectTime = 5 * time.Minute

var safeServerID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// HostConfig contains runtime-only MCP configuration. It intentionally has no
// credential or vault field. Stdio processes still run with the daemon user's OS
// permissions; environment scrubbing is not a sandbox.
type HostConfig struct {
	ID                   string
	Name                 string
	Transport            string // "stdio" or "streamable_http"
	Enabled              bool
	Endpoint             string
	Command              string
	Args                 []string
	WorkingDirectory     string
	EnvironmentAllowlist []string
	AllowedTools         []string
	ReadTools            []string
	WriteTools           []string
	RequestTimeout       time.Duration
	Token                func(context.Context) (string, error)
}

type managedHost struct {
	config        HostConfig
	stdio         *Client
	http          *HTTPClient
	tools         []engine.Tool
	providerNames map[string]string // engine-safe alias -> server-provided tool name
}

// HostManager connects explicitly configured MCP providers and produces
// engine.Tool bindings. It never registers tools itself; the caller owns the
// central engine registry and policy boundary.
type HostManager struct {
	mu    sync.Mutex
	hosts map[string]*managedHost
}

func NewHostManager() *HostManager { return &HostManager{hosts: make(map[string]*managedHost)} }

// Connect starts one explicitly enabled provider, discovers its tools, and
// returns only tools in the explicit allowlist with a manual access class.
func (m *HostManager) Connect(ctx context.Context, cfg HostConfig) ([]engine.Tool, error) {
	if m == nil {
		return nil, errors.New("MCP host manager is unavailable")
	}
	if !cfg.Enabled {
		return nil, ErrHostDisabled
	}
	if !safeServerID.MatchString(cfg.ID) {
		return nil, errors.New("MCP server ID is invalid")
	}
	if err := validateHostClasses(cfg); err != nil {
		return nil, err
	}
	if len(cfg.AllowedTools) == 0 {
		return nil, errors.New("MCP host requires an explicit tool allowlist")
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 30 * time.Second
	}
	if cfg.RequestTimeout > maxHostConnectTime {
		cfg.RequestTimeout = maxHostConnectTime
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.RequestTimeout)
	defer cancel()

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.hosts[cfg.ID]; exists {
		return nil, ErrHostConnected
	}
	entry := &managedHost{config: cfg, providerNames: make(map[string]string, len(cfg.AllowedTools))}
	m.hosts[cfg.ID] = entry
	connected := false
	defer func() {
		if !connected {
			closeHost(entry)
			delete(m.hosts, cfg.ID)
		}
	}()

	aliases := make(map[string]string, len(cfg.AllowedTools)) // provider name -> engine-safe alias
	for _, name := range cfg.AllowedTools {
		alias := ToolAlias(cfg.ID, name)
		if alias == "" {
			return nil, errors.New("MCP tool name cannot be aliased safely")
		}
		if _, duplicate := aliases[name]; duplicate {
			return nil, errors.New("MCP allowlist contains duplicate tool names")
		}
		aliases[name] = alias
	}
	proof := func(ctx context.Context, providerName string, args json.RawMessage) error {
		alias, ok := aliases[providerName]
		if !ok || !engine.IsAuthorizedDispatch(ctx, alias, args) {
			return ErrDispatchNotAllowed
		}
		return nil
	}

	var discovered []Tool
	var err error
	switch cfg.Transport {
	case "stdio":
		entry.stdio = New(Config{
			Name: cfg.Name, Enabled: true, Command: cfg.Command, Args: append([]string(nil), cfg.Args...),
			WorkingDirectory: cfg.WorkingDirectory, EnvironmentAllowlist: append([]string(nil), cfg.EnvironmentAllowlist...),
			AllowedTools: append([]string(nil), cfg.AllowedTools...), ReadTools: append([]string(nil), cfg.ReadTools...),
			WriteTools: append([]string(nil), cfg.WriteTools...), RequestTimeout: cfg.RequestTimeout, AuthorizeCall: proof,
		})
		if err := entry.stdio.Connect(ctx); err != nil {
			return nil, err
		}
		discovered, err = entry.stdio.Tools()
		if err != nil {
			return nil, err
		}
	case "streamable_http":
		entry.http = &HTTPClient{Endpoint: cfg.Endpoint, Enabled: true, AllowedTools: append([]string(nil), cfg.AllowedTools...), Token: cfg.Token, Authorize: proof}
		if err := entry.http.Connect(ctx); err != nil {
			if isAuthenticationError(err) {
				return nil, fmt.Errorf("%w: provider rejected unauthenticated connection", ErrAuthenticationRequired)
			}
			return nil, err
		}
		if !supportsHTTPVersion(entry.http.version) {
			return nil, errors.New("MCP server negotiated an unsupported protocol version")
		}
		discovered, err = entry.http.Tools(ctx)
		if err != nil {
			if isAuthenticationError(err) {
				return nil, fmt.Errorf("%w: provider rejected unauthenticated connection", ErrAuthenticationRequired)
			}
			return nil, err
		}
	default:
		return nil, errors.New("MCP transport must be stdio or streamable_http")
	}

	byName := make(map[string]Tool, len(discovered))
	for _, tool := range discovered {
		byName[tool.Name] = tool
	}
	for _, name := range cfg.AllowedTools {
		tool, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("configured MCP tool %q was not discovered", name)
		}
		var schema map[string]json.RawMessage
		if !json.Valid(tool.InputSchema) || json.Unmarshal(tool.InputSchema, &schema) != nil || schema == nil {
			return nil, fmt.Errorf("MCP tool %q has an invalid input schema", name)
		}
	}

	readSet := allowedSet(cfg.ReadTools)
	bindings := make([]engine.Tool, 0, len(cfg.AllowedTools))
	aliasesSeen := map[string]bool{}
	for _, remoteName := range cfg.AllowedTools {
		remoteTool := byName[remoteName]
		alias := aliases[remoteName]
		if aliasesSeen[alias] {
			return nil, errors.New("MCP tool aliases collided")
		}
		aliasesSeen[alias] = true
		toolSchema := models.Tool{Name: alias, Description: prefixedDescription(cfg.ID, remoteName, remoteTool.Description), Parameters: append(json.RawMessage(nil), remoteTool.InputSchema...)}
		binding := engine.Tool{Schema: toolSchema, Source: "mcp", Server: cfg.ID, Untrusted: true}
		entry.providerNames[alias] = remoteName
		if readSet[remoteName] {
			binding.Risk = engine.Read
		} else {
			// A generic MCP write has unknown reversibility and is treated as irreversible.
			binding.Risk = engine.Irreversible
			binding.AlwaysApprove = true
		}
		remoteNameCopy, aliasCopy, schemaCopy := remoteName, alias, append(json.RawMessage(nil), remoteTool.InputSchema...)
		binding.Execute = func(ctx context.Context, args json.RawMessage, _ string) (json.RawMessage, error) {
			if !engine.IsAuthorizedDispatch(ctx, aliasCopy, args) {
				return nil, ErrDispatchNotAllowed
			}
			if err := models.ValidateJSONSchema(schemaCopy, args); err != nil {
				return nil, errors.New("MCP tool arguments failed schema validation")
			}
			callCtx, cancel := context.WithTimeout(ctx, cfg.RequestTimeout)
			defer cancel()
			var result ToolResult
			var err error
			if entry.stdio != nil {
				result, err = entry.stdio.CallTool(callCtx, remoteNameCopy, args)
			} else {
				result, err = entry.http.CallTool(callCtx, remoteNameCopy, args)
			}
			if err != nil {
				if isAuthenticationError(err) {
					return nil, fmt.Errorf("%w: provider authorization expired or was denied", ErrAuthenticationRequired)
				}
				return nil, err
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				return nil, errors.New("MCP result encoding failed")
			}
			if result.IsError {
				return encoded, errors.New("MCP server reported a tool error")
			}
			return encoded, nil
		}
		bindings = append(bindings, binding)
	}
	entry.tools = bindings
	if err := m.checkAliasCollisions(cfg.ID, bindings); err != nil {
		return nil, err
	}
	connected = true
	return append([]engine.Tool(nil), bindings...), nil
}

func isAuthenticationError(err error) bool {
	var status *HTTPStatusError
	return errors.As(err, &status) && (status.Code == http.StatusUnauthorized || status.Code == http.StatusForbidden)
}

// Disconnect closes a host and discards its runtime session and tool closures.
func (m *HostManager) Disconnect(serverID string) error {
	if m == nil {
		return ErrHostNotConnected
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.hosts[serverID]
	if !ok {
		return ErrHostNotConnected
	}
	closeErr := closeHost(entry)
	delete(m.hosts, serverID)
	return closeErr
}

// Tools returns a copy of the connected host's engine bindings.
func (m *HostManager) Tools(serverID string) ([]engine.Tool, error) {
	if m == nil {
		return nil, ErrHostNotConnected
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.hosts[serverID]
	if !ok {
		return nil, ErrHostNotConnected
	}
	if entry.http != nil {
		entry.http.mu.Lock()
		authRequired := entry.http.authRequired
		entry.http.mu.Unlock()
		if authRequired {
			return nil, ErrAuthenticationRequired
		}
	}
	return append([]engine.Tool(nil), entry.tools...), nil
}

// ProviderToolName returns the remote name represented by a connected tool
// alias. It lets API clients compare configured provider allowlists without
// exposing provider names as agent-callable tool names.
func (m *HostManager) ProviderToolName(serverID, alias string) (string, bool) {
	if m == nil {
		return "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.hosts[serverID]
	if !ok {
		return "", false
	}
	name, ok := entry.providerNames[alias]
	return name, ok
}

func (m *HostManager) checkAliasCollisions(serverID string, tools []engine.Tool) error {
	for id, host := range m.hosts {
		if id == serverID {
			continue
		}
		for _, existing := range host.tools {
			for _, candidate := range tools {
				if existing.Schema.Name == candidate.Schema.Name {
					return errors.New("MCP tool alias collides with another server")
				}
			}
		}
	}
	return nil
}

func validateHostClasses(cfg HostConfig) error {
	allow, reads, writes := allowedSet(cfg.AllowedTools), allowedSet(cfg.ReadTools), allowedSet(cfg.WriteTools)
	if len(allow) != len(cfg.AllowedTools) || len(reads) != len(cfg.ReadTools) || len(writes) != len(cfg.WriteTools) {
		return errors.New("MCP tool lists contain empty or duplicate names")
	}
	for name := range allow {
		if !reads[name] && !writes[name] {
			return fmt.Errorf("MCP tool %q has no configured access class", name)
		}
	}
	for name := range reads {
		if !allow[name] || writes[name] {
			return errors.New("MCP read/write classes must be disjoint subsets of the allowlist")
		}
	}
	for name := range writes {
		if !allow[name] || reads[name] {
			return errors.New("MCP read/write classes must be disjoint subsets of the allowlist")
		}
	}
	return nil
}

func ToolAlias(serverID, providerName string) string {
	if !safeServerID.MatchString(serverID) || providerName == "" || len(providerName) > 256 {
		return ""
	}
	hash := sha256.Sum256([]byte(serverID + "\x00" + providerName))
	suffix := hex.EncodeToString(hash[:5])
	server := aliasPart(serverID)
	if len(server) > 12 {
		server = server[:12]
	}
	tool := aliasPart(providerName)
	const fixedLength = len("mcp_") + 12 + 1 + 1 + 10
	maxTool := 64 - fixedLength
	if len(tool) > maxTool {
		tool = tool[:maxTool]
	}
	if tool == "" {
		tool = "tool"
	}
	return "mcp_" + server + "_" + tool + "_" + suffix
}

func aliasPart(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func prefixedDescription(server, remote, description string) string {
	const maxDescription = 1024
	d := "Untrusted MCP metadata from server " + server + ", remote tool " + remote
	if description != "" {
		d += ": " + description
	}
	if len(d) > maxDescription {
		d = d[:maxDescription]
	}
	return d
}

func closeHost(host *managedHost) error {
	if host == nil {
		return nil
	}
	if host.stdio != nil {
		_ = host.stdio.Close()
	}
	if host.http != nil {
		return terminateHTTPSession(host.http)
	}
	return nil
}

func terminateHTTPSession(client *HTTPClient) error {
	client.mu.Lock()
	endpoint, session, version := client.Endpoint, client.session, client.version
	client.initialized = false
	client.session = ""
	client.version = ""
	client.mu.Unlock()
	if session == "" || !supportsHTTPVersion(version) {
		return nil
	}
	req, err := http.NewRequest(http.MethodDelete, endpoint, nil)
	if err != nil {
		return errors.New("could not terminate MCP session")
	}
	req.Header.Set("Mcp-Session-Id", session)
	if version != "" {
		req.Header.Set("MCP-Protocol-Version", version)
	}
	transport := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := transport.Do(req)
	if err != nil {
		return errors.New("could not terminate MCP session")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNotFound {
		return nil // Session deletion is optional for Streamable HTTP servers.
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("MCP server did not terminate its session")
	}
	return nil
}

// Close ends all runtime sessions when the owning daemon shuts down.
func (m *HostManager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var err error
	for id, host := range m.hosts {
		err = errors.Join(err, closeHost(host))
		delete(m.hosts, id)
	}
	return err
}
