package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type principalContextKey struct{}

type pairAttempt struct {
	started time.Time
	count   int
}

type Device struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	RevokedAt string `json:"revoked_at"`
}

// ConfigureLAN stores the exact HTTPS authority and certificate fingerprint
// used by the listener. Configure it before exposing LANHandler.
func (s *Server) ConfigureLAN(authority, fingerprint string) error {
	if s == nil {
		return errors.New("server is nil")
	}
	if strings.Contains(authority, "://") || strings.ContainsAny(authority, "/?#@ \t\r\n") {
		return errors.New("LAN authority must be a host and optional port")
	}
	host := authority
	if h, _, err := net.SplitHostPort(authority); err == nil {
		host = h
	} else if strings.Contains(authority, ":") && net.ParseIP(strings.Trim(authority, "[]")) == nil {
		return errors.New("LAN authority must include a port for non-default ports")
	}
	if host == "" {
		return errors.New("LAN authority is required")
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip == nil {
		for _, label := range strings.Split(host, ".") {
			if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return errors.New("invalid LAN authority host")
			}
			for _, c := range label {
				if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
					return errors.New("invalid LAN authority host")
				}
			}
		}
	}
	fp := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(fingerprint)), "sha256:")
	fp = strings.ReplaceAll(fp, ":", "")
	if len(fp) != 64 {
		return errors.New("certificate fingerprint must be SHA-256")
	}
	if _, err := hex.DecodeString(fp); err != nil {
		return errors.New("certificate fingerprint must be SHA-256")
	}
	var formatted strings.Builder
	for i := 0; i < len(fp); i += 2 {
		if i > 0 {
			formatted.WriteByte(':')
		}
		formatted.WriteString(strings.ToUpper(fp[i : i+2]))
	}
	s.lanMu.Lock()
	s.lanAuthority, s.lanFingerprint = authority, formatted.String()
	s.lanMu.Unlock()
	return nil
}

// LANHandler exposes the dashboard and APIs to paired private-network peers.
// HTTPS is enforced by requiring the request to arrive on a TLS connection.
func (s *Server) LANHandler() http.Handler { return s.lanSecurity(s.mux) }

func (s *Server) lanSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		s.lanMu.RLock()
		authority := s.lanAuthority
		s.lanMu.RUnlock()
		if authority == "" {
			http.Error(w, "LAN access is not configured", http.StatusServiceUnavailable)
			return
		}
		if r.TLS == nil {
			http.Error(w, "HTTPS required", http.StatusForbidden)
			return
		}
		if r.Host != authority {
			http.Error(w, "host rejected", http.StatusForbidden)
			return
		}
		if !privatePeer(r.RemoteAddr) {
			http.Error(w, "private network peer required", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if origin := r.Header.Get("Origin"); origin != "" && !matchingLANOrigin(origin, authority) {
				http.Error(w, "origin rejected", http.StatusForbidden)
				return
			}
			if r.URL.Path == "/api/status" && r.Method == http.MethodGet {
				write(w, 200, map[string]any{"ok": true, "service": "ownbot", "lan": true})
				return
			}
			if r.URL.Path == "/api/devices/pair" && r.Method == http.MethodPost {
				s.pairDevice(w, r)
				return
			}
			// Local owner bootstrap/session endpoints are never reachable through
			// the paired-device listener. Keep the whole access route family
			// blocked, including future subroutes.
			if strings.HasPrefix(r.URL.Path, "/api/access") || r.URL.Path == "/api/devices/pairing" || r.URL.Path == "/api/devices" || r.URL.Path == "/api/daemon/shutdown" || strings.HasPrefix(r.URL.Path, "/api/devices/") {
				http.Error(w, "local administrator required", http.StatusForbidden)
				return
			}
			// The websocket upgrade is authorized by its principal-bound one-use ticket.
			if r.URL.Path == "/api/events" && r.Method == http.MethodGet {
				next.ServeHTTP(w, r)
				return
			}
			if r.URL.Path != "/api/devices/pair" {
				principal, ok := s.authenticateDevice(r.Header.Get("Authorization"))
				if !ok {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				r = r.WithContext(context.WithValue(r.Context(), principalContextKey{}, principal))
			}
		}
		next.ServeHTTP(w, r)
	})
}

func privatePeer(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast()
}

func matchingLANOrigin(raw, authority string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == authority && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}

