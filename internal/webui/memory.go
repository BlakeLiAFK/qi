package webui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"gopi/internal/session"
	"gopi/internal/tasks"
	"net/http"
	"strings"
)

type MemoryBackend interface {
	ListMemory(context.Context, session.MemoryScope) ([]session.MemoryRecord, error)
	CreateMemory(context.Context, session.MemoryScope, session.MemoryFact) (session.MemoryRecord, error)
	EditMemory(context.Context, session.MemoryScope, string, int64, session.MemoryFact) (session.MemoryRecord, error)
	DeleteMemory(context.Context, session.MemoryScope, string, int64) error
	MemoryHistory(context.Context, session.MemoryScope, string) ([]session.MemoryRecord, error)
}

func (b EngineBackend) ListMemory(c context.Context, s session.MemoryScope) ([]session.MemoryRecord, error) {
	return b.Engine.ListMemory(c, s)
}
func (b EngineBackend) CreateMemory(c context.Context, s session.MemoryScope, f session.MemoryFact) (session.MemoryRecord, error) {
	return b.Engine.CreateMemory(c, s, f)
}
func (b EngineBackend) EditMemory(c context.Context, s session.MemoryScope, id string, rev int64, f session.MemoryFact) (session.MemoryRecord, error) {
	return b.Engine.EditMemory(c, s, id, rev, f)
}
func (b EngineBackend) DeleteMemory(c context.Context, s session.MemoryScope, id string, rev int64) error {
	return b.Engine.DeleteMemory(c, s, id, rev)
}
func (b EngineBackend) MemoryHistory(c context.Context, s session.MemoryScope, id string) ([]session.MemoryRecord, error) {
	return b.Engine.MemoryHistory(c, s, id)
}

// memory is reached only after the common authentication, origin and mutation
// checks. Approval references are generated here, not accepted as credentials.
func (s *server) memory(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	b, ok := s.cfg.Backend.(MemoryBackend)
	if !ok {
		http.Error(w, "memory management unavailable", 503)
		return
	}
	q := r.URL.Query()
	for key, values := range q {
		if (key != "task_id" && key != "global" && key != "id") || len(values) != 1 {
			http.Error(w, "invalid memory query", 400)
			return
		}
	}
	scope := session.MemoryScope{TaskID: q.Get("task_id"), Global: q.Get("global") == "true"}
	if (scope.Global && scope.TaskID != "") || (!scope.Global && strings.TrimSpace(scope.TaskID) == "") || (q.Has("global") && q.Get("global") != "true") || len(scope.TaskID) > 128 {
		http.Error(w, "select one task or explicit global scope", 400)
		return
	}
	if !scope.Global {
		if _, err := s.cfg.Backend.Get(r.Context(), scope.TaskID); err != nil {
			http.Error(w, "task unavailable", 404)
			return
		}
	}
	var result any
	var err error
	if r.Method == "GET" {
		if q.Has("id") {
			result, err = b.MemoryHistory(r.Context(), scope, q.Get("id"))
		} else {
			result, err = b.ListMemory(r.Context(), scope)
		}
	} else {
		if q.Has("id") {
			http.Error(w, "mutation ID belongs in body", 400)
			return
		}
		var req struct {
			Action       string              `json:"action"`
			ID           string              `json:"id"`
			Revision     int64               `json:"revision"`
			Confirm      bool                `json:"confirm"`
			NonSensitive bool                `json:"non_sensitive"`
			Fact         *session.MemoryFact `json:"fact"`
		}
		if decode(w, r, &req) != nil || !req.Confirm {
			http.Error(w, "explicit confirmation required", 400)
			return
		}
		if req.Action != "create" && req.Action != "edit" && req.Action != "delete" {
			http.Error(w, "invalid memory action", 400)
			return
		}
		if req.Action == "create" && (req.ID != "" || req.Revision != 0) || req.Action != "create" && (len(req.ID) != 32 || req.Revision < 1) {
			http.Error(w, "invalid memory identity or revision", 400)
			return
		}
		if req.Action == "delete" {
			if req.Fact != nil {
				http.Error(w, "delete must not include a fact", 400)
				return
			}
			err = b.DeleteMemory(r.Context(), scope, req.ID, req.Revision)
			result = map[string]bool{"deleted": err == nil}
		} else {
			if req.Fact == nil || !req.NonSensitive || req.Fact.ApprovalRef != "" {
				http.Error(w, "non-sensitive fact confirmation required; approval reference is server generated", 400)
				return
			}
			var nonce [16]byte
			if _, err = rand.Read(nonce[:]); err != nil {
				http.Error(w, "approval unavailable", 500)
				return
			}
			req.Fact.ApprovalRef = "web-owner:" + hex.EncodeToString(nonce[:])
			if req.Action == "create" {
				result, err = b.CreateMemory(r.Context(), scope, *req.Fact)
			} else {
				result, err = b.EditMemory(r.Context(), scope, req.ID, req.Revision, *req.Fact)
			}
		}
	}
	if err != nil {
		status := 400
		if errors.Is(err, session.ErrMemoryNotFound) {
			status = 404
		}
		if errors.Is(err, session.ErrMemoryConflict) || errors.Is(err, session.ErrMemoryLimit) {
			status = 409
		}
		http.Error(w, "memory operation rejected; inspect current records before retrying", status)
		return
	}
	respond(w, result)
}

type MemorySubmissionBackend interface {
	SubmitWithMemory(context.Context, string, string, string, []string, []session.MemorySelection, bool, string) (tasks.Run, error)
}

func (b EngineBackend) SubmitWithMemory(ctx context.Context, id, key, prompt string, names []string, selected []session.MemorySelection, disclosed bool, destination string) (tasks.Run, error) {
	return b.Engine.SubmitWithMemory(ctx, id, "local-owner", key, prompt, names, selected, disclosed, destination)
}

func (b EngineBackend) MemoryDestination() session.MemoryDestination {
	return b.Engine.MemoryDestination()
}
func (s *server) memoryDestination(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	b, ok := s.cfg.Backend.(interface {
		MemoryDestination() session.MemoryDestination
	})
	if !ok {
		http.Error(w, "memory destination unavailable", 503)
		return
	}
	d := b.MemoryDestination()
	if d.ID == "" {
		http.Error(w, "memory destination unavailable", 503)
		return
	}
	respond(w, d)
}
