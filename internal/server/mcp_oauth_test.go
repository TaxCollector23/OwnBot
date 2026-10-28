package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"ownbot/internal/mcp"
)

func TestMCPPublicOAuthPKCELifecycleAndSecretBoundary(t *testing.T) {
	var mu sync.Mutex
	var registeredRedirect string
	var exchangedVerifier string
	var revokedToken string
	var clientID string
	var accessToken = "fixture-access-secret"
	expiresIn := int64(3600)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource":
			_ = json.NewEncoder(w).Encode(mcp.ProtectedResourceMetadata{Resource: "http://" + r.Host + "/mcp", AuthorizationServers: []string{"http://" + r.Host}})
		case "/.well-known/oauth-authorization-server":
			_ = json.NewEncoder(w).Encode(mcp.AuthorizationServerMetadata{
				Issuer: "http://" + r.Host, AuthorizationEndpoint: "http://" + r.Host + "/authorize",
				TokenEndpoint: "http://" + r.Host + "/token", RegistrationEndpoint: "http://" + r.Host + "/register",
				RevocationEndpoint: "http://" + r.Host + "/revoke", CodeChallengeMethodsSupported: []string{"S256"},
				ResponseTypesSupported: []string{"code"}, GrantTypesSupported: []string{"authorization_code", "refresh_token"},
				TokenEndpointAuthMethodsSupported: []string{"none"},
			})
		case "/register":
			var body struct {
				RedirectURIs []string `json:"redirect_uris"`
				ClientID     string   `json:"client_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || r.Header.Get("Content-Type") != "application/json" || len(body.RedirectURIs) != 1 {
				t.Errorf("bad dynamic registration: %+v %v", body, err)
				http.Error(w, "bad registration", 400)
				return
			}
			mu.Lock()
			registeredRedirect = body.RedirectURIs[0]
			clientID = "fixture-client"
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"client_id": "fixture-client", "client_secret": "must-not-be-stored"})
		case "/token":
			_ = r.ParseForm()
			mu.Lock()
			grantType := r.Form.Get("grant_type")
			if grantType == "authorization_code" {
				exchangedVerifier = r.Form.Get("code_verifier")
				if r.Form.Get("redirect_uri") != registeredRedirect || r.Form.Get("client_id") != clientID || r.Form.Get("code") != "fixture-code" {
					mu.Unlock()
					http.Error(w, "bad exchange", 400)
					return
				}
				accessToken = "fixture-access-secret"
			} else if grantType == "refresh_token" {
				if r.Form.Get("refresh_token") != "fixture-refresh-secret" {
					mu.Unlock()
					http.Error(w, "bad refresh", 400)
					return
				}
				accessToken = "fixture-refreshed-secret"
			}
			token := accessToken
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(mcp.OAuthTokenResponse{AccessToken: token, TokenType: "Bearer", RefreshToken: "fixture-refresh-secret", ExpiresIn: &expiresIn})
		case "/revoke":
			_ = r.ParseForm()
			mu.Lock()
			revokedToken = r.Form.Get("token")
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case "/mcp":
			mu.Lock()
			wantToken := accessToken
			mu.Unlock()
			if r.Header.Get("Authorization") != "Bearer "+wantToken {
				w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="http://`+r.Host+`/.well-known/oauth-protected-resource"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			var request struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if json.NewDecoder(r.Body).Decode(&request) != nil {
				http.Error(w, "bad MCP request", 400)
				return
			}
			if len(request.ID) == 0 {
				w.WriteHeader(http.StatusAccepted)
				return
			}
			var result any
			switch request.Method {
			case "initialize":
				result = map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "fixture", "version": "1"}}
			case "tools/list":
				result = map[string]any{"tools": []any{map[string]any{"name": "read_profile", "inputSchema": map[string]any{"type": "object", "additionalProperties": false}}}}
			default:
				result = map[string]any{}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()

	s, token := testApp(t)
	s.oauthHTTP = mcp.OAuthHTTPClient{HTTPClient: provider.Client(), AllowLoopbackHTTP: true}
	h := s.Handler()
	create := `{"name":"Fixture MCP","transport":"streamable_http","endpoint":"` + provider.URL + `/mcp","allowed_tools":["read_profile"],"read_tools":["read_profile"],"write_tools":[]}`
	w := request(t, h, "POST", "/api/mcp/servers", create, "127.0.0.1:8765", "http://127.0.0.1:8765", token)
	if w.Code != http.StatusCreated {
		t.Fatalf("create MCP config: %d %s", w.Code, w.Body.String())
	}
	var config MCPServerConfig
	if err := json.Unmarshal(w.Body.Bytes(), &config); err != nil {
		t.Fatal(err)
	}
	path := "/api/mcp/servers/" + config.ID
	w = request(t, h, "POST", path+"/oauth/start", `{}`, "127.0.0.1:8765", "http://127.0.0.1:8765", token)
	if w.Code != http.StatusOK {
		t.Fatalf("start OAuth: %d %s", w.Code, w.Body.String())
	}
	var auth struct {
		AuthorizationURL string `json:"authorization_url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &auth); err != nil {
		t.Fatal(err)
	}
	authURL, err := url.Parse(auth.AuthorizationURL)
	if err != nil || authURL.Query().Get("code_challenge_method") != "S256" || authURL.Query().Get("state") == "" {
		t.Fatalf("authorization URL missing PKCE/state: %s", auth.AuthorizationURL)
	}
	mu.Lock()
	redirect := registeredRedirect
	mu.Unlock()
	if redirect == "" {
		t.Fatal("dynamic registration did not capture callback")
	}
	s.oauthMu.Lock()
	var flow *pendingMCPOAuth
	for _, flow = range s.oauthFlows {
		break
	}
	s.oauthMu.Unlock()
	if flow == nil {
		t.Fatal("OAuth flow was not held in memory")
	}
	callback, _ := url.Parse(redirect)
	aliasReq, _ := http.NewRequest(http.MethodGet, redirect+"?"+url.Values{"state": {flow.state}, "code": {"fixture-code"}}.Encode(), nil)
	aliasReq.Host = "localhost:" + callback.Port()
	aliasResp, err := http.DefaultClient.Do(aliasReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = aliasResp.Body.Close()
	if aliasResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("localhost alias bypassed exact loopback callback: %d", aliasResp.StatusCode)
	}
	originReq, _ := http.NewRequest(http.MethodGet, redirect+"?"+url.Values{"state": {flow.state}, "code": {"fixture-code"}}.Encode(), nil)
	originReq.Header.Set("Origin", "http://evil.example")
	originResp, err := http.DefaultClient.Do(originReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = originResp.Body.Close()
	if originResp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign callback Origin was accepted: %d", originResp.StatusCode)
	}
	callbackQuery := url.Values{"state": {"bad-state"}, "code": {"fixture-code"}}
	resp, err := http.Get(redirect + "?" + callbackQuery.Encode())
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad state response: %d", resp.StatusCode)
	}
	callbackQuery.Set("state", flow.state)
	resp, err = http.Get(redirect + "?" + callbackQuery.Encode())
	if err != nil {
		t.Fatal(err)
	}
	callbackBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || strings.Contains(string(callbackBody), "fixture-access-secret") {
		t.Fatalf("callback leaked credentials or failed: %d %s", resp.StatusCode, callbackBody)
	}
	mu.Lock()
	verifier := exchangedVerifier
	mu.Unlock()
	if verifier != flow.verifier || callback.Host != flow.listener.Addr().String() {
		t.Fatalf("code exchange did not use the flow's PKCE verifier and exact redirect: verifier=%q expected=%q host=%q redirect=%q", verifier, flow.verifier, callback.Host, redirect)
	}
	if _, err := http.Get(redirect + "?" + callbackQuery.Encode()); err == nil {
		t.Fatal("replayed callback remained available")
	}
	var saved int
	if err := s.db.QueryRow(`SELECT count(*) FROM mcp_oauth_tokens WHERE server_id=?`, config.ID).Scan(&saved); err != nil || saved != 1 {
		t.Fatalf("local OAuth token not persisted: %d %v", saved, err)
	}
	w = request(t, h, "GET", "/api/mcp/servers", "", "127.0.0.1:8765", "", token)
	if strings.Contains(w.Body.String(), "fixture-access-secret") || strings.Contains(w.Body.String(), "fixture-refresh-secret") || strings.Contains(w.Body.String(), "must-not-be-stored") {
		t.Fatal("OAuth credential escaped through MCP configuration API")
	}
	var listed []MCPServerConfig
	if json.Unmarshal(w.Body.Bytes(), &listed) != nil || len(listed) != 1 || listed[0].ID != config.ID || !listed[0].OAuthAuthorized {
		t.Fatalf("configuration API did not report only authorization state: %s", w.Body.String())
	}
	w = request(t, h, "GET", "/api/tools", "", "127.0.0.1:8765", "", token)
	if strings.Contains(w.Body.String(), config.ID) || strings.Contains(w.Body.String(), "read_profile") {
		t.Fatal("OAuth sign-in granted tools before explicit MCP connect")
	}

	// Expired access tokens refresh before the HTTP MCP client uses them.
	if _, err := s.db.Exec(`UPDATE mcp_oauth_tokens SET expires_at=? WHERE server_id=?`, time.Now().Add(-time.Minute).Unix(), config.ID); err != nil {
		t.Fatal(err)
	}
	gotToken, err := s.accessTokenForMCP(t.Context(), config.ID)
	if err != nil || gotToken != "fixture-refreshed-secret" {
		t.Fatalf("refresh failed: token=%q error=%v", gotToken, err)
	}
	w = request(t, h, "POST", path+"/connect", `{}`, "127.0.0.1:8765", "http://127.0.0.1:8765", token)
	if w.Code != http.StatusOK {
		t.Fatalf("connect with saved OAuth grant: %d %s", w.Code, w.Body.String())
	}
	w = request(t, h, "GET", "/api/tools", "", "127.0.0.1:8765", "", token)
	if !strings.Contains(w.Body.String(), "read_profile") {
		t.Fatalf("explicit connect did not expose discovered tool: %s", w.Body.String())
	}
	var mcpAuthCount int
	if err := s.db.QueryRow(`SELECT count(*) FROM agent_settings`).Scan(&mcpAuthCount); err != nil || mcpAuthCount != 0 {
		t.Fatalf("OAuth auto-created an agent grant: rows=%d err=%v", mcpAuthCount, err)
	}

	w = request(t, h, "POST", path+"/disconnect", `{}`, "127.0.0.1:8765", "http://127.0.0.1:8765", token)
	if w.Code != http.StatusOK {
		t.Fatalf("disconnect: %d %s", w.Code, w.Body.String())
	}
	mu.Lock()
	wasRevoked := revokedToken == "fixture-refresh-secret"
	mu.Unlock()
	if !wasRevoked {
		t.Fatal("disconnect did not request provider token revocation")
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM mcp_oauth_tokens WHERE server_id=?`, config.ID).Scan(&saved); err != nil || saved != 0 {
		t.Fatalf("disconnect retained local token: %d %v", saved, err)
	}
	w = request(t, h, "GET", "/api/tools", "", "127.0.0.1:8765", "", token)
	if strings.Contains(w.Body.String(), "read_profile") {
		t.Fatal("disconnect retained local tool bindings")
	}

	// Provider denial consumes the state and does not save credentials.
	w = request(t, h, "POST", path+"/oauth/start", `{}`, "127.0.0.1:8765", "http://127.0.0.1:8765", token)
	if w.Code != http.StatusOK {
		t.Fatalf("restart denied OAuth flow: %d %s", w.Code, w.Body.String())
	}
	s.oauthMu.Lock()
	flow = nil
	for _, flow = range s.oauthFlows {
		break
	}
	s.oauthMu.Unlock()
	if flow == nil {
		t.Fatal("denial flow missing")
	}
	denied, err := http.Get(flow.redirect + "?" + url.Values{"state": {flow.state}, "error": {"access_denied"}}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	_ = denied.Body.Close()
	if denied.StatusCode != http.StatusForbidden {
		t.Fatalf("provider denial response: %d", denied.StatusCode)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM mcp_oauth_tokens WHERE server_id=?`, config.ID).Scan(&saved); err != nil || saved != 0 {
		t.Fatalf("provider denial left OAuth credentials: %d %v", saved, err)
	}
	w = request(t, h, "POST", path+"/oauth/start", `{}`, "127.0.0.1:8765", "http://127.0.0.1:8765", token)
	if w.Code != http.StatusOK {
		t.Fatalf("start expiring OAuth flow: %d %s", w.Code, w.Body.String())
	}
	s.oauthMu.Lock()
	flow = nil
	for _, flow = range s.oauthFlows {
		break
	}
	if flow != nil {
		flow.expires = time.Now().Add(-time.Second)
	}
	s.oauthMu.Unlock()
	if flow == nil {
		t.Fatal("expiry flow missing")
	}
	expired, err := http.Get(flow.redirect + "?" + url.Values{"state": {flow.state}, "code": {"fixture-code"}}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	_ = expired.Body.Close()
	if expired.StatusCode != http.StatusGone {
		t.Fatalf("expired flow was accepted: %d", expired.StatusCode)
	}
	s.oauthMu.Lock()
	remaining := len(s.oauthFlows)
	s.oauthMu.Unlock()
	if remaining != 0 {
		t.Fatalf("expired flow remained live: %d", remaining)
	}
	if err := s.saveOAuthToken(storedMCPOAuth{
		ServerID: config.ID, Owner: "admin", ClientID: "fixture-client", AccessToken: "fixture-access-secret",
		Refresh: "fixture-invalid-refresh", ExpiresAt: time.Now().Add(-time.Minute).Unix(), Discovery: flow.discovery,
	}); err != nil {
		t.Fatal(err)
	}
	if refreshed, err := s.accessTokenForMCP(t.Context(), config.ID); err != nil || refreshed != "" {
		t.Fatalf("failed refresh should remove availability without returning a token: %q %v", refreshed, err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM mcp_oauth_tokens WHERE server_id=?`, config.ID).Scan(&saved); err != nil || saved != 0 {
		t.Fatalf("failed refresh retained OAuth availability: %d %v", saved, err)
	}
}

func TestMCPGoogleOAuthRemainsDeferredWithoutClientSetup(t *testing.T) {
	s, token := testApp(t)
	w := request(t, s.Handler(), "POST", "/api/mcp/servers", `{"name":"Google Calendar","transport":"streamable_http","endpoint":"https://calendarmcp.googleapis.com/mcp/v1","allowed_tools":["list"],"read_tools":["list"],"write_tools":[]}`, "127.0.0.1:8765", "http://127.0.0.1:8765", token)
	if w.Code != http.StatusCreated {
		t.Fatalf("save Google metadata: %d %s", w.Code, w.Body.String())
	}
	var config MCPServerConfig
	if json.Unmarshal(w.Body.Bytes(), &config) != nil {
		t.Fatal("could not parse Google MCP config")
	}
	w = request(t, s.Handler(), "POST", "/api/mcp/servers/"+config.ID+"/oauth/start", `{}`, "127.0.0.1:8765", "http://127.0.0.1:8765", token)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "TODO.md") {
		t.Fatalf("Google OAuth should remain deferred: %d %s", w.Code, w.Body.String())
	}
}

func TestMCPOAuthStartRejectsLANHostsAndForeignOrigins(t *testing.T) {
	s, token := testApp(t)
	w := request(t, s.Handler(), "POST", "/api/mcp/servers", `{"name":"Hosted MCP","transport":"streamable_http","endpoint":"https://mcp.example/mcp","allowed_tools":["read"],"read_tools":["read"],"write_tools":[]}`, "127.0.0.1:8765", "http://127.0.0.1:8765", token)
	if w.Code != http.StatusCreated {
		t.Fatalf("create MCP config: %d %s", w.Code, w.Body.String())
	}
	var config MCPServerConfig
	if json.Unmarshal(w.Body.Bytes(), &config) != nil {
		t.Fatal("could not parse MCP config")
	}
	path := "/api/mcp/servers/" + config.ID + "/oauth/start"
	for _, tc := range []struct{ name, host, origin string }{
		{"LAN host", "192.168.1.2:8765", "http://192.168.1.2:8765"},
		{"foreign origin", "127.0.0.1:8765", "http://evil.example"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := request(t, s.Handler(), "POST", path, `{}`, tc.host, tc.origin, token)
			if w.Code != http.StatusForbidden {
				t.Fatalf("OAuth start accepted %s: %d %s", tc.name, w.Code, w.Body.String())
			}
		})
	}
	s.oauthMu.Lock()
	pending := len(s.oauthFlows)
	s.oauthMu.Unlock()
	if pending != 0 {
		t.Fatalf("rejected OAuth request created %d loopback callbacks", pending)
	}
}

func TestMCPServiceCatalogReportsOAuthAvailabilityWithoutCredentials(t *testing.T) {
	s, token := testApp(t)
	w := request(t, s.Handler(), "GET", "/api/mcp/services", "", "127.0.0.1:8765", "", token)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "client_secret") || strings.Contains(w.Body.String(), "access_token") || strings.Contains(w.Body.String(), "api_key") {
		t.Fatalf("service catalog leaked or failed: %d %s", w.Code, w.Body.String())
	}
	var services []mcp.Service
	if json.Unmarshal(w.Body.Bytes(), &services) != nil {
		t.Fatalf("invalid service catalog: %s", w.Body.String())
	}
	for _, service := range services {
		if service.ID == "notion" || service.ID == "calendly" {
			if !service.OAuthAvailable || !service.NoAgentGrants {
				t.Fatalf("public OAuth service unavailable or grants tools: %+v", service)
			}
		}
		if service.ID == "google_calendar" && (service.OAuthAvailable || !strings.Contains(service.Notice, "TODO.md")) {
			t.Fatalf("Google setup should remain deferred: %+v", service)
		}
	}
}
