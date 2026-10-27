package server

import (
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// privatePeerListener supplies a synthetic private address because macOS
// routes connections to this host's own LAN IP through lo0. It lets this test
// exercise a real TLS socket and LAN middleware without claiming a second-peer
// network test.
type privatePeerListener struct{ net.Listener }

func (l privatePeerListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return privatePeerConn{Conn: c}, nil
}

type privatePeerConn struct{ net.Conn }

func (c privatePeerConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("192.168.1.25"), Port: 53124}
}

func TestLANTransportRequiresTLSAndPairsDevice(t *testing.T) {
	s, owner := configuredLAN(t)
	defer s.Close()

	ts := httptest.NewUnstartedServer(s.LANHandler())
	authority := ts.Listener.Addr().String()
	if err := s.ConfigureLAN(authority, testLANFingerprint); err != nil {
		t.Fatal(err)
	}
	ts.Listener = privatePeerListener{ts.Listener}
	ts.StartTLS()
	defer ts.Close()

	client := ts.Client()
	client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // test certificate is self-signed
	do := func(method, path, token string, body io.Reader) (*http.Response, error) {
		req, err := http.NewRequest(method, ts.URL+path, body)
		if err != nil {
			return nil, err
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", "https://"+authority)
		}
		return client.Do(req)
	}

	// Plain HTTP sent to the TLS socket must not reach any handler successfully.
	plain, err := http.Get("http://" + authority + "/api/status")
	if err == nil {
		defer plain.Body.Close()
		if plain.StatusCode == http.StatusOK {
			t.Fatal("plain HTTP succeeded on the LAN TLS listener")
		}
	}

	resp, err := do(http.MethodGet, "/api/agents", owner, nil)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("owner bearer on LAN: %d", resp.StatusCode)
	}

	secret, _ := createPairingLink(t, s, owner)
	payload, err := json.Marshal(map[string]string{"secret": secret, "name": "fixture device"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err = do(http.MethodPost, "/api/devices/pair", "", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	var paired struct {
		Token  string `json:"token"`
		Device Device `json:"device"`
	}
	err = json.NewDecoder(resp.Body).Decode(&paired)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated || paired.Token == "" || paired.Device.ID == "" {
		t.Fatalf("pair over TLS: status=%d device=%+v", resp.StatusCode, paired.Device)
	}

	resp, err = do(http.MethodGet, "/api/agents", paired.Token, nil)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("paired token over TLS: %d", resp.StatusCode)
	}

	resp, err = do(http.MethodDelete, "/api/devices/"+paired.Device.ID, paired.Token, nil)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("device revoke attempt: %d", resp.StatusCode)
	}
}
