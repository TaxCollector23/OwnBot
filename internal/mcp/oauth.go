package mcp

// OAuth support for public MCP servers. The package deliberately keeps provider
// metadata and credentials out of the HTTP/MCP configuration types.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const maxOAuthDocument = 1 << 20

var resourceMetadataParam = regexp.MustCompile(`(?i)(?:^|,)\s*resource_metadata\s*=\s*"([^"\\]+)"`)
var bearerTokenPattern = regexp.MustCompile(`^[A-Za-z0-9\-._~+/]+=*$`)

type OAuthHTTPClient struct {
	// HTTPClient is an explicit transport injection point for local fake-issuer tests.
	// Production callers leave it nil so DNS and redirect guards stay active.
	HTTPClient *http.Client
	// AllowLoopbackHTTP is for local fake-issuer tests only. Production callers
	// must leave this false; public discovery and OAuth endpoints require HTTPS.
	AllowLoopbackHTTP bool
}

type ProtectedResourceMetadata struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	ScopesSupported        []string `json:"scopes_supported"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
	ResourceDocumentation  string   `json:"resource_documentation,omitempty"`
}

type AuthorizationServerMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint"`
	RevocationEndpoint                string   `json:"revocation_endpoint,omitempty"`
	ScopesSupported                   []string `json:"scopes_supported,omitempty"`
	ResponseTypesSupported            []string `json:"response_types_supported,omitempty"`
	GrantTypesSupported               []string `json:"grant_types_supported,omitempty"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported,omitempty"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported,omitempty"`
}

type OAuthDiscovery struct {
	Resource ProtectedResourceMetadata
	Server   AuthorizationServerMetadata
}

type OAuthClientRegistration struct {
	ClientID string `json:"client_id"`
}

type OAuthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    *int64 `json:"expires_in,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

func (c OAuthHTTPClient) client() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 15 * time.Second, Transport: guardedOAuthTransport{}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type guardedOAuthTransport struct{}

func (guardedOAuthTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	u := r.URL
	if err := validateOAuthURL(u.String(), false); err != nil {
		return nil, err
	}
	transport := http.Transport{Proxy: nil, TLSHandshakeTimeout: 8 * time.Second, ResponseHeaderTimeout: 10 * time.Second, DisableKeepAlives: true}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.New("invalid OAuth endpoint")
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil || len(ips) == 0 {
			return nil, errors.New("OAuth endpoint DNS lookup failed")
		}
		for _, ip := range ips {
			if !isPublicIP(ip.IP) {
				return nil, errors.New("OAuth endpoint resolves to a non-public address")
			}
		}
		dialer := net.Dialer{Timeout: 8 * time.Second}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}
	return transport.RoundTrip(r)
}

func validateOAuthURL(raw string, allowLoopback bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.Opaque != "" {
		return errors.New("invalid OAuth URL")
	}
	if u.Scheme == "https" {
		return nil
	}
	if allowLoopback && u.Scheme == "http" {
		host := strings.ToLower(u.Hostname())
		ip := net.ParseIP(host)
		if host == "localhost" || ip != nil && ip.IsLoopback() {
			return nil
		}
	}
	return errors.New("OAuth discovery and endpoints must use HTTPS")
}

func isPublicIP(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		for _, block := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4"} {
			if ipInCIDR(ip4, block) {
				return false
			}
		}
	} else {
		// Public IPv6 unicast is limited to 2000::/3. Exclude protocol,
		// transition, and documentation blocks inside that range.
		if !ipInCIDR(ip, "2000::/3") {
			return false
		}
		for _, block := range []string{"2001::/23", "2001:10::/28", "2001:20::/28", "2001:db8::/32", "2002::/16", "3fff::/20"} {
			if ipInCIDR(ip, block) {
				return false
			}
		}
	}
	return true
}

func ipInCIDR(ip net.IP, block string) bool {
	_, subnet, err := net.ParseCIDR(block)
	return err == nil && subnet.Contains(ip)
}

// Discover obtains MCP's protected-resource metadata and the corresponding
// OAuth authorization-server metadata. A 401 challenge is required; the MCP
// endpoint itself is never inferred from provider names or user input.
func (c OAuthHTTPClient) Discover(ctx context.Context, resourceURL string) (OAuthDiscovery, error) {
	var out OAuthDiscovery
	if err := validateOAuthURL(resourceURL, c.AllowLoopbackHTTP); err != nil {
		return out, err
	}
	challenge, err := c.resourceChallenge(ctx, resourceURL)
	if err != nil {
		return out, err
	}
	resourceMetadataURL, err := bearerResourceMetadata(challenge)
	if err != nil {
		return out, err
	}
	if err = validateOAuthURL(resourceMetadataURL, c.AllowLoopbackHTTP); err != nil {
		return out, err
	}
	if err = c.getJSON(ctx, resourceMetadataURL, &out.Resource); err != nil {
		return out, err
	}
	endpoint, err := url.Parse(resourceURL)
	if err != nil || len(out.Resource.AuthorizationServers) == 0 {
		return OAuthDiscovery{}, errors.New("protected resource metadata omitted authorization servers")
	}
	resource, err := url.Parse(out.Resource.Resource)
	if err != nil || validateOAuthURL(out.Resource.Resource, c.AllowLoopbackHTTP) != nil || resource.Host != endpoint.Host || resource.Scheme != endpoint.Scheme || resource.EscapedPath() != endpoint.EscapedPath() {
		return OAuthDiscovery{}, errors.New("protected resource metadata does not match the MCP endpoint")
	}
	issuer, err := url.Parse(out.Resource.AuthorizationServers[0])
	if err != nil || validateOAuthURL(issuer.String(), c.AllowLoopbackHTTP) != nil {
		return OAuthDiscovery{}, errors.New("protected resource metadata contains an invalid authorization server")
	}
	issuerValue := issuer.String()
	metadataURL := oauthMetadataURL(issuer)
	if err = validateOAuthURL(metadataURL, c.AllowLoopbackHTTP); err != nil {
		return OAuthDiscovery{}, err
	}
	if err = c.getJSON(ctx, metadataURL, &out.Server); err != nil {
		return OAuthDiscovery{}, err
	}
	if out.Server.Issuer != issuerValue || !sameIssuer(out.Server.Issuer, issuerValue) {
		return OAuthDiscovery{}, errors.New("authorization server issuer did not match discovery")
	}
	for _, endpoint := range []string{out.Server.AuthorizationEndpoint, out.Server.TokenEndpoint, out.Server.RegistrationEndpoint} {
		if endpoint == "" || validateOAuthURL(endpoint, c.AllowLoopbackHTTP) != nil {
			return OAuthDiscovery{}, errors.New("authorization server metadata omitted a valid required endpoint")
		}
	}
	if len(out.Server.CodeChallengeMethodsSupported) > 0 && !contains(out.Server.CodeChallengeMethodsSupported, "S256") {
		return OAuthDiscovery{}, errors.New("authorization server does not support PKCE S256")
	}
	if len(out.Server.ResponseTypesSupported) > 0 && !contains(out.Server.ResponseTypesSupported, "code") {
		return OAuthDiscovery{}, errors.New("authorization server does not support authorization codes")
	}
	if len(out.Server.GrantTypesSupported) > 0 && !contains(out.Server.GrantTypesSupported, "authorization_code") {
		return OAuthDiscovery{}, errors.New("authorization server does not support authorization-code grants")
	}
	if len(out.Server.TokenEndpointAuthMethodsSupported) > 0 && !contains(out.Server.TokenEndpointAuthMethodsSupported, "none") {
		return OAuthDiscovery{}, errors.New("authorization server does not support public OAuth clients")
	}
	return out, nil
}

func (c OAuthHTTPClient) resourceChallenge(ctx context.Context, endpoint string) (string, error) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"ownbot","version":"1"}}}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", errors.New("could not request MCP authorization metadata")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := c.client().Do(req)
	if err != nil {
		return "", errors.New("could not reach MCP authorization challenge")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		return "", errors.New("MCP server did not return an OAuth authorization challenge")
	}
	return resp.Header.Get("WWW-Authenticate"), nil
}

func bearerResourceMetadata(value string) (string, error) {
	parts := strings.SplitN(strings.TrimSpace(value), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", errors.New("MCP authorization challenge omitted protected-resource metadata")
	}
	matches := resourceMetadataParam.FindAllStringSubmatch(strings.TrimSpace(parts[1]), -1)
	if len(matches) != 1 || len(matches[0]) != 2 || matches[0][1] == "" {
		return "", errors.New("MCP authorization challenge omitted protected-resource metadata")
	}
	return matches[0][1], nil
}

func oauthMetadataURL(issuer *url.URL) string {
	path := strings.TrimSuffix(issuer.Path, "/")
	issuer.Path = "/.well-known/oauth-authorization-server" + path
	issuer.RawPath = ""
	issuer.RawQuery = ""
	issuer.Fragment = ""
	return issuer.String()
}

func sameIssuer(a, b string) bool {
	ua, ea := url.Parse(a)
	ub, eb := url.Parse(b)
	return ea == nil && eb == nil && ua.Scheme == ub.Scheme && ua.Host == ub.Host && strings.TrimSuffix(ua.Path, "/") == strings.TrimSuffix(ub.Path, "/")
}

func (c OAuthHTTPClient) getJSON(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("invalid OAuth metadata request")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.client().Do(req)
	if err != nil {
		return errors.New("could not fetch OAuth metadata")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("OAuth metadata request returned HTTP %d", resp.StatusCode)
	}
	if json.NewDecoder(io.LimitReader(resp.Body, maxOAuthDocument)).Decode(out) != nil {
		return errors.New("OAuth metadata was invalid")
	}
	return nil
}

func (c OAuthHTTPClient) Register(ctx context.Context, d OAuthDiscovery, redirectURI string) (OAuthClientRegistration, error) {
	var out OAuthClientRegistration
	if err := validateRedirectURI(redirectURI); err != nil {
		return out, err
	}
	payload := map[string]any{
		"client_name": "OwnBot", "redirect_uris": []string{redirectURI},
		"grant_types":    []string{"authorization_code", "refresh_token"},
		"response_types": []string{"code"}, "token_endpoint_auth_method": "none",
	}
	if err := c.jsonPost(ctx, d.Server.RegistrationEndpoint, payload, &out); err != nil || !validOAuthOpaque(out.ClientID) || len(out.ClientID) > 1024 {
		return OAuthClientRegistration{}, errors.New("OAuth dynamic client registration failed")
	}
	return out, nil
}

func (c OAuthHTTPClient) jsonPost(ctx context.Context, endpoint string, payload, out any) error {
	if validateOAuthURL(endpoint, c.AllowLoopbackHTTP) != nil {
		return errors.New("invalid OAuth endpoint")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return errors.New("invalid OAuth request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid OAuth request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.client().Do(req)
	if err != nil {
		return errors.New("OAuth endpoint request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("OAuth endpoint returned HTTP %d", resp.StatusCode)
	}
	if json.NewDecoder(io.LimitReader(resp.Body, maxOAuthDocument)).Decode(out) != nil {
		return errors.New("OAuth response was invalid")
	}
	return nil
}

func validateRedirectURI(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/oauth/callback" {
		return errors.New("OAuth redirect must be the exact loopback callback")
	}
	return nil
}

func (c OAuthHTTPClient) AuthorizationURL(d OAuthDiscovery, clientID, redirectURI, state, challenge string) (string, error) {
	if clientID == "" || state == "" || challenge == "" || validateRedirectURI(redirectURI) != nil {
		return "", errors.New("invalid OAuth authorization request")
	}
	u, err := url.Parse(d.Server.AuthorizationEndpoint)
	if err != nil || validateOAuthURL(u.String(), c.AllowLoopbackHTTP) != nil {
		return "", errors.New("invalid OAuth authorization endpoint")
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("resource", d.Resource.Resource)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (c OAuthHTTPClient) Exchange(ctx context.Context, d OAuthDiscovery, clientID, code, redirectURI, verifier string) (OAuthTokenResponse, error) {
	var out OAuthTokenResponse
	if clientID == "" || code == "" || verifier == "" || validateRedirectURI(redirectURI) != nil {
		return out, errors.New("invalid OAuth token request")
	}
	err := c.formJSON(ctx, d.Server.TokenEndpoint, url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID},
		"redirect_uri": {redirectURI}, "code_verifier": {verifier}, "resource": {d.Resource.Resource},
	}, &out)
	if err != nil || !validBearerToken(out.AccessToken) || !validOAuthOpaque(out.RefreshToken) || !strings.EqualFold(out.TokenType, "bearer") || !validOAuthExpiry(out.ExpiresIn) || len(out.Scope) > 8192 {
		return OAuthTokenResponse{}, errors.New("OAuth token exchange failed")
	}
	return out, nil
}

func (c OAuthHTTPClient) Refresh(ctx context.Context, d OAuthDiscovery, clientID, refreshToken string) (OAuthTokenResponse, error) {
	var out OAuthTokenResponse
	if clientID == "" || refreshToken == "" {
		return out, errors.New("OAuth refresh is unavailable")
	}
	err := c.formJSON(ctx, d.Server.TokenEndpoint, url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {refreshToken},
		"client_id": {clientID}, "resource": {d.Resource.Resource},
	}, &out)
	if err != nil || !validBearerToken(out.AccessToken) || !validOAuthOpaque(out.RefreshToken) || !strings.EqualFold(out.TokenType, "bearer") || !validOAuthExpiry(out.ExpiresIn) || len(out.Scope) > 8192 {
		return OAuthTokenResponse{}, errors.New("OAuth token refresh failed")
	}
	if out.RefreshToken == "" {
		out.RefreshToken = refreshToken
	}
	return out, nil
}

func (c OAuthHTTPClient) Revoke(ctx context.Context, d OAuthDiscovery, clientID, accessToken, refreshToken string) error {
	if d.Server.RevocationEndpoint == "" {
		return nil
	}
	if validateOAuthURL(d.Server.RevocationEndpoint, c.AllowLoopbackHTTP) != nil {
		return errors.New("invalid OAuth revocation endpoint")
	}
	token, hint := accessToken, "access_token"
	if refreshToken != "" {
		token, hint = refreshToken, "refresh_token"
	}
	if token == "" {
		return nil
	}
	return c.formJSON(ctx, d.Server.RevocationEndpoint, url.Values{"token": {token}, "token_type_hint": {hint}, "client_id": {clientID}}, nil)
}

func validBearerToken(token string) bool {
	return len(token) > 0 && len(token) <= 16*1024 && bearerTokenPattern.MatchString(token)
}

func validOAuthOpaque(value string) bool {
	return len(value) <= 16*1024 && !strings.ContainsAny(value, "\r\n\x00")
}

func validOAuthExpiry(value *int64) bool {
	return value == nil || *value >= 0 && *value <= 365*24*60*60
}

func (c OAuthHTTPClient) formJSON(ctx context.Context, endpoint string, values url.Values, out any) error {
	if validateOAuthURL(endpoint, c.AllowLoopbackHTTP) != nil {
		return errors.New("invalid OAuth endpoint")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return errors.New("invalid OAuth request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.client().Do(req)
	if err != nil {
		return errors.New("OAuth endpoint request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("OAuth endpoint returned HTTP %d", resp.StatusCode)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil
	}
	if json.NewDecoder(io.LimitReader(resp.Body, maxOAuthDocument)).Decode(out) != nil {
		return errors.New("OAuth response was invalid")
	}
	return nil
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func PKCEChallenge(verifier string) string {
	hash := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}
