package webui

import (
	"context"
	"errors"
	"gopi/internal/session"
	"net/http"
)

type ArchiveBackend interface {
	ListArchived(context.Context, bool) ([]Task, error)
	SetArchived(context.Context, string, bool) error
}

func (b EngineBackend) ListArchived(ctx context.Context, archived bool) ([]Task, error) {
	return b.Engine.ListArchived(ctx, archived)
}
func (b EngineBackend) SetArchived(ctx context.Context, id string, archived bool) error {
	return b.Engine.SetArchived(ctx, id, archived)
}

func (s *server) archive(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	backend, ok := s.cfg.Backend.(ArchiveBackend)
	if !ok {
		http.Error(w, "archiving unavailable", 503)
		return
	}
	var req struct {
		Archived *bool `json:"archived"`
	}
	if decode(w, r, &req) != nil || req.Archived == nil {
		http.Error(w, "archived boolean required", 400)
		return
	}
	if err := backend.SetArchived(r.Context(), id, *req.Archived); err != nil {
		if errors.Is(err, session.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "archive change rejected: task has outstanding work or changed; refresh", 409)
		return
	}
	respond(w, map[string]bool{"accepted": true, "archived": *req.Archived})
}
