package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ownbot/internal/mcp"
)

const (
	oauthFlowTTL  = 10 * time.Minute
	maxOAuthFlows = 16
)

var (
	errLocalOAuthState    = errors.New("local OAuth state could not be changed")
	errProviderRevocation = errors.New("provider token revocation could not be confirmed")
)

type pendingMCPOAuth struct {
	state     string
	serverID  string
	owner     string
	clientID  string
	verifier  string
	redirect  string
	discovery mcp.OAuthDiscovery
	expires   time.Time
	listener  net.Listener
	server    *http.Server
	timer     *time.Timer
}

type storedMCPOAuth struct {
	ServerID    string
	Owner       string
	ClientID    string
	AccessToken string
	Refresh     string
	ExpiresAt   int64
	Discovery   mcp.OAuthDiscovery
	Scope       string
}

func oauthRandom(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", errors.New("could not start secure OAuth flow")
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *Server) startMCPOAuth(w http.ResponseWriter, r *http.Request) {
	c, err := s.loadMCP(r.PathValue("id"))
	if err != nil {
		errJSON(w, 404, "MCP server not found")
		return
	}
	if c.Transport != "streamable_http" {
		errJSON(w, 409, "OAuth sign-in is available only for HTTP MCP servers")
		return
	}
	providerHost := ""
	if endpoint, parseErr := url.Parse(c.Endpoint); parseErr == nil {
		providerHost = strings.ToLower(endpoint.Hostname())
	}
	if c.ID == "google_calendar" || c.ID == "email" || providerHost == "calendarmcp.googleapis.com" || providerHost == "gmailmcp.googleapis.com" {
		errJSON(w, 409, "Google OAuth requires the client setup documented in TODO.md")
		return
	}
	owner, _ := r.Context().Value(principalContextKey{}).(string)
	if owner == "" {
		errJSON(w, 401, "local owner authentication required")
		return
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		errJSON(w, 500, "could not start loopback OAuth callback")
		return
	}
	redirect := "http://" + listener.Addr().String() + "/oauth/callback"
	discovery, err := s.oauthHTTP.Discover(r.Context(), c.Endpoint)
	if err != nil {
		_ = listener.Close()
		errJSON(w, 409, "MCP server did not provide safe OAuth metadata or PKCE support")
		return
	}
	registration, err := s.oauthHTTP.Register(r.Context(), discovery, redirect)
	if err != nil {
		_ = listener.Close()
		errJSON(w, 409, "MCP authorization server does not support public-client registration")
		return
	}
	state, err := oauthRandom(32)
	if err != nil {
		_ = listener.Close()
		errJSON(w, 500, "could not start secure OAuth flow")
		return
	}
	verifier, err := oauthRandom(32)
	if err != nil {
		_ = listener.Close()
		errJSON(w, 500, "could not start secure OAuth flow")
		return
	}
	authorizationURL, err := s.oauthHTTP.AuthorizationURL(discovery, registration.ClientID, redirect, state, mcp.PKCEChallenge(verifier))
	if err != nil {
		_ = listener.Close()
		errJSON(w, 409, "MCP authorization server metadata is invalid")
		return
	}
	flow := &pendingMCPOAuth{
		state: state, serverID: c.ID, owner: owner, clientID: registration.ClientID,
		verifier: verifier, redirect: redirect, discovery: discovery,
		expires: time.Now().Add(oauthFlowTTL), listener: listener,
	}
	flow.server = &http.Server{ReadHeaderTimeout: 3 * time.Second, MaxHeaderBytes: 16 << 10, Handler: http.HandlerFunc(func(cw http.ResponseWriter, cr *http.Request) {
		s.handleMCPOAuthCallback(cw, cr, flow)
	})}
	s.mcpMu.Lock()
	s.oauthMu.Lock()
	s.pruneOAuthFlowsLocked(time.Now())
	for _, existing := range s.oauthFlows {
		if existing.serverID == c.ID {
			s.oauthMu.Unlock()
			s.mcpMu.Unlock()
			_ = listener.Close()
			errJSON(w, 409, "OAuth sign-in is already waiting for this MCP server")
			return
		}
	}
	for _, existing := range s.oauthExchanges {
		if existing.serverID == c.ID {
			s.oauthMu.Unlock()
			s.mcpMu.Unlock()
			_ = listener.Close()
			errJSON(w, 409, "OAuth sign-in is already finishing for this MCP server")
			return
		}
	}
	if len(s.oauthFlows)+len(s.oauthExchanges) >= maxOAuthFlows {
		s.oauthMu.Unlock()
		s.mcpMu.Unlock()
		_ = listener.Close()
		errJSON(w, 429, "too many pending MCP sign-ins")
		return
	}
	s.registry.RemoveServer(c.ID)
	_ = s.hosts.Disconnect(c.ID)
	s.oauthFlows[state] = flow
	flow.timer = time.AfterFunc(oauthFlowTTL, func() { s.expireOAuthFlow(flow) })
	s.oauthMu.Unlock()
	s.mcpMu.Unlock()
	s.event("mcp.oauth.started", "local-user", map[string]any{"server_id": c.ID})
	go func() {
		_ = flow.server.Serve(listener)
	}()
	write(w, 200, map[string]any{
		"authorization_url": authorizationURL,
		"expires_at":        flow.expires.UTC().Format(time.RFC3339),
		"callback_url":      redirect,
		"callback_port":     listener.Addr().(*net.TCPAddr).Port,
		"connected":         false,
		"grants_created":    false,
	})
}

