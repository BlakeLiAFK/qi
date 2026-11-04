package webui

import (
	"bytes"
	"errors"
	"gopi/internal/artifacts"
	"mime"
	"net/http"
	"strings"
)

// Slots span verification AND transmission, bounding retained download buffers
// even when clients consume responses slowly. Shared across all server instances.
var artifactResponses = make(chan struct{}, artifacts.MaxConcurrentReads)

// Called only after the normal host/origin/authentication/request checks.
func (s *server) artifactRoute(w http.ResponseWriter, r *http.Request) bool {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/tasks/"), "/")
	if len(parts) < 2 || parts[1] != "artifacts" {
		return false
	}
	if len(parts) > 3 || parts[0] == "" || len(parts[0]) > 128 {
		http.NotFound(w, r)
		return true
	}
	if _, err := s.cfg.Backend.Get(r.Context(), parts[0]); err != nil {
		http.NotFound(w, r)
		return true
	}
	if s.cfg.Artifacts == nil {
		if r.Method == "GET" && len(parts) == 2 {
			respond(w, []any{})
		} else {
			http.Error(w, "deliverables are disabled", 404)
		}
		return true
	}
	if len(parts) == 3 || r.Method == "POST" {
		select {
		case artifactResponses <- struct{}{}:
			defer func() { <-artifactResponses }()
		default:
			w.Header().Set("Retry-After", "1")
			http.Error(w, "deliverables busy; retry later", 429)
			return true
		}
	}
	if len(parts) == 3 {
		if r.Method != "GET" {
			http.Error(w, "method not allowed", 405)
			return true
		}
		a, b, err := s.cfg.Artifacts.Download(r.Context(), parts[0], parts[2])
		if err != nil {
			if errors.Is(err, artifacts.ErrBusy) {
				http.Error(w, "deliverables busy; retry later", 429)
				return true
			}
			http.NotFound(w, r)
			return true
		}
		// Always attachment + octet-stream: generated HTML/SVG must never execute at
		// the authenticated application origin. No user-supplied response headers.
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": a.Name}))
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
		http.ServeContent(w, r, a.Name, a.CreatedAt, bytes.NewReader(b))
		return true
	}
	switch r.Method {
	case "GET":
		items, err := s.cfg.Artifacts.List(r.Context(), parts[0])
		if err != nil {
			http.Error(w, "cannot list deliverables", 500)
		} else {
			respond(w, items)
		}
	case "POST":
		var req struct {
			Path string `json:"path"`
		}
		if decode(w, r, &req) != nil {
			http.Error(w, "invalid artifact path", 400)
			return true
		}
		a, err := s.cfg.Artifacts.Register(r.Context(), parts[0], req.Path)
		if err != nil {
			if errors.Is(err, artifacts.ErrQuota) {
				http.Error(w, "task deliverable limit reached (100)", 409)
			} else if errors.Is(err, artifacts.ErrBusy) {
				http.Error(w, "deliverables busy; retry later", 429)
			} else {
				http.Error(w, "file unavailable, unsafe, or exceeds 32 MiB", 400)
			}
		} else {
			respond(w, a)
		}
	default:
		http.Error(w, "method not allowed", 405)
	}
	return true
}
