// Package mcp implements a disabled-by-default MCP client for explicitly configured
// local stdio servers. It scrubs inherited environment variables, but a child process
// still runs with the daemon user's operating-system permissions; this is not a sandbox.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrDisabled       = errors.New("MCP server is disabled")
	ErrNotConnected   = errors.New("MCP server is not connected")
	ErrToolNotAllowed = errors.New("MCP tool is not allowlisted")
)

const protocolVersion = "2025-11-25" // Legacy initialization based stdio protocol.
const maxMessageBytes = 2 << 20
const maxTools = 1000

// Config requires explicit activation, a per-server tool allowlist, and explicit
// access classes. EnvironmentAllowlist copies named non-secret variables only.
// Credentials from the daemon vault are never available through this interface.
type Config struct {
	Name                 string
	Enabled              bool
	Command              string
	Args                 []string
	WorkingDirectory     string
	EnvironmentAllowlist []string
	AllowedTools         []string
	ReadTools            []string
	WriteTools           []string
	RequestTimeout       time.Duration
	// AuthorizeCall must run policy and approval checks for every tool invocation.
	// It is deliberately runtime-only and should not be serialized with Config.
	AuthorizeCall func(context.Context, string, json.RawMessage) error
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations json.RawMessage `json:"annotations,omitempty"`
}

type ToolResult struct {
	Content           json.RawMessage `json:"content,omitempty"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError,omitempty"`
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Client is a single stdio server session. It is not safe to reuse after Close.
type Client struct {
	cfg       Config
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	messages  chan rpcMessage
	readErr   chan error
	writeMu   sync.Mutex
	rpcMu     sync.Mutex
	nextID    atomic.Uint64
	connected atomic.Bool
	dead      atomic.Bool
	closeOnce sync.Once
}

func New(cfg Config) *Client { return &Client{cfg: cfg} }

func (c *Client) Connect(ctx context.Context) error {
	if !c.cfg.Enabled {
		return ErrDisabled
	}
	if strings.TrimSpace(c.cfg.Command) == "" {
		return errors.New("MCP server command is required")
	}
	if len(c.cfg.AllowedTools) == 0 {
		return errors.New("MCP server requires an explicit tool allowlist")
	}
	if err := validateAccessConfig(c.cfg); err != nil {
		return err
	}
	if c.connected.Load() {
		return nil
	}

	cmd := exec.Command(c.cfg.Command, c.cfg.Args...)
	cmd.Dir = c.cfg.WorkingDirectory
	cmd.Env = scrubbedEnv(c.cfg.EnvironmentAllowlist)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return errors.New("could not open MCP server stdin")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return errors.New("could not open MCP server stdout")
	}
	// stderr is discarded because subprocess diagnostics can contain secrets.
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("start MCP server failed: %w", err)
	}
	c.cmd, c.stdin = cmd, stdin
	c.messages, c.readErr = make(chan rpcMessage, 32), make(chan error, 1)
	go c.readLoop(stdout)

	initCtx, cancel := c.withTimeout(ctx)
	defer cancel()
	result, err := c.request(initCtx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "ownbot", "version": "dev"},
	})
	if err == nil {
		var init struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if json.Unmarshal(result, &init) != nil || init.ProtocolVersion != protocolVersion {
			err = errors.New("MCP server negotiated an unsupported protocol version")
		}
	}
	if err == nil {
		err = c.notify("notifications/initialized", map[string]any{})
	}
	if err != nil {
		_ = c.Close()
		return err
	}
	if _, err = c.listAllTools(initCtx); err != nil {
		_ = c.Close()
		return err
	}
	c.connected.Store(true)
	return nil
}

// Tools discovers tools but only returns names that are explicitly allowlisted and
// assigned a read or write class in this server's configuration.
func (c *Client) Tools() ([]Tool, error) {
	if !c.connected.Load() {
		return nil, ErrNotConnected
	}
	ctx, cancel := c.withTimeout(context.Background())
	defer cancel()
	all, err := c.listAllTools(ctx)
	if err != nil {
		return nil, err
	}
	allowed := allowedSet(c.cfg.AllowedTools)
	classified := allowedSet(append(append([]string{}, c.cfg.ReadTools...), c.cfg.WriteTools...))
	filtered := make([]Tool, 0, len(all))
	for _, tool := range all {
		if allowed[tool.Name] && classified[tool.Name] {
			filtered = append(filtered, tool)
		}
	}
	return filtered, nil
}

// CallTool requires all three gates: server enabled, tool allowlisted/classified,
// and a runtime policy callback that approves this invocation's exact arguments.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (ToolResult, error) {
	if !c.connected.Load() {
		return ToolResult{}, ErrNotConnected
	}
	if !allowedSet(c.cfg.AllowedTools)[name] {
		return ToolResult{}, ErrToolNotAllowed
	}
	if !allowedSet(append(append([]string{}, c.cfg.ReadTools...), c.cfg.WriteTools...))[name] {
		return ToolResult{}, errors.New("MCP tool has no configured access class")
	}
	if !json.Valid(args) {
		return ToolResult{}, errors.New("MCP tool arguments must be valid JSON")
	}
	if c.cfg.AuthorizeCall == nil {
		return ToolResult{}, errors.New("MCP tool call requires a policy decision")
	}
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	if err := c.cfg.AuthorizeCall(ctx, name, append(json.RawMessage(nil), args...)); err != nil {
		return ToolResult{}, fmt.Errorf("MCP tool call denied by policy: %w", err)
	}
	result, err := c.request(ctx, "tools/call", map[string]any{"name": name, "arguments": json.RawMessage(args)})
	if err != nil {
		return ToolResult{}, err
	}
	var response ToolResult
	if err = json.Unmarshal(result, &response); err != nil {
		return ToolResult{}, errors.New("MCP server returned an invalid tool result")
	}
	return response, nil
}