func (s *Server) pruneOAuthFlowsLocked(now time.Time) {
	for state, flow := range s.oauthFlows {
		if now.After(flow.expires) {
			delete(s.oauthFlows, state)
			if flow.timer != nil {
				flow.timer.Stop()
			}
			go flow.server.Close()
		}
	}
}

func (s *Server) handleMCPOAuthCallback(w http.ResponseWriter, r *http.Request, flow *pendingMCPOAuth) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	if r.Method != http.MethodGet || r.URL.Path != "/oauth/callback" || r.URL.RawPath != "" || callbackHostError(r, flow.listener) || r.ContentLength > 0 || len(r.TransferEncoding) != 0 {
		http.Error(w, "Invalid OAuth callback.", http.StatusBadRequest)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
		http.Error(w, "OAuth callback origin did not match.", http.StatusForbidden)
		return
	}
	if len(r.URL.RawQuery) > 8192 {
		http.Error(w, "Invalid OAuth callback.", http.StatusBadRequest)
		return
	}
	query, queryErr := url.ParseQuery(r.URL.RawQuery)
	if queryErr != nil {
		http.Error(w, "Invalid OAuth callback.", http.StatusBadRequest)
		return
	}
	for key, values := range query {
		if len(values) != 1 || key != "state" && key != "code" && key != "error" && key != "error_description" && key != "error_uri" && key != "iss" {
			http.Error(w, "Invalid OAuth callback.", http.StatusBadRequest)
			return
		}
	}
	gotState := query.Get("state")
	if gotState == "" || subtle.ConstantTimeCompare([]byte(gotState), []byte(flow.state)) != 1 {
		http.Error(w, "OAuth state did not match.", http.StatusBadRequest)
		return
	}
	if iss := query.Get("iss"); iss != "" && iss != flow.discovery.Server.Issuer {
		http.Error(w, "OAuth authorization server did not match discovery.", http.StatusBadRequest)
		return
	}
	if query.Get("error") != "" && query.Get("code") != "" {
		http.Error(w, "Invalid OAuth callback.", http.StatusBadRequest)
		return
	}
	if len(query.Get("error_description")) > 1024 || len(query.Get("error_uri")) > 2048 {
		http.Error(w, "Invalid OAuth callback.", http.StatusBadRequest)
		return
	}
	s.oauthMu.Lock()
	stored := s.oauthFlows[flow.state]
	if stored != flow || time.Now().After(flow.expires) {
		if stored == flow {
			delete(s.oauthFlows, flow.state)
			if flow.timer != nil {
				flow.timer.Stop()
			}
		}
		s.oauthMu.Unlock()
		go flow.server.Close()
		http.Error(w, "OAuth sign-in expired. Start again from OwnBot.", http.StatusGone)
		return
	}
	delete(s.oauthFlows, flow.state) // consume state before any remote token exchange
	s.oauthExchanges[flow.state] = flow
	if flow.timer != nil {
		flow.timer.Stop()
	}
	s.oauthMu.Unlock()
	defer func() { go flow.server.Close() }()
	defer s.finishOAuthExchange(flow)
	if query.Get("error") != "" {
		s.event("mcp.oauth.denied", "local-user", map[string]any{"server_id": flow.serverID})
		http.Error(w, "Provider sign-in was denied. Start again from OwnBot if you want to retry.", http.StatusForbidden)
		return
	}
	code := query.Get("code")
	if code == "" || len(code) > 4096 || strings.ContainsAny(code, "\r\n\x00") {
		http.Error(w, "Provider returned an invalid authorization code.", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	token, err := s.oauthHTTP.Exchange(ctx, flow.discovery, flow.clientID, code, flow.redirect, flow.verifier)
	if err != nil {
		http.Error(w, "Provider token exchange failed. Start again from OwnBot.", http.StatusBadGateway)
		return
	}
	grant := storedMCPOAuth{
		ServerID: flow.serverID, Owner: flow.owner, ClientID: flow.clientID,
		AccessToken: token.AccessToken, Refresh: token.RefreshToken,
		ExpiresAt: tokenExpiry(token.ExpiresIn), Discovery: flow.discovery, Scope: token.Scope,
	}
	// Serialize persistence with clear/disconnect. If either operation removed
	// this exchange while the provider request was in flight, discard its result.
	s.oauthMu.Lock()
	if s.oauthExchanges[flow.state] != flow {
		s.oauthMu.Unlock()
		http.Error(w, "OAuth sign-in was cleared. Start again from OwnBot.", http.StatusGone)
		return
	}
	s.oauthTokenMu.Lock()
	err = s.saveOAuthTokenLocked(grant)
	s.oauthTokenMu.Unlock()
	s.oauthMu.Unlock()
	if err != nil {
		http.Error(w, "Could not save local authorization. Start again from OwnBot.", http.StatusInternalServerError)
		return
	}
	s.event("mcp.oauth.authorized", "local-user", map[string]any{"server_id": flow.serverID})
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "OwnBot sign-in is complete. Return to your terminal or dashboard. No tools were granted to an agent.\n")
}

