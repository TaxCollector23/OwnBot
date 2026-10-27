package server

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const testLANAuthority = "ownbot.local:9443"
const testLANFingerprint = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func configuredLAN(t *testing.T) (*Server, string) {
	t.Helper()
	s, owner := testApp(t)
	if err := s.ConfigureLAN(testLANAuthority, testLANFingerprint); err != nil {
		t.Fatal(err)
	}
	return s, owner
}

func lanRequest(t *testing.T, h http.Handler, method, path, body, origin, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = testLANAuthority
	r.RemoteAddr = "192.168.1.25:53124"
	r.TLS = &tls.ConnectionState{}
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

func createPairingLink(t *testing.T, s *Server, owner string) (string, string) {
	t.Helper()
	w := request(t, s.Handler(), "POST", "/api/devices/pairing", "{}", "127.0.0.1:8765", "http://127.0.0.1:8765", owner)
	if w.Code != 201 {
		t.Fatalf("pairing link: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		PairingURL  string `json:"pairing_url"`
		ExpiresAt   string `json:"expires_at"`
		Fingerprint string `json:"certificate_fingerprint"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(response.PairingURL)
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.TrimPrefix(u.Fragment, "pair=")
	if secret == "" || response.Fingerprint != "01:23:45:67:89:AB:CD:EF:01:23:45:67:89:AB:CD:EF:01:23:45:67:89:AB:CD:EF:01:23:45:67:89:AB:CD:EF" {
		t.Fatalf("unexpected pairing response: %+v", response)
	}
	return secret, response.ExpiresAt
}

func pair(t *testing.T, s *Server, secret, name string) (string, Device) {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"secret": secret, "name": name})
	w := lanRequest(t, s.LANHandler(), "POST", "/api/devices/pair", string(b), "https://"+testLANAuthority, "")
	if w.Code != 201 {
		t.Fatalf("pair: %d %s", w.Code, w.Body.String())
	}
	var result struct {
		Token  string `json:"token"`
		Device Device `json:"device"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Token, result.Device
}

func TestLANRequiresTLSExactHostPrivatePeerAndMatchingOrigin(t *testing.T) {
	s, _ := configuredLAN(t)
	h := s.LANHandler()
	r := httptest.NewRequest("GET", "https://"+testLANAuthority+"/api/status", nil)
	r.Host = "evil.local:9443"
	r.RemoteAddr = "192.168.1.5:1"
	r.TLS = &tls.ConnectionState{}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("host: %d", w.Code)
	}
	r = httptest.NewRequest("GET", "http://"+testLANAuthority+"/api/status", nil)
	r.Host = testLANAuthority
	r.RemoteAddr = "192.168.1.5:1"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("TLS: %d", w.Code)
	}
	r = httptest.NewRequest("GET", "https://"+testLANAuthority+"/api/status", nil)
	r.Host = testLANAuthority
	r.RemoteAddr = "8.8.8.8:1"
	r.TLS = &tls.ConnectionState{}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("public peer: %d", w.Code)
	}
	if w = lanRequest(t, h, "GET", "/api/status", "", "https://evil.local:9443", ""); w.Code != 403 {
		t.Fatalf("origin: %d", w.Code)
	}
	if w = lanRequest(t, h, "GET", "/api/status", "", "", ""); w.Code != 200 || strings.Contains(w.Body.String(), "agents") {
		t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}
}

func TestPairingIsOneUseAndOwnerTokenIsRejectedOnLAN(t *testing.T) {
	s, owner := configuredLAN(t)
	if w := lanRequest(t, s.LANHandler(), "GET", "/api/agents", "", "", owner); w.Code != 401 {
		t.Fatalf("owner token accepted: %d", w.Code)
	}
	secret, _ := createPairingLink(t, s, owner)
	token, device := pair(t, s, secret, "Phone")
	if token == owner || device.ID == "" || device.Name != "Phone" {
		t.Fatalf("bad paired response: %+v", device)
	}
	if w := lanRequest(t, s.LANHandler(), "POST", "/api/devices/pair", `{"secret":"`+secret+`","name":"Again"}`, "", ""); w.Code != 401 {
		t.Fatalf("pairing reused: %d %s", w.Code, w.Body.String())
	}
	if w := lanRequest(t, s.LANHandler(), "GET", "/api/agents", "", "", token); w.Code != 200 {
		t.Fatalf("device token rejected: %d %s", w.Code, w.Body.String())
	}
}

func TestPairingExpiresAndPersistedDeviceCanBeRevoked(t *testing.T) {
	dir := privateTestDir(t)
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	owner := s.token
	if err = s.ConfigureLAN(testLANAuthority, testLANFingerprint); err != nil {
		t.Fatal(err)
	}
	secret, _ := createPairingLink(t, s, owner)
	if _, err = s.db.Exec(`UPDATE device_pairings SET expires_at=? WHERE secret_hash=?`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), tokenHash(secret)); err != nil {
		t.Fatal(err)
	}
	if w := lanRequest(t, s.LANHandler(), "POST", "/api/devices/pair", `{"secret":"`+secret+`","name":"Expired"}`, "", ""); w.Code != 401 {
		t.Fatalf("expired pairing: %d", w.Code)
	}
	secret, _ = createPairingLink(t, s, owner)
	token, device := pair(t, s, secret, "Tablet")
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.ConfigureLAN(testLANAuthority, testLANFingerprint); err != nil {
		t.Fatal(err)
	}
	if w := lanRequest(t, s.LANHandler(), "GET", "/api/agents", "", "", token); w.Code != 200 {
		t.Fatalf("persisted token: %d %s", w.Code, w.Body.String())
	}
	w := request(t, s.Handler(), "DELETE", "/api/devices/"+device.ID, "{}", "127.0.0.1:8765", "http://127.0.0.1:8765", owner)
	if w.Code != 200 {
		t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
	}
	if w = lanRequest(t, s.LANHandler(), "GET", "/api/agents", "", "", token); w.Code != 401 {
		t.Fatalf("revoked token: %d", w.Code)
	}
	w = request(t, s.Handler(), "GET", "/api/devices", "", "127.0.0.1:8765", "", owner)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"revoked_at":"`) {
		t.Fatalf("device record: %d %s", w.Code, w.Body.String())
	}
}

func TestPairingPruneUsesChronologicalFractionalTimestamps(t *testing.T) {
	s, _ := testApp(t)
	secretHash := tokenHash("a valid test pairing secret")
	expires, err := time.Parse(time.RFC3339Nano, "2026-10-27T12:00:00.12Z")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO device_pairings(secret_hash,expires_at,created_at) VALUES(?,?,?)`, secretHash, expires.Format(time.RFC3339Nano), "2026-10-27T11:55:00Z"); err != nil {
		t.Fatal(err)
	}
	checkAt, err := time.Parse(time.RFC3339Nano, "2026-10-27T12:00:00.1Z")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.prunePairings(checkAt); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.db.QueryRow(`SELECT count(*) FROM device_pairings WHERE secret_hash=?`, secretHash).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("unexpired pairing with a wider fractional timestamp was pruned; count=%d", count)
	}
}
