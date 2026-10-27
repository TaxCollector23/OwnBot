package server

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	localBootstrapLifetime = 60 * time.Second
	localSessionLifetime   = 30 * time.Minute
	maxLocalBootstraps     = 32
	maxLocalSessions       = 128
)

func (s *Server) createLocalBootstrap(w http.ResponseWriter, r *http.Request) {
	if !loopbackPeer(r.RemoteAddr) {
		errJSON(w, http.StatusForbidden, "loopback peer required")
		return
	}
	var empty struct{}
	if err := decode(r, &empty); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid bootstrap request")
		return
	}
	ticket, err := randomLocalSecret()
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not create owner session link")
		return
	}
	expires := time.Now().Add(localBootstrapLifetime)
	s.localAuthMu.Lock()
	s.pruneLocalAuthLocked(time.Now())
	if len(s.bootstrapTickets) >= maxLocalBootstraps {
		s.localAuthMu.Unlock()
		errJSON(w, http.StatusTooManyRequests, "too many outstanding owner session links")
		return
	}
	s.bootstrapTickets[tokenHash(ticket)] = expires
	s.localAuthMu.Unlock()
	write(w, http.StatusCreated, map[string]string{"ticket": ticket, "expires_at": expires.UTC().Format(time.RFC3339Nano)})
}

func (s *Server) exchangeLocalBootstrap(w http.ResponseWriter, r *http.Request) {
	// Defense in depth: this handler must only mint a local owner session for
	// an exact same-origin request from a loopback peer, even if routing is
	// accidentally mounted outside the local security middleware.
	if !loopbackPeer(r.RemoteAddr) {
		errJSON(w, http.StatusForbidden, "loopback peer required")
		return
	}
	if origin := r.Header.Get("Origin"); !matchingLoopbackOrigin(origin, r.Host, r.TLS != nil) {
		errJSON(w, http.StatusForbidden, "same-origin request required")
		return
	}
	var request struct {
		Ticket string `json:"ticket"`
	}
	if err := decode(r, &request); err != nil || len(request.Ticket) < 40 || len(request.Ticket) > 64 {
		errJSON(w, http.StatusUnauthorized, "owner session link is invalid or expired")
		return
	}
	hash := tokenHash(request.Ticket)
	nowTime := time.Now()
	s.localAuthMu.Lock()
	expires, ok := s.bootstrapTickets[hash]
	if ok {
		delete(s.bootstrapTickets, hash)
	}
	s.pruneLocalAuthLocked(nowTime)
	s.localAuthMu.Unlock()
	if !ok || !nowTime.Before(expires) {
		errJSON(w, http.StatusUnauthorized, "owner session link is invalid or expired")
		return
	}
	session, err := randomLocalSecret()
	if err != nil {
		errJSON(w, http.StatusInternalServerError, "could not create owner session")
		return
	}
	sessionExpires := nowTime.Add(localSessionLifetime)
	s.localAuthMu.Lock()
	s.pruneLocalAuthLocked(time.Now())
	if len(s.localSessions) >= maxLocalSessions {
		s.localAuthMu.Unlock()
		errJSON(w, http.StatusTooManyRequests, "too many active owner sessions")
		return
	}
	s.localSessions[tokenHash(session)] = sessionExpires
	s.localAuthMu.Unlock()
	write(w, http.StatusCreated, map[string]string{"token": session, "expires_at": sessionExpires.UTC().Format(time.RFC3339Nano)})
}

func randomLocalSecret() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", errors.New("secure random source unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func (s *Server) validLocalSession(token string) bool {
	nowTime := time.Now()
	s.localAuthMu.Lock()
	defer s.localAuthMu.Unlock()
	s.pruneLocalAuthLocked(nowTime)
	expires, ok := s.localSessions[tokenHash(token)]
	return ok && nowTime.Before(expires)
}

func (s *Server) pruneLocalAuthLocked(nowTime time.Time) {
	for hash, expires := range s.bootstrapTickets {
		if !nowTime.Before(expires) {
			delete(s.bootstrapTickets, hash)
		}
	}
	for hash, expires := range s.localSessions {
		if !nowTime.Before(expires) {
			delete(s.localSessions, hash)
		}
	}
}

func loopbackPeer(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func matchingLoopbackOrigin(raw, host string, secure bool) bool {
	u, err := url.Parse(raw)
	expectedScheme := "http"
	if secure {
		expectedScheme = "https"
	}
	return err == nil && u.Scheme == expectedScheme && u.Host == host && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == "" && loopbackHost(u.Host)
}