func tokenExpiry(expiresIn *int64) int64 {
	if expiresIn == nil {
		return 0
	}
	if *expiresIn <= 0 {
		return time.Now().Unix()
	}
	return time.Now().Add(time.Duration(*expiresIn) * time.Second).Unix()
}

func (s *Server) saveOAuthToken(token storedMCPOAuth) error {
	s.oauthTokenMu.Lock()
	defer s.oauthTokenMu.Unlock()
	return s.saveOAuthTokenLocked(token)
}

func (s *Server) saveOAuthTokenLocked(token storedMCPOAuth) error {
	metadata, err := json.Marshal(token.Discovery)
	if err != nil {
		return errors.New("could not encode OAuth metadata")
	}
	_, err = s.db.Exec(`INSERT INTO mcp_oauth_tokens(server_id,owner,client_id,access_token,refresh_token,expires_at,discovery_json,scope,updated_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(server_id) DO UPDATE SET owner=excluded.owner,client_id=excluded.client_id,access_token=excluded.access_token,refresh_token=excluded.refresh_token,expires_at=excluded.expires_at,discovery_json=excluded.discovery_json,scope=excluded.scope,updated_at=excluded.updated_at`, token.ServerID, token.Owner, token.ClientID, token.AccessToken, token.Refresh, token.ExpiresAt, string(metadata), token.Scope, now())
	if err != nil {
		return errors.New("could not save local OAuth authorization")
	}
	return nil
}

func (s *Server) loadOAuthToken(serverID string) (storedMCPOAuth, error) {
	var token storedMCPOAuth
	var metadata string
	if err := s.db.QueryRow(`SELECT server_id,owner,client_id,access_token,refresh_token,expires_at,discovery_json,scope FROM mcp_oauth_tokens WHERE server_id=?`, serverID).Scan(&token.ServerID, &token.Owner, &token.ClientID, &token.AccessToken, &token.Refresh, &token.ExpiresAt, &metadata, &token.Scope); err != nil {
		return token, err
	}
	if err := json.Unmarshal([]byte(metadata), &token.Discovery); err != nil {
		return storedMCPOAuth{}, errors.New("saved OAuth metadata is invalid")
	}
	return token, nil
}

func (s *Server) deleteOAuthTokenLocked(serverID string) error {
	_, err := s.db.Exec(`DELETE FROM mcp_oauth_tokens WHERE server_id=?`, serverID)
	return err
}

func (s *Server) accessTokenForMCP(ctx context.Context, serverID string) (string, error) {
	s.oauthTokenMu.Lock()
	defer s.oauthTokenMu.Unlock()
	token, err := s.loadOAuthToken(serverID)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "", err
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("could not read local MCP authorization")
	}
	if err != nil {
		return "", nil // no stored authorization: try unauthenticated MCP and surface its 401
	}
	if token.Owner != "admin" && token.Owner != "local-user" {
		return "", errors.New("MCP authorization owner mismatch")
	}
	if token.ExpiresAt == 0 || time.Now().Unix() < token.ExpiresAt-30 {
		return token.AccessToken, nil
	}
	if token.Refresh == "" {
		_ = s.deleteOAuthTokenLocked(serverID)
		return "", nil
	}
	refresh, err := s.oauthHTTP.Refresh(ctx, token.Discovery, token.ClientID, token.Refresh)
	if err != nil {
		_ = s.deleteOAuthTokenLocked(serverID)
		return "", nil
	}
	token.AccessToken = refresh.AccessToken
	token.Refresh = refresh.RefreshToken
	token.ExpiresAt = tokenExpiry(refresh.ExpiresIn)
	if refresh.Scope != "" {
		token.Scope = refresh.Scope
	}
	if err = s.saveOAuthTokenLocked(token); err != nil {
		return "", errors.New("could not refresh local MCP authorization")
	}
	return token.AccessToken, nil
}

