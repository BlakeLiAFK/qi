// Package webui serves qi's single-user, authenticated web interface.
package webui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"gopi/internal/tasks"
)

//go:embed assets/*
var assets embed.FS

type Task = tasks.Task
type ActionRequest = tasks.ActionRequest

// Backend must enforce authoritative task/card state and atomic single-use actions.
type Backend interface {
	List(context.Context) ([]Task, error)
	Create(context.Context, string) (Task, error)
	Start(context.Context, string, string) error
	Get(context.Context, string) (Task, error)
	Cancel(context.Context, string) error
	Act(context.Context, string, ActionRequest) error
}
type Config struct {
	Origin  string
	Token   string
	Backend Backend
}
type server struct {
	cfg      Config
	host     string
	digest   [32]byte
	mu       sync.Mutex
	failures int
	window   time.Time
}

// New requires HTTPS except on literal loopback hosts. Behind a TLS proxy, bind
// the HTTP listener to loopback and preserve the original Host header. The proxy
// must reject public cleartext HTTP and must not log Authorization headers.
func New(cfg Config) (http.Handler, error) {
	u, err := url.Parse(cfg.Origin)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return nil, errors.New("origin must be an exact scheme and host without a trailing slash")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && ip != nil && ip.IsLoopback()) {
		return nil, errors.New("HTTPS is required except for literal loopback addresses")
	}
	if len(cfg.Token) < 32 || strings.ContainsAny(cfg.Token, "\r\n") {
		return nil, errors.New("web token must contain at least 32 characters and no newlines")
	}
	if cfg.Backend == nil {
		return nil, errors.New("web backend is required")
	}
	return &server{cfg: cfg, host: u.Host, digest: sha256.Sum256([]byte(cfg.Token))}, nil
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	if strings.HasPrefix(s.cfg.Origin, "https:") {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
	}
	if r.Host != s.host {
		http.Error(w, "unrecognized host", http.StatusForbidden)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != s.cfg.Origin {
		http.Error(w, "origin denied", http.StatusForbidden)
		return
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" {
		http.Error(w, "cross-site request denied", http.StatusForbidden)
		return
	}
	user, pass, ok := r.BasicAuth()
	digest := sha256.Sum256([]byte(pass))
	authenticated := ok && subtle.ConstantTimeCompare([]byte(user), []byte("qi")) == 1 && subtle.ConstantTimeCompare(digest[:], s.digest[:]) == 1
	if !authenticated {
		s.mu.Lock()
		if time.Since(s.window) >= time.Minute {
			s.window = time.Now()
			s.failures = 0
		}
		s.failures++
		limited := s.failures > 20
		s.mu.Unlock()
		if limited {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "too many authentication failures", http.StatusTooManyRequests)
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="qi", charset="UTF-8"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if r.Method != "GET" && (r.Header.Get("Origin") != s.cfg.Origin || r.Header.Get("X-Qi-Request") != "1") {
		http.Error(w, "request verification failed", http.StatusForbidden)
		return
	}
	switch r.URL.Path {
	case "/", "/app.js", "/style.css":
		if r.Method != "GET" {
			http.Error(w, "method not allowed", 405)
			return
		}
		name := r.URL.Path[1:]
		if name == "" {
			name = "index.html"
		}
		b, err := assets.ReadFile("assets/" + name)
		if err != nil {
			http.Error(w, "asset unavailable", 500)
			return
		}
		contentType := map[string]string{"index.html": "text/html; charset=utf-8", "app.js": "text/javascript; charset=utf-8", "style.css": "text/css; charset=utf-8"}
		w.Header().Set("Content-Type", contentType[name])
		w.Write(b)
	case "/api/tasks":
		s.tasks(w, r)
	default:
		if strings.HasPrefix(r.URL.Path, "/api/tasks/") {
			s.task(w, r)
			return
		}
		http.NotFound(w, r)
	}
}
func respond(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(value)
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	if r.Header.Get("Content-Type") != "application/json" {
		return errors.New("application/json required")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	d := json.NewDecoder(r.Body)
	var raw json.RawMessage
	if err := d.Decode(&raw); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("expected one JSON object")
	}
	if len(raw) == 0 || raw[0] != '{' {
		return errors.New("expected JSON object")
	}
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	return strict.Decode(v)
}
func (s *server) tasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		tasks, err := s.cfg.Backend.List(r.Context())
		if err != nil {
			http.Error(w, "cannot list tasks", 500)
			return
		}
		if tasks == nil {
			tasks = []Task{}
		}
		respond(w, tasks)
	case "POST":
		var req struct {
			Title string `json:"title"`
		}
		if decode(w, r, &req) != nil || strings.TrimSpace(req.Title) == "" || len(req.Title) > 160 {
			http.Error(w, "invalid title (maximum 160 bytes)", 400)
			return
		}
		task, err := s.cfg.Backend.Create(r.Context(), req.Title)
		if err != nil {
			http.Error(w, "task could not be started", 409)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(202)
		respond(w, task)
	default:
		http.Error(w, "method not allowed", 405)
	}
}
func (s *server) task(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/tasks/"), "/")
	id := parts[0]
	if id == "" || len(id) > 128 || len(parts) > 2 {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 1 && r.Method == "GET" {
		task, err := s.cfg.Backend.Get(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		respond(w, task)
		return
	}
	if len(parts) != 2 || r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	var err error
	switch parts[1] {
	case "messages":
		var req struct {
			Prompt string `json:"prompt"`
		}
		if decode(w, r, &req) != nil || strings.TrimSpace(req.Prompt) == "" || len(req.Prompt) > 16000 {
			http.Error(w, "invalid prompt (maximum 16000 bytes)", 400)
			return
		}
		err = s.cfg.Backend.Start(r.Context(), id, req.Prompt)
	case "cancel":
		var req struct{}
		if decode(w, r, &req) != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		err = s.cfg.Backend.Cancel(r.Context(), id)
	case "actions":
		var req ActionRequest
		if decode(w, r, &req) != nil || req.CardID == "" || req.Action == "" || req.Revision < 1 || len(req.CardID) > 128 || len(req.Action) > 128 {
			http.Error(w, "invalid action", 400)
			return
		}
		err = s.cfg.Backend.Act(r.Context(), id, req)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "action rejected: task or card changed; refresh and try again", 409)
		return
	}
	respond(w, map[string]bool{"accepted": true})
}
