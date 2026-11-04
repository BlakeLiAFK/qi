// Package webui serves qi's single-user, authenticated web interface.
package webui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"gopi/internal/artifacts"
	"gopi/internal/session"
	"gopi/internal/skills"
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

// ApprovalBackend is deliberately separate from informational card actions.
type ApprovalBackend interface {
	Approvals(context.Context, string) ([]tasks.Approval, error)
	DecideApproval(context.Context, string, tasks.ApprovalDecision) (tasks.Approval, error)
}
type TaskCreationBackend interface {
	CreateIdempotent(context.Context, string, string) (Task, error)
}
type SubmissionBackend interface {
	Submit(context.Context, string, string, string, []string) (tasks.Run, error)
}
type SkillsBackend interface {
	AvailableSkills() []skills.Metadata
	StartWithSkills(context.Context, string, string, []string) error
}
type Config struct {
	PublicPreview bool // HTTPS browser sessions; disables Basic and terminal attach
	Artifacts     *artifacts.Service
	Origin        string
	Token         string
	Backend       Backend
}
type loginWindow struct {
	failures int
	expires  time.Time
}

const maxLoginPeers = 4096
const maxLoginFailures = 20

type server struct {
	cfg      Config
	host     string
	digest   [32]byte
	mu       sync.Mutex
	attempts map[string]loginWindow
	sessions map[[32]byte]*browserSession
	streams  int
	now      func() time.Time
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
	if cfg.PublicPreview && len(cfg.Token) > 4096 {
		return nil, errors.New("public preview token must not exceed 4096 bytes")
	}
	if cfg.PublicPreview && u.Scheme != "https" {
		return nil, errors.New("public preview requires an HTTPS origin")
	}
	if len(cfg.Token) < 32 || strings.ContainsAny(cfg.Token, "\r\n") {
		return nil, errors.New("web token must contain at least 32 characters and no newlines")
	}
	if cfg.Backend == nil {
		return nil, errors.New("web backend is required")
	}
	digest := sha256.Sum256([]byte(cfg.Token))
	cfg.Token = "" // retain only a digest of the high-entropy login credential
	return &server{cfg: cfg, host: u.Host, digest: digest, attempts: make(map[string]loginWindow), sessions: make(map[[32]byte]*browserSession), now: time.Now}, nil
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
	if s.cfg.PublicPreview {
		if !publicTransport(r) {
			http.Error(w, "HTTPS transport required", 403)
			return
		}
		switch r.URL.Path {
		case "/login", "/login.js":
			if r.Method != "GET" {
				http.Error(w, "method not allowed", 405)
				return
			}
			name := "login.html"
			contentType := "text/html; charset=utf-8"
			if r.URL.Path == "/login.js" {
				name = "login.js"
				contentType = "text/javascript; charset=utf-8"
			}
			data, err := assets.ReadFile("assets/" + name)
			if err != nil {
				http.Error(w, "login unavailable", 500)
				return
			}
			w.Header().Set("Content-Type", contentType)
			w.Write(data)
			return
		case "/api/login":
			s.login(w, r)
			return
		}
		session := s.sessionFor(r)
		if session == nil {
			if r.URL.Path == "/" && r.Method == "GET" {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
			} else {
				http.Error(w, "authentication required", 401)
			}
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		stop := context.AfterFunc(session.ctx, cancel)
		defer stop()
		defer cancel()
		r = r.WithContext(ctx)
	} else {
		authenticated, limited := s.authenticate(r)
		if limited {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "authentication temporarily rate limited", 429)
			return
		}
		if !authenticated {
			w.Header().Set("WWW-Authenticate", `Basic realm="qi", charset="UTF-8"`)
			http.Error(w, "authentication required", 401)
			return
		}
	}
	if r.Method != "GET" && (r.Header.Get("Origin") != s.cfg.Origin || r.Header.Get("X-Qi-Request") != "1") {
		http.Error(w, "request verification failed", http.StatusForbidden)
		return
	}
	if s.cfg.PublicPreview && r.URL.Path == "/api/logout" {
		s.logout(w, r)
		return
	}
	switch r.URL.Path {
	case "/", "/app.js", "/style.css", "/memory", "/memory.js", "/plugins", "/plugins.js":
		if r.Method != "GET" {
			http.Error(w, "method not allowed", 405)
			return
		}
		name := r.URL.Path[1:]
		if name == "plugins" {
			name = "plugins.html"
		}
		if name == "memory" {
			name = "memory.html"
		}
		if name == "" {
			name = "index.html"
		}
		b, err := assets.ReadFile("assets/" + name)
		if err != nil {
			http.Error(w, "asset unavailable", 500)
			return
		}
		contentType := map[string]string{"plugins.html": "text/html; charset=utf-8", "plugins.js": "text/javascript; charset=utf-8", "memory.html": "text/html; charset=utf-8", "memory.js": "text/javascript; charset=utf-8", "index.html": "text/html; charset=utf-8", "app.js": "text/javascript; charset=utf-8", "style.css": "text/css; charset=utf-8"}
		w.Header().Set("Content-Type", contentType[name])
		w.Write(b)
	case "/api/auth":
		if r.Method != "GET" {
			http.Error(w, "method not allowed", 405)
			return
		}
		respond(w, map[string]bool{"session_auth": s.cfg.PublicPreview})
	case "/api/memory-destination":
		s.memoryDestination(w, r)
		return
	case "/api/plugins":
		s.plugins(w, r)
	case "/api/skills":
		if r.Method != "GET" {
			http.Error(w, "method not allowed", 405)
			return
		}
		items := []skills.Metadata{}
		if backend, ok := s.cfg.Backend.(SkillsBackend); ok {
			items = backend.AvailableSkills()
			if items == nil {
				items = []skills.Metadata{}
			}
		}
		respond(w, items)
	case "/api/memory":
		s.memory(w, r)
	case "/api/tasks":
		s.tasks(w, r)
	default:
		if strings.HasPrefix(r.URL.Path, "/api/tasks/") {
			if s.artifactRoute(w, r) {
				return
			}
			s.task(w, r)
			return
		}
		http.NotFound(w, r)
	}
}