func (s *Server) authenticateDevice(header string) (string, bool) {
	if !strings.HasPrefix(header, "Bearer ") {
		return "", false
	}
	token := strings.TrimPrefix(header, "Bearer ")
	if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) == 1 {
		return "", false
	}
	hash := tokenHash(token)
	var id string
	err := s.db.QueryRow(`SELECT id FROM paired_devices WHERE token_hash=? AND revoked_at=''`, hash).Scan(&id)
	if err != nil {
		return "", false
	}
	return "device:" + id, true
}

func (s *Server) validEventPrincipal(principal string) bool {
	if principal == "admin" {
		return true
	}
	if !strings.HasPrefix(principal, "device:") {
		return false
	}
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM paired_devices WHERE id=? AND revoked_at=''`, strings.TrimPrefix(principal, "device:")).Scan(&n)
	return err == nil && n == 1
}

func tokenHash(token string) string {
	b := sha256.Sum256([]byte(token))
	return hex.EncodeToString(b[:])
}

func (s *Server) access(w http.ResponseWriter, _ *http.Request) {
	s.lanMu.RLock()
	authority, fingerprint := s.lanAuthority, s.lanFingerprint
	s.lanMu.RUnlock()
	urls := []string{}
	if authority != "" {
		urls = append(urls, "https://"+authority+"/dashboard")
	}
	write(w, 200, map[string]any{"enabled": authority != "", "urls": urls, "certificate_fingerprint": fingerprint})
}

func (s *Server) devices(w http.ResponseWriter, _ *http.Request) {
	rows, err := s.db.Query(`SELECT id,name,created_at,revoked_at FROM paired_devices ORDER BY created_at,id`)
	if err != nil {
		errJSON(w, 500, "could not read devices")
		return
	}
	defer rows.Close()
	items := []Device{}
	for rows.Next() {
		var d Device
		if err = rows.Scan(&d.ID, &d.Name, &d.CreatedAt, &d.RevokedAt); err != nil {
			errJSON(w, 500, "could not read devices")
			return
		}
		items = append(items, d)
	}
	if err = rows.Err(); err != nil {
		errJSON(w, 500, "could not read devices")
		return
	}
	write(w, 200, items)
}

func (s *Server) createPairing(w http.ResponseWriter, _ *http.Request) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		errJSON(w, 500, "could not create pairing link")
		return
	}
	raw := base64.RawURLEncoding.EncodeToString(secret)
	expires := time.Now().UTC().Add(5 * time.Minute)
	if err := s.prunePairings(time.Now().UTC()); err != nil {
		errJSON(w, 500, "could not create pairing link")
		return
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM device_pairings`).Scan(&count); err != nil || count >= 128 {
		errJSON(w, 429, "too many outstanding pairing links")
		return
	}
	if _, err := s.db.Exec(`INSERT INTO device_pairings(secret_hash,expires_at,created_at) VALUES(?,?,?)`, tokenHash(raw), expires.Format(time.RFC3339Nano), now()); err != nil {
		errJSON(w, 500, "could not create pairing link")
		return
	}
	s.lanMu.RLock()
	authority, fingerprint := s.lanAuthority, s.lanFingerprint
	s.lanMu.RUnlock()
	s.event("device.pairing.created", "admin", map[string]any{"expires_at": expires.Format(time.RFC3339Nano)})
	write(w, 201, map[string]any{"pairing_url": "https://" + authority + "/dashboard#pair=" + raw, "expires_at": expires.Format(time.RFC3339Nano), "certificate_fingerprint": fingerprint})
}

