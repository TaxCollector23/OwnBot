package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestOAuthDiscoveryRegistrationAndPKCE(t *testing.T) {
	var registered map[string]any
	var exchange url.Values
	expires := int64(60)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mcp":
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+"http://"+r.Host+`/.well-known/oauth-protected-resource"`)
			w.WriteHeader(http.StatusUnauthorized)
		case "/.well-known/oauth-protected-resource":
			_ = json.NewEncoder(w).Encode(ProtectedResourceMetadata{Resource: "http://" + r.Host + "/mcp", AuthorizationServers: []string{"http://" + r.Host}})
		case "/.well-known/oauth-authorization-server":
			_ = json.NewEncoder(w).Encode(AuthorizationServerMetadata{
				Issuer: "http://" + r.Host, AuthorizationEndpoint: "http://" + r.Host + "/authorize",
				TokenEndpoint: "http://" + r.Host + "/token", RegistrationEndpoint: "http://" + r.Host + "/register",
				RevocationEndpoint: "http://" + r.Host + "/revoke", CodeChallengeMethodsSupported: []string{"S256"},
				ResponseTypesSupported: []string{"code"}, GrantTypesSupported: []string{"authorization_code"},
				TokenEndpointAuthMethodsSupported: []string{"none"},
			})
		case "/register":
			if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("registration request was not JSON: %s %s", r.Method, r.Header.Get("Content-Type"))
			}
			_ = json.NewDecoder(r.Body).Decode(&registered)
			_ = json.NewEncoder(w).Encode(map[string]string{"client_id": "public-client"})
		case "/token":
			_ = r.ParseForm()
			exchange = r.Form
			_ = json.NewEncoder(w).Encode(OAuthTokenResponse{AccessToken: "access-secret", TokenType: "Bearer", RefreshToken: "refresh-secret", ExpiresIn: &expires})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()

	client := OAuthHTTPClient{HTTPClient: provider.Client(), AllowLoopbackHTTP: true}
	discovery, err := client.Discover(context.Background(), provider.URL+"/mcp")
	if err != nil {
		t.Fatal(err)
	}
	redirect := "http://127.0.0.1:43210/oauth/callback"
	registration, err := client.Register(context.Background(), discovery, redirect)
	if err != nil {
		t.Fatal(err)
	}
	if registration.ClientID != "public-client" || registered["token_endpoint_auth_method"] != "none" {
		t.Fatalf("registration did not create a public client: %#v / %#v", registration, registered)
	}
	if redirectURIs, ok := registered["redirect_uris"].([]any); !ok || len(redirectURIs) != 1 || redirectURIs[0] != redirect {
		t.Fatalf("registration changed the exact callback: %#v", registered)
	}
	verifier := "random-pkce-verifier-value"
	challenge := PKCEChallenge(verifier)
	state := "one-use-state"
	authorizationURL, err := client.AuthorizationURL(discovery, registration.ClientID, redirect, state, challenge)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := url.Parse(authorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	q := auth.Query()
	if q.Get("state") != state || q.Get("code_challenge") != challenge || q.Get("code_challenge_method") != "S256" || q.Get("resource") != discovery.Resource.Resource {
		t.Fatalf("authorization request lost PKCE/state/resource: %s", authorizationURL)
	}
	token, err := client.Exchange(context.Background(), discovery, registration.ClientID, "one-time-code", redirect, verifier)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(exchange.Get("code_verifier")))
	if exchange.Get("code") != "one-time-code" || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge || token.AccessToken != "access-secret" {
		t.Fatalf("token exchange did not use the PKCE verifier: form=%v token=%+v", exchange, token)
	}
}

func TestOAuthRejectsInsecureAndUnexpectedRedirects(t *testing.T) {
	for _, raw := range []string{
		"http://example.com/oauth", "https://user@example.com/oauth", "https://example.com/oauth?x=1", "https://example.com/oauth#frag",
	} {
		if err := validateOAuthURL(raw, false); err == nil {
			t.Errorf("accepted unsafe OAuth URL %q", raw)
		}
	}
	for _, raw := range []string{
		"http://localhost:4000/oauth/callback", "http://127.0.0.1/oauth/callback", "http://127.0.0.1:4000/other", "https://127.0.0.1:4000/oauth/callback",
	} {
		if err := validateRedirectURI(raw); err == nil {
			t.Errorf("accepted non-exact OAuth callback %q", raw)
		}
	}
	if !isPublicIP([]byte{8, 8, 8, 8}) || isPublicIP([]byte{10, 0, 0, 1}) || isPublicIP([]byte{127, 0, 0, 1}) || !isPublicIP(net.ParseIP("2001:4860:4860::8888")) || isPublicIP(net.ParseIP("2001:db8::1")) {
		t.Fatal("public-address guard accepted a private address or rejected public DNS")
	}
	if _, err := bearerResourceMetadata(`Basic resource_metadata="https://example.com/prm"`); err == nil {
		t.Fatal("accepted a non-Bearer challenge")
	}
	resourceMetadata, err := bearerResourceMetadata(`Bearer realm="mcp", resource_metadata="https://example.com/prm"`)
	if err != nil || resourceMetadata != "https://example.com/prm" {
		t.Fatalf("did not parse valid RFC 9728 challenge: %q %v", resourceMetadata, err)
	}
	for _, raw := range []string{"https://127.0.0.1/", "https://localhost/", "https://100.64.0.1/"} {
		req, _ := http.NewRequest(http.MethodGet, raw, nil)
		if _, err := (guardedOAuthTransport{}).RoundTrip(req); err == nil {
			t.Errorf("network guard allowed private OAuth target %q", raw)
		}
	}
}