func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.connected.Store(false)
		if c.stdin != nil {
			_ = c.stdin.Close()
		}
		if c.cmd != nil && c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
			_ = c.cmd.Wait()
		}
	})
	return nil
}

func (c *Client) listAllTools(ctx context.Context) ([]Tool, error) {
	cursor := ""
	var all []Tool
	seenCursors := map[string]bool{}
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.request(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var page struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if json.Unmarshal(raw, &page) != nil {
			return nil, errors.New("MCP server returned an invalid tool list")
		}
		all = append(all, page.Tools...)
		if len(all) > maxTools {
			return nil, errors.New("MCP server tool list exceeds limit")
		}
		if page.NextCursor == "" {
			break
		}
		if seenCursors[page.NextCursor] {
			return nil, errors.New("MCP server repeated a pagination cursor")
		}
		seenCursors[page.NextCursor] = true
		cursor = page.NextCursor
	}
	seen := map[string]bool{}
	for _, tool := range all {
		if tool.Name == "" || seen[tool.Name] {
			return nil, errors.New("MCP server returned duplicate or empty tool name")
		}
		seen[tool.Name] = true
	}
	return all, nil
}

func (c *Client) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	d := c.cfg.RequestTimeout
	if d <= 0 {
		d = 30 * time.Second
	}
	return context.WithTimeout(ctx, d)
}

func (c *Client) notify(method string, params any) error {
	_, err := c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	return err
}

func (c *Client) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.rpcMu.Lock()
	defer c.rpcMu.Unlock()
	if c.cmd == nil || c.dead.Load() {
		return nil, ErrNotConnected
	}
	id := c.nextID.Add(1)
	rawID, _ := json.Marshal(id)
	if _, err := c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		select {
		case <-ctx.Done():
			// A timed-out operation may still be running in the child. Terminate the
			// process so it cannot continue unobserved, especially for writes.
			go c.Close()
			return nil, ctx.Err()
		case err := <-c.readErr:
			if err == nil {
				return nil, io.EOF
			}
			return nil, errors.New("MCP server stream failed")
		case msg := <-c.messages:
			if msg.JSONRPC != "2.0" {
				continue
			}
			if len(msg.ID) == 0 || !bytes.Equal(bytes.TrimSpace(msg.ID), rawID) {
				continue
			}
			if msg.Error != nil {
				return nil, fmt.Errorf("MCP request failed with code %d", msg.Error.Code)
			}
			if !json.Valid(msg.Result) {
				return nil, errors.New("MCP server returned an invalid response")
			}
			return msg.Result, nil
		}
	}
}

func (c *Client) write(value any) (int, error) {
	if c.stdin == nil {
		return 0, ErrNotConnected
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return 0, errors.New("encode MCP message failed")
	}
	if len(raw) > maxMessageBytes {
		return 0, errors.New("MCP message exceeds size limit")
	}
	raw = append(raw, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	n, err := c.stdin.Write(raw)
	if err != nil {
		return n, errors.New("write to MCP server failed")
	}
	return n, nil
}

func (c *Client) readLoop(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), maxMessageBytes)
	for scanner.Scan() {
		var msg rpcMessage
		if json.Unmarshal(scanner.Bytes(), &msg) != nil {
			c.dead.Store(true)
			select {
			case c.readErr <- errors.New("invalid message"):
			default:
			}
			return
		}
		select {
		case c.messages <- msg:
		case <-time.After(30 * time.Second):
			c.dead.Store(true)
			select {
			case c.readErr <- errors.New("client not consuming messages"):
			default:
			}
			return
		}
	}
	c.dead.Store(true)
	if err := scanner.Err(); err != nil {
		select {
		case c.readErr <- err:
		default:
		}
		return
	}
	select {
	case c.readErr <- io.EOF:
	default:
	}
}

func allowedSet(names []string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, name := range names {
		if name != "" {
			m[name] = true
		}
	}
	return m
}

func validateAccessConfig(cfg Config) error {
	allow, reads, writes := allowedSet(cfg.AllowedTools), allowedSet(cfg.ReadTools), allowedSet(cfg.WriteTools)
	for name := range reads {
		if !allow[name] {
			return fmt.Errorf("read tool %q is not in the allowlist", name)
		}
		if writes[name] {
			return fmt.Errorf("tool %q cannot be both read and write", name)
		}
	}
	for name := range writes {
		if !allow[name] {
			return fmt.Errorf("write tool %q is not in the allowlist", name)
		}
	}
	return nil
}

func scrubbedEnv(allow []string) []string {
	allowed := map[string]bool{"PATH": true, "HOME": true, "USER": true, "TMPDIR": true, "TMP": true, "TEMP": true, "SYSTEMROOT": true, "WINDIR": true, "LANG": true, "LC_ALL": true}
	for _, key := range allow {
		key = strings.TrimSpace(key)
		if key != "" && !strings.ContainsAny(key, "=\x00") && !containsSecretMarker(strings.ToUpper(key)) {
			allowed[key] = true
		}
	}
	keys := make([]string, 0, len(allowed))
	for key := range allowed {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var env []string
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func containsSecretMarker(key string) bool {
	for _, marker := range []string{"TOKEN", "SECRET", "PASSWORD", "CREDENTIAL", "API_KEY", "APIKEY", "AUTH"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}