func (s *Server) clearMCPOAuth(w http.ResponseWriter, r *http.Request) {
	serverID := r.PathValue("id")
	if _, err := s.loadMCP(serverID); err != nil {
		errJSON(w, 404, "MCP server not found")
		return
	}
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	s.removePendingOAuth(serverID)
	s.registry.RemoveServer(serverID)
	_ = s.hosts.Disconnect(serverID)
	err := s.clearOAuthToken(r.Context(), serverID)
	if errors.Is(err, errLocalOAuthState) {
		errJSON(w, 500, "could not read local MCP authorization")
		return
	}
	if errors.Is(err, errProviderRevocation) {
		s.event("mcp.oauth.cleared", "local-user", map[string]any{"server_id": serverID})
		write(w, 502, map[string]any{"server_id": serverID, "authorized": false, "notice": "local access was removed; provider revocation could not be confirmed"})
		return
	}
	s.event("mcp.oauth.cleared", "local-user", map[string]any{"server_id": serverID})
	write(w, 200, map[string]any{"server_id": serverID, "authorized": false})
}

func (s *Server) clearOAuthToken(ctx context.Context, serverID string) error {
	s.oauthTokenMu.Lock()
	defer s.oauthTokenMu.Unlock()
	token, err := s.loadOAuthToken(serverID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: could not read local MCP authorization", errLocalOAuthState)
	}
	var revokeErr error
	if token.Discovery.Server.RevocationEndpoint != "" {
		revokeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		revokeErr = s.oauthHTTP.Revoke(revokeCtx, token.Discovery, token.ClientID, token.AccessToken, token.Refresh)
		cancel()
	}
	if err = s.deleteOAuthTokenLocked(serverID); err != nil {
		return fmt.Errorf("%w: could not clear local MCP authorization", errLocalOAuthState)
	}
	if revokeErr != nil {
		return fmt.Errorf("%w", errProviderRevocation)
	}
	return nil
}

func (s *Server) removePendingOAuth(serverID string) {
	s.oauthMu.Lock()
	defer s.oauthMu.Unlock()
	for state, flow := range s.oauthFlows {
		if flow.serverID == serverID {
			delete(s.oauthFlows, state)
			if flow.timer != nil {
				flow.timer.Stop()
			}
			go flow.server.Close()
		}
	}
	for state, flow := range s.oauthExchanges {
		if flow.serverID == serverID {
			delete(s.oauthExchanges, state)
		}
	}
}

func (s *Server) finishOAuthExchange(flow *pendingMCPOAuth) {
	s.oauthMu.Lock()
	if s.oauthExchanges[flow.state] == flow {
		delete(s.oauthExchanges, flow.state)
	}
	s.oauthMu.Unlock()
}

func (s *Server) closePendingOAuth() {
	s.oauthMu.Lock()
	flows := make([]*pendingMCPOAuth, 0, len(s.oauthFlows))
	for state, flow := range s.oauthFlows {
		flows = append(flows, flow)
		if flow.timer != nil {
			flow.timer.Stop()
		}
		delete(s.oauthFlows, state)
	}
	for state := range s.oauthExchanges {
		delete(s.oauthExchanges, state)
	}
	s.oauthMu.Unlock()
	for _, flow := range flows {
		_ = flow.server.Close()
	}
}

func (s *Server) expireOAuthFlow(flow *pendingMCPOAuth) {
	s.oauthMu.Lock()
	if s.oauthFlows[flow.state] == flow {
		delete(s.oauthFlows, flow.state)
	}
	s.oauthMu.Unlock()
	_ = flow.server.Close()
}

func strictOAuthHost(host string) bool {
	return strings.HasPrefix(host, "127.0.0.1:") && host != "127.0.0.1:"
}

func callbackHostError(r *http.Request, listener net.Listener) bool {
	return r.Host != listener.Addr().String() || !strictOAuthHost(r.Host)
}