// authenticate checks the peer's failure budget BEFORE parsing or comparing
// credentials. A blocked peer cannot test guesses, including a correct guess.
// Never trust Forwarded/X-Forwarded-For supplied by an arbitrary caller.
func (s *server) authenticate(r *http.Request) (authenticated, limited bool) {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	if ip := net.ParseIP(peer); ip != nil {
		peer = ip.String()
	} else {
		peer = "unknown"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	window, exists := s.attempts[peer]
	if exists && !now.Before(window.expires) {
		delete(s.attempts, peer)
		exists = false
		window = loginWindow{}
	}
	if exists && window.failures >= maxLoginFailures {
		return false, true
	}
	if !exists && len(s.attempts) >= maxLoginPeers {
		for key, value := range s.attempts {
			if !now.Before(value.expires) {
				delete(s.attempts, key)
			}
		}
		if len(s.attempts) >= maxLoginPeers {
			return false, true
		}
	}
	user, pass, ok := r.BasicAuth()
	digest := sha256.Sum256([]byte(pass))
	authenticated = ok && subtle.ConstantTimeCompare([]byte(user), []byte("qi")) == 1 && subtle.ConstantTimeCompare(digest[:], s.digest[:]) == 1
	if authenticated {
		// Successful polling must not replenish another caller's failed-guess budget
		// when a proxy or NAT shares this peer address.
		return true, false
	}
	if !exists {
		window.expires = now.Add(time.Minute)
	}
	window.failures++
	s.attempts[peer] = window
	return false, false
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
		var tasks []Task
		var err error
		if value := r.URL.Query().Get("archived"); value != "" && value != "false" && value != "true" {
			http.Error(w, "invalid archived filter", 400)
			return
		}
		if r.URL.Query().Get("archived") == "true" {
			backend, ok := s.cfg.Backend.(ArchiveBackend)
			if !ok {
				http.Error(w, "archiving unavailable", 503)
				return
			}
			tasks, err = backend.ListArchived(r.Context(), true)
		} else {
			tasks, err = s.cfg.Backend.List(r.Context())
		}
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
			Key   string `json:"idempotency_key,omitempty"`
		}
		if decode(w, r, &req) != nil || strings.TrimSpace(req.Title) == "" || len(req.Title) > 160 {
			http.Error(w, "invalid title (maximum 160 bytes)", 400)
			return
		}
		var task Task
		var err error
		if req.Key != "" {
			if len(req.Key) > 128 || strings.IndexFunc(req.Key, func(c rune) bool { return c < 33 || c > 126 }) >= 0 {
				http.Error(w, "invalid idempotency key", 400)
				return
			}
			backend, ok := s.cfg.Backend.(TaskCreationBackend)
			if !ok {
				http.Error(w, "idempotent task creation unavailable", 503)
				return
			}
			task, err = backend.CreateIdempotent(r.Context(), req.Key, req.Title)
		} else {
			task, err = s.cfg.Backend.Create(r.Context(), req.Title)
		}
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
	if len(parts) == 2 && parts[1] == "read-result" {
		s.readResult(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "archive" {
		s.archive(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "events" {
		s.events(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "approvals" {
		s.approvals(w, r, id)
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
			Prompt            string                    `json:"prompt"`
			Skills            []string                  `json:"skills,omitempty"`
			Key               string                    `json:"idempotency_key,omitempty"`
			Memory            []session.MemorySelection `json:"memory,omitempty"`
			MemoryDestination string                    `json:"memory_destination,omitempty"`
			MemoryDisclosed   bool                      `json:"memory_disclosed,omitempty"`
		}
		if decode(w, r, &req) != nil || strings.TrimSpace(req.Prompt) == "" || len(req.Prompt) > 16000 {
			http.Error(w, "invalid prompt (maximum 16000 bytes)", 400)
			return
		}
		if len(req.Memory) > 32 || (len(req.Memory) > 0 && (!req.MemoryDisclosed || req.Key == "")) {
			http.Error(w, "memory selection requires disclosure confirmation and idempotency key", 400)
			return
		}
		if len(req.Skills) > 256 {
			http.Error(w, "too many selected skills", 400)
			return
		}
		seen := map[string]bool{}
		for _, name := range req.Skills {
			if name == "" || len(name) > 128 || seen[name] {
				http.Error(w, "invalid skill selection", 400)
				return
			}
			seen[name] = true
		}
		if req.Key != "" {
			if len(req.Key) > 128 || strings.IndexFunc(req.Key, func(c rune) bool { return c < 33 || c > 126 }) >= 0 {
				http.Error(w, "invalid idempotency key", 400)
				return
			}
			backend, ok := s.cfg.Backend.(SubmissionBackend)
			if !ok {
				http.Error(w, "idempotent submission unavailable", 503)
				return
			}
			var run tasks.Run
			var submitErr error
			if len(req.Memory) > 0 {
				memoryBackend, ok := s.cfg.Backend.(MemorySubmissionBackend)
				if !ok {
					http.Error(w, "memory selection unavailable", 503)
					return
				}
				run, submitErr = memoryBackend.SubmitWithMemory(r.Context(), id, req.Key, req.Prompt, req.Skills, req.Memory, req.MemoryDisclosed, req.MemoryDestination)
			} else {
				run, submitErr = backend.Submit(r.Context(), id, req.Key, req.Prompt, req.Skills)
			}
			if submitErr != nil {
				http.Error(w, "submission rejected: key, task state, or capacity conflict", 409)
				return
			}
			respond(w, struct {
				Accepted bool      `json:"accepted"`
				Run      tasks.Run `json:"run"`
			}{true, run})
			return
		}
		if len(req.Skills) == 0 {
			err = s.cfg.Backend.Start(r.Context(), id, req.Prompt)
		} else {
			backend, ok := s.cfg.Backend.(SkillsBackend)
			if !ok {
				http.Error(w, "skills are unavailable", 400)
				return
			}
			err = backend.StartWithSkills(r.Context(), id, req.Prompt, req.Skills)
		}
	case "cancel":
		var req struct{}
		if decode(w, r, &req) != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		err = s.cfg.Backend.Cancel(r.Context(), id)
	case "actions":
		var req ActionRequest
		if decode(w, r, &req) != nil || req.CardID == "" || req.Action == "" || req.Revision < 1 || len(req.CardID) > 128 || len(req.Action) > 128 || len(req.TaskID) > 128 || len(req.ChoiceID) > 128 || len(req.Text) > 8000 {
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

func (s *server) approvals(w http.ResponseWriter, r *http.Request, id string) {
	backend, ok := s.cfg.Backend.(ApprovalBackend)
	if !ok {
		http.Error(w, "approvals are unavailable", http.StatusNotImplemented)
		return
	}
	switch r.Method {
	case "GET":
		items, err := backend.Approvals(r.Context(), id)
		if err != nil {
			http.Error(w, "cannot list approvals", 500)
			return
		}
		if items == nil {
			items = []tasks.Approval{}
		}
		respond(w, items)
	case "POST":
		var req struct {
			ID       string `json:"id"`
			Revision int64  `json:"revision"`
			Digest   string `json:"digest"`
			Allow    *bool  `json:"allow"`
		}
		if decode(w, r, &req) != nil || req.ID == "" || len(req.ID) > 128 || req.Revision < 1 || len(req.Digest) != 64 || req.Allow == nil {
			http.Error(w, "invalid exact-action decision", 400)
			return
		}
		if _, err := hex.DecodeString(req.Digest); err != nil {
			http.Error(w, "invalid action digest", 400)
			return
		}
		approval, err := backend.DecideApproval(r.Context(), id, tasks.ApprovalDecision{ID: req.ID, Revision: req.Revision, Digest: req.Digest, Allow: *req.Allow})
		if err != nil {
			http.Error(w, "approval rejected: stale, expired or already decided; refresh", 409)
			return
		}
		respond(w, approval)
	default:
		http.Error(w, "method not allowed", 405)
	}
}