// prunePairings parses persisted timestamps instead of comparing RFC3339Nano
// strings. RFC3339Nano omits trailing fractional zeros, so lexical ordering
// can disagree with chronological ordering within the same second.
func (s *Server) prunePairings(at time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT secret_hash,expires_at,used_at FROM device_pairings`)
	if err != nil {
		return err
	}
	type stalePairing struct{ hash string }
	stale := []stalePairing{}
	for rows.Next() {
		var hash, expiry, used string
		if err = rows.Scan(&hash, &expiry, &used); err != nil {
			rows.Close()
			return err
		}
		if used != "" {
			stale = append(stale, stalePairing{hash: hash})
			continue
		}
		expires, parseErr := time.Parse(time.RFC3339Nano, expiry)
		if parseErr != nil || !at.Before(expires) {
			stale = append(stale, stalePairing{hash: hash})
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, item := range stale {
		if _, err = tx.Exec(`DELETE FROM device_pairings WHERE secret_hash=?`, item.hash); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Server) pairDevice(w http.ResponseWriter, r *http.Request) {
	ip := remoteIP(r.RemoteAddr)
	if !s.allowPairAttempt(ip) {
		errJSON(w, 429, "pairing rate limit reached")
		return
	}
	var req struct {
		Secret string `json:"secret"`
		Name   string `json:"name"`
	}
	if err := decode(r, &req); err != nil {
		errJSON(w, 400, "invalid pairing request")
		return
	}
	if len(req.Secret) < 40 || len(req.Secret) > 64 {
		errJSON(w, 401, "pairing link is invalid or expired")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Paired device"
	}
	if len(name) > 80 {
		errJSON(w, 400, "device name must be 80 characters or fewer")
		return
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		errJSON(w, 500, "could not pair device")
		return
	}
	token := hex.EncodeToString(tokenBytes)
	device := Device{ID: newID(), Name: name, CreatedAt: now(), RevokedAt: ""}
	tx, err := s.db.Begin()
	if err != nil {
		errJSON(w, 500, "could not pair device")
		return
	}
	defer tx.Rollback()
	var expiry string
	err = tx.QueryRow(`SELECT expires_at FROM device_pairings WHERE secret_hash=? AND used_at=''`, tokenHash(req.Secret)).Scan(&expiry)
	if err != nil {
		errJSON(w, 401, "pairing link is invalid or expired")
		return
	}
	expires, parseErr := time.Parse(time.RFC3339Nano, expiry)
	if parseErr != nil || !time.Now().Before(expires) {
		errJSON(w, 401, "pairing link is invalid or expired")
		return
	}
	res, err := tx.Exec(`UPDATE device_pairings SET used_at=? WHERE secret_hash=? AND used_at=''`, now(), tokenHash(req.Secret))
	if err != nil {
		errJSON(w, 500, "could not pair device")
		return
	}
	changed, _ := res.RowsAffected()
	if changed != 1 {
		errJSON(w, 401, "pairing link is invalid or expired")
		return
	}
	if _, err = tx.Exec(`INSERT INTO paired_devices(id,name,token_hash,created_at) VALUES(?,?,?,?)`, device.ID, device.Name, tokenHash(token), device.CreatedAt); err != nil {
		errJSON(w, 500, "could not pair device")
		return
	}
	if err = tx.Commit(); err != nil {
		errJSON(w, 500, "could not pair device")
		return
	}
	s.event("device.paired", device.ID, map[string]any{"name": device.Name})
	write(w, 201, map[string]any{"token": token, "device": device})
}

func (s *Server) allowPairAttempt(ip string) bool {
	s.pairMu.Lock()
	defer s.pairMu.Unlock()
	now := time.Now()
	for key, state := range s.pairAttempts {
		if now.Sub(state.started) > time.Minute {
			delete(s.pairAttempts, key)
		}
	}
	state, ok := s.pairAttempts[ip]
	if !ok {
		if len(s.pairAttempts) >= 256 {
			return false
		}
		state = pairAttempt{started: now}
	}
	if state.count >= 10 {
		s.pairAttempts[ip] = state
		return false
	}
	state.count++
	s.pairAttempts[ip] = state
	return true
}

func remoteIP(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err == nil {
		return host
	}
	return remote
}

func (s *Server) revokeDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	res, err := s.db.Exec(`UPDATE paired_devices SET revoked_at=? WHERE id=? AND revoked_at=''`, now(), id)
	if err != nil {
		errJSON(w, 500, "could not revoke device")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		errJSON(w, 404, "device not found")
		return
	}
	s.event("device.revoked", "admin", map[string]string{"device_id": id})
	write(w, 200, map[string]bool{"revoked": true})
}

// SetShutdown registers the daemon's graceful shutdown callback.
func (s *Server) SetShutdown(shutdown func()) {
	s.shutdownMu.Lock()
	s.shutdown = shutdown
	s.shutdownMu.Unlock()
}

func (s *Server) shutdownDaemon(w http.ResponseWriter, _ *http.Request) {
	s.shutdownMu.RLock()
	shutdown := s.shutdown
	s.shutdownMu.RUnlock()
	if shutdown == nil {
		errJSON(w, 503, "shutdown is unavailable")
		return
	}
	write(w, 202, map[string]bool{"shutting_down": true})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go shutdown()
}

func (s *Server) LANAuthority() string {
	s.lanMu.RLock()
	defer s.lanMu.RUnlock()
	return s.lanAuthority
}
