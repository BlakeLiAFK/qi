package webui

import (
	"gopi/internal/tasks"
	"net/http"
)

type ToolCatalogBackend interface{ ToolCatalog() tasks.ToolCatalog }

func (b EngineBackend) ToolCatalog() tasks.ToolCatalog { return b.Engine.ToolCatalog() }
func (s *server) plugins(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		w.Header().Set("Allow", "GET")
		http.Error(w, "catalog is read-only; change operator configuration and restart", 405)
		return
	}
	backend, ok := s.cfg.Backend.(ToolCatalogBackend)
	if !ok {
		http.Error(w, "tool catalog unavailable", 503)
		return
	}
	respond(w, backend.ToolCatalog())
}
