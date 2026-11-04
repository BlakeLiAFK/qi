package webui

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"gopi/internal/session"
)

type ResultReadBackend interface {
	MarkResultRead(context.Context, string, string) error
}

func (b EngineBackend) MarkResultRead(ctx context.Context, taskID, runID string) error {
	return b.Engine.MarkResultRead(ctx, taskID, runID)
}
func (s *server) readResult(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	backend, ok := s.cfg.Backend.(ResultReadBackend)
	if !ok {
		http.Error(w, "result acknowledgment unavailable", 503)
		return
	}
	var req struct {
		RunID string `json:"run_id"`
	}
	if decode(w, r, &req) != nil || strings.TrimSpace(req.RunID) == "" || len(req.RunID) > 128 {
		http.Error(w, "exact run_id required", 400)
		return
	}
	if err := backend.MarkResultRead(r.Context(), id, req.RunID); err != nil {
		if errors.Is(err, session.ErrResultUnavailable) {
			http.Error(w, "terminal result unavailable in this task", 409)
			return
		}
		http.Error(w, "cannot acknowledge result", 500)
		return
	}
	respond(w, map[string]bool{"accepted": true})
}
