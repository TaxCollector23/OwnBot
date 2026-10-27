package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// HTTPClient speaks MCP Streamable HTTP. Credentials and authorization decisions
// are supplied by its caller and are not persisted. It does not implement OAuth.
type HTTPClient struct {
	Endpoint     string
	Enabled      bool
	AllowedTools []string
	Token        func(context.Context) (string, error)
	Authorize    func(context.Context, string, json.RawMessage) error
	mu           sync.Mutex
	session      string
	nextID       int
	initialized  bool
	version      string
}

func (c *HTTPClient) validate() error {
	if !c.Enabled {
		return ErrDisabled
	}
	u, e := url.Parse(c.Endpoint)
	if e != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return errors.New("invalid MCP endpoint")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" {
		h := u.Hostname()
		ip := net.ParseIP(h)
		if h == "localhost" || (ip != nil && ip.IsLoopback()) {
			return nil
		}
	}
	return errors.New("MCP endpoint must use HTTPS or loopback HTTP")
}

// Connect initializes the protocol. It does not perform OAuth or discover tools.
func (c *HTTPClient) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.validate(); err != nil {
		return err
	}
	if c.initialized {
		return nil
	}
	result, err := c.exchange(ctx, "initialize", map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "ownbot", "version": "dev"}}, false)
	if err != nil {
		return err
	}
	var hello struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(result, &hello) != nil || hello.ProtocolVersion == "" {
		return errors.New("invalid MCP initialize response")
	}
	c.version = hello.ProtocolVersion
	if _, err = c.exchange(ctx, "notifications/initialized", nil, true); err != nil {
		return err
	}
	c.initialized = true
	return nil
}
func (c *HTTPClient) Tools(ctx context.Context) ([]Tool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.initialized {
		return nil, ErrNotConnected
	}
	var all []Tool
	cursor := ""
	seen := map[string]bool{}
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.exchange(ctx, "tools/list", params, false)
		if err != nil {
			return nil, err
		}
		var page struct {
			Tools []Tool `json:"tools"`
			Next  string `json:"nextCursor"`
		}
		if json.Unmarshal(raw, &page) != nil {
			return nil, errors.New("invalid MCP tool list")
		}
		all = append(all, page.Tools...)
		if len(all) > maxTools {
			return nil, errors.New("MCP tool list exceeds limit")
		}
		if page.Next == "" {
			break
		}
		if seen[page.Next] {
			return nil, errors.New("MCP pagination loop")
		}
		seen[page.Next] = true
		cursor = page.Next
	}
	allowed := allowedSet(c.AllowedTools)
	out := make([]Tool, 0, len(all))
	for _, t := range all {
		if allowed[t.Name] {
			out = append(out, t)
		}
	}
	return out, nil
}
func (c *HTTPClient) CallTool(ctx context.Context, name string, args json.RawMessage) (ToolResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.initialized {
		return ToolResult{}, ErrNotConnected
	}
	if !allowedSet(c.AllowedTools)[name] {
		return ToolResult{}, ErrToolNotAllowed
	}
	if !json.Valid(args) {
		return ToolResult{}, errors.New("invalid tool arguments")
	}
	if c.Authorize == nil {
		return ToolResult{}, errors.New("MCP tool authorization required")
	}
	if err := c.Authorize(ctx, name, args); err != nil {
		return ToolResult{}, errors.New("MCP tool authorization refused")
	}
	raw, err := c.exchange(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, false)
	if err != nil {
		return ToolResult{}, err
	}
	var out ToolResult
	if json.Unmarshal(raw, &out) != nil {
		return ToolResult{}, errors.New("invalid MCP tool result")
	}
	return out, nil
}
func (c *HTTPClient) exchange(ctx context.Context, method string, params any, notification bool) (json.RawMessage, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	c.nextID++
	id := c.nextID
	v := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		v["params"] = params
	}
	if !notification {
		v["id"] = id
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, errors.New("MCP request encoding failed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, errors.New("invalid MCP request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if c.session != "" {
		req.Header.Set("Mcp-Session-Id", c.session)
	}
	if c.initialized {
		req.Header.Set("MCP-Protocol-Version", c.version)
	}
	if c.Token != nil {
		token, e := c.Token(ctx)
		if e != nil {
			return nil, errors.New("MCP authentication unavailable")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("MCP request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("MCP server returned HTTP %d", resp.StatusCode)
	}
	if method == "initialize" {
		c.session = resp.Header.Get("Mcp-Session-Id")
	}
	if notification {
		return nil, nil
	}
	var msg rpcMessage
	if strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		scanner := bufio.NewScanner(io.LimitReader(resp.Body, maxMessageBytes))
		scanner.Buffer(make([]byte, 4096), maxMessageBytes)
		var lines []string
		found := false
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				if len(lines) > 0 {
					var candidate rpcMessage
					if json.Unmarshal([]byte(strings.Join(lines, "\n")), &candidate) == nil && string(candidate.ID) == fmt.Sprint(id) {
						msg = candidate
						found = true
						break
					}
					lines = nil
				}
				continue
			}
			if strings.HasPrefix(line, "data:") {
				lines = append(lines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
		if !found {
			return nil, errors.New("MCP stream missing response")
		}
	} else if err = json.NewDecoder(io.LimitReader(resp.Body, maxMessageBytes)).Decode(&msg); err != nil {
		return nil, errors.New("invalid MCP response")
	}
	if string(msg.ID) != fmt.Sprint(id) || msg.JSONRPC != "2.0" {
		return nil, errors.New("unexpected MCP response")
	}
	if msg.Error != nil {
		return nil, fmt.Errorf("MCP request failed with code %d", msg.Error.Code)
	}
	if !json.Valid(msg.Result) {
		return nil, errors.New("invalid MCP response result")
	}
	return msg.Result, nil
}
