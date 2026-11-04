package webui

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net"
	"net/http"
	"time"
)

const sessionCookie = "__Host-qi-session"
const sessionLifetime = 8 * time.Hour
const maxSessions = 64

type browserSession struct {
	expires time.Time
	ctx     context.Context
	cancel  context.CancelFunc
}

// publicTransport accepts TLS, or the explicitly selected loopback TLS proxy
// boundary. Forwarded client addresses are deliberately never trusted. Caddy
// must overwrite X-Forwarded-Proto and preserve Host. Local processes are trusted.
func publicTransport(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	return err == nil && ip != nil && ip.IsLoopback() && r.Header.Get("X-Forwarded-Proto") == "https"
}

func (s *server) sessionFor(r *http.Request) *browserSession {
	cookies := r.Cookies()
	var raw string
	count := 0
	for _, c := range cookies {
		if c.Name == sessionCookie {
			count++
			if count > 1 {
				return nil
			}
			raw = c.Value
		}
	}
	if len(raw) != 43 {
		return nil
	}
	key := sha256.Sum256([]byte(raw))
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.sessions[key]
	if session != nil && (!s.now().Before(session.expires) || session.ctx.Err() != nil) {
		session.cancel()
		delete(s.sessions, key)
		return nil
	}
	return session
}

func sessionSetCookie(w http.ResponseWriter, value string, expires time.Time, age int) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: age})
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if r.Header.Get("Origin") != s.cfg.Origin || r.Header.Get("X-Qi-Request") != "1" {
		http.Error(w, "request verification failed", 403)
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if decode(w, r, &body) != nil || len(body.Token) > 4096 {
		http.Error(w, "invalid login request", 400)
		return
	}
	// Reuse the bounded actual-peer limiter; credentials are never logged/returned.
	clone := r.Clone(r.Context())
	clone.SetBasicAuth("qi", body.Token)
	ok, limited := s.authenticate(clone)
	if limited {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "login temporarily rate limited", 429)
		return
	}
	if !ok {
		http.Error(w, "invalid credential", 401)
		return
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		http.Error(w, "login unavailable", 503)
		return
	}
	raw := base64.RawURLEncoding.EncodeToString(random[:])
	key := sha256.Sum256([]byte(raw))
	s.mu.Lock()
	now := s.now()
	for k, v := range s.sessions {
		if !now.Before(v.expires) {
			v.cancel()
			delete(s.sessions, k)
		}
	}
	// Rotate an existing session instead of retaining a reusable pre-login cookie.
	if old, err := r.Cookie(sessionCookie); err == nil {
		oldKey := sha256.Sum256([]byte(old.Value))
		if v := s.sessions[oldKey]; v != nil {
			v.cancel()
			delete(s.sessions, oldKey)
		}
	}
	if len(s.sessions) >= maxSessions {
		s.mu.Unlock()
		http.Error(w, "session capacity reached; sign out on another device or restart", 503)
		return
	}
	expires := now.Add(sessionLifetime)
	ctx, cancel := context.WithTimeout(context.Background(), sessionLifetime)
	s.sessions[key] = &browserSession{expires: expires, ctx: ctx, cancel: cancel}
	s.mu.Unlock()
	sessionSetCookie(w, raw, expires, int(sessionLifetime.Seconds()))
	respond(w, map[string]bool{"authenticated": true})
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		All bool `json:"all"`
	}
	if decode(w, r, &body) != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		http.Error(w, "authentication required", 401)
		return
	}
	key := sha256.Sum256([]byte(cookie.Value))
	s.mu.Lock()
	for k, v := range s.sessions {
		if body.All || k == key {
			v.cancel()
			delete(s.sessions, k)
		}
	}
	s.mu.Unlock()
	sessionSetCookie(w, "", time.Unix(1, 0), -1)
	respond(w, map[string]bool{"authenticated": false})
}
