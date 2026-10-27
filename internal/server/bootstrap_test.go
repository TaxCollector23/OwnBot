package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func localBootstrapRequest(t *testing.T, h http.Handler, method, path, body, host, origin, token, remote string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host, r.RemoteAddr = host, remote
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func issueLocalBootstrap(t *testing.T, s *Server, owner string) string {
	t.Helper()
	w := localBootstrapRequest(t, s.Handler(), http.MethodPost, "/api/access/bootstrap", `{}`, "127.0.0.1:8765", "http://127.0.0.1:8765", owner, "127.0.0.1:50000")
	if w.Code != http.StatusCreated {
		t.Fatalf("create bootstrap: %d %s", w.Code, w.Body.String())
	}
	var result struct {
		Ticket string `json:"ticket"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Ticket == "" {
		t.Fatalf("bootstrap response: %s err=%v", w.Body.String(), err)
	}
	return result.Ticket
}

func exchangeLocalBootstrap(t *testing.T, s *Server, ticket, host, origin, remote string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"ticket": ticket})
	return localBootstrapRequest(t, s.Handler(), http.MethodPost, "/api/access/bootstrap/exchange", string(body), host, origin, "", remote)
}

func TestLocalOwnerBootstrapExchangesTicketForScopedSession(t *testing.T) {
	s, owner := testApp(t)
	ticket := issueLocalBootstrap(t, s, owner)
	w := exchangeLocalBootstrap(t, s, ticket, "127.0.0.1:8765", "http://127.0.0.1:8765", "127.0.0.1:50001")
	if w.Code != http.StatusCreated {
		t.Fatalf("exchange: %d %s", w.Code, w.Body.String())
	}
	var session struct {
		Token   string `json:"token"`
		Expires string `json:"expires_at"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil || session.Token == "" || session.Token == owner {
		t.Fatalf("session response: %s err=%v", w.Body.String(), err)
	}
	expires, err := time.Parse(time.RFC3339Nano, session.Expires)
	if err != nil || time.Until(expires) <= 0 || time.Until(expires) > localSessionLifetime {
		t.Fatalf("invalid session expiry %q err=%v", session.Expires, err)
	}
	if r := localBootstrapRequest(t, s.Handler(), http.MethodGet, "/api/access", "", "127.0.0.1:8765", "", session.Token, "127.0.0.1:50002"); r.Code != http.StatusOK {
		t.Fatalf("owner session rejected locally: %d %s", r.Code, r.Body.String())
	}
	if r := localBootstrapRequest(t, s.Handler(), http.MethodGet, "/api/devices", "", "127.0.0.1:8765", "", session.Token, "127.0.0.1:50002"); r.Code != http.StatusOK {
		t.Fatalf("owner session cannot list devices: %d %s", r.Code, r.Body.String())
	}
	pairing := localBootstrapRequest(t, s.Handler(), http.MethodPost, "/api/devices/pairing", `{}`, "127.0.0.1:8765", "http://127.0.0.1:8765", session.Token, "127.0.0.1:50002")
	if pairing.Code != http.StatusCreated || !strings.Contains(pairing.Body.String(), `"pairing_url"`) {
		t.Fatalf("owner session cannot create pairing link: %d %s", pairing.Code, pairing.Body.String())
	}
	if err := s.ConfigureLAN(testLANAuthority, testLANFingerprint); err != nil {
		t.Fatal(err)
	}
	if r := request(t, s.Handler(), http.MethodPost, "/api/access/bootstrap", `{}`, "127.0.0.1:8765", "http://127.0.0.1:8765", session.Token); r.Code != http.StatusUnauthorized {
		t.Fatalf("session minted another bootstrap: %d", r.Code)
	}
	if r := request(t, s.Handler(), http.MethodPost, "/api/daemon/shutdown", `{}`, "127.0.0.1:8765", "http://127.0.0.1:8765", session.Token); r.Code != http.StatusUnauthorized {
		t.Fatalf("session shut down daemon: %d", r.Code)
	}
	if r := exchangeLocalBootstrap(t, s, ticket, "127.0.0.1:8765", "http://127.0.0.1:8765", "127.0.0.1:50001"); r.Code != http.StatusUnauthorized {
		t.Fatalf("replayed ticket accepted: %d %s", r.Code, r.Body.String())
	}
	if r := lanRequest(t, s.LANHandler(), http.MethodGet, "/api/agents", "", "https://"+testLANAuthority, session.Token); r.Code != http.StatusUnauthorized {
		t.Fatalf("local owner session was accepted by LAN handler: %d %s", r.Code, r.Body.String())
	}
	s.localAuthMu.Lock()
	s.localSessions[tokenHash(session.Token)] = time.Now().Add(-time.Second)
	s.localAuthMu.Unlock()
	if r := localBootstrapRequest(t, s.Handler(), http.MethodGet, "/api/access", "", "127.0.0.1:8765", "", session.Token, "127.0.0.1:50002"); r.Code != http.StatusUnauthorized {
		t.Fatalf("expired owner session accepted: %d %s", r.Code, r.Body.String())
	}
}

func TestLocalBootstrapRequiresLoopbackAndExactMatchingOrigin(t *testing.T) {
	s, owner := testApp(t)
	ticket := issueLocalBootstrap(t, s, owner)
	if r := exchangeLocalBootstrap(t, s, ticket, "127.0.0.1:8765", "http://127.0.0.1:8765", "192.168.1.12:50000"); r.Code != http.StatusForbidden {
		t.Fatalf("remote peer redeemed ticket: %d", r.Code)
	}
	if r := exchangeLocalBootstrap(t, s, ticket, "127.0.0.1:8765", "https://127.0.0.1:8765", "127.0.0.1:50000"); r.Code != http.StatusForbidden {
		t.Fatalf("scheme-mismatched origin redeemed ticket: %d", r.Code)
	}
	if r := exchangeLocalBootstrap(t, s, ticket, "127.0.0.1:8765", "http://evil.example", "127.0.0.1:50000"); r.Code != http.StatusForbidden {
		t.Fatalf("wrong origin redeemed ticket: %d", r.Code)
	}
	if r := exchangeLocalBootstrap(t, s, ticket, "127.0.0.1:8765", "http://127.0.0.1:8765", "127.0.0.1:50000"); r.Code != http.StatusCreated {
		t.Fatalf("legitimate local redemption failed: %d %s", r.Code, r.Body.String())
	}
}

func TestLocalBootstrapTicketExpires(t *testing.T) {
	s, owner := testApp(t)
	ticket := issueLocalBootstrap(t, s, owner)
	s.localAuthMu.Lock()
	s.bootstrapTickets[tokenHash(ticket)] = time.Now().Add(-time.Second)
	s.localAuthMu.Unlock()
	if r := exchangeLocalBootstrap(t, s, ticket, "127.0.0.1:8765", "http://127.0.0.1:8765", "127.0.0.1:50000"); r.Code != http.StatusUnauthorized {
		t.Fatalf("expired ticket accepted: %d", r.Code)
	}
}

func TestLANPairedDeviceCannotRedeemLocalOwnerBootstrap(t *testing.T) {
	s, owner := configuredLAN(t)
	ticket := issueLocalBootstrap(t, s, owner)
	secret, _ := createPairingLink(t, s, owner)
	pairedToken, _ := pair(t, s, secret, "bootstrap isolation")
	body, err := json.Marshal(map[string]string{"ticket": ticket})
	if err != nil {
		t.Fatal(err)
	}
	lan := lanRequest(t, s.LANHandler(), http.MethodPost, "/api/access/bootstrap/exchange", string(body), "https://"+testLANAuthority, pairedToken)
	if lan.Code != http.StatusForbidden {
		t.Fatalf("LAN paired device reached local bootstrap exchange: %d %s", lan.Code, lan.Body.String())
	}
	local := exchangeLocalBootstrap(t, s, ticket, "127.0.0.1:8765", "http://127.0.0.1:8765", "127.0.0.1:50000")
	if local.Code != http.StatusCreated {
		t.Fatalf("LAN rejection consumed local bootstrap ticket: %d %s", local.Code, local.Body.String())
	}
}

func TestBootstrapExchangeHandlerRejectsNonLoopbackAndWrongOrigin(t *testing.T) {
	s, owner := testApp(t)
	ticket := issueLocalBootstrap(t, s, owner)
	body, _ := json.Marshal(map[string]string{"ticket": ticket})
	r := httptest.NewRequest(http.MethodPost, "/api/access/bootstrap/exchange", strings.NewReader(string(body)))
	r.Host = "127.0.0.1:8765"
	r.RemoteAddr = "192.168.1.2:1234"
	r.Header.Set("Origin", "http://127.0.0.1:8765")
	w := httptest.NewRecorder()
	s.exchangeLocalBootstrap(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("direct exchange handler accepted non-loopback peer: %d", w.Code)
	}
	r = httptest.NewRequest(http.MethodPost, "/api/access/bootstrap/exchange", strings.NewReader(string(body)))
	r.Host = "127.0.0.1:8765"
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Origin", "https://127.0.0.1:8765")
	w = httptest.NewRecorder()
	s.exchangeLocalBootstrap(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("direct exchange handler accepted wrong origin scheme: %d", w.Code)
	}
}
