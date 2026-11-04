package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"gopi/internal/session"
)

type EventBackend interface {
	EventIDs(context.Context, string, int64, int) ([]int64, error)
}

func (b EngineBackend) EventIDs(ctx context.Context, id string, after int64, limit int) ([]int64, error) {
	return b.Engine.EventIDs(ctx, id, after, limit)
}

// events streams durable invalidations, not provider tokens or executable tool
// fragments. Reconnects replay committed IDs after Last-Event-ID. A snapshot
// refresh is authoritative; replay never reruns tools. No subscriber queue is
// retained: slow clients reconnect to SQLite rather than accumulating memory.
func (s *server) events(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	backend, ok := s.cfg.Backend.(EventBackend)
	if !ok {
		http.Error(w, "events unavailable", 501)
		return
	}
	s.mu.Lock()
	if s.streams >= 32 {
		s.mu.Unlock()
		http.Error(w, "too many event streams", http.StatusTooManyRequests)
		return
	}
	s.streams++
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.streams--; s.mu.Unlock() }()
	var after int64
	if raw := r.Header.Get("Last-Event-ID"); raw != "" {
		var err error
		after, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || after < 0 {
			http.Error(w, "invalid event cursor", 400)
			return
		}
	}
	ids, err := backend.EventIDs(r.Context(), id, after, session.MaxEventPage)
	if err != nil {
		if errors.Is(err, session.ErrNotFound) {
			http.NotFound(w, r)
		} else {
			http.Error(w, "events unavailable", 500)
		}
		return
	}
	if _, ok := w.(http.Flusher); !ok {
		http.Error(w, "streaming unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	defer controller.SetWriteDeadline(time.Time{})
	write := func(data string) bool {
		// Override the ordinary server write timeout for this write only. A slow
		// client has five seconds, while idle connections can remain subscribed.
		_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := fmt.Fprint(w, data); err != nil {
			return false
		}
		if err := controller.Flush(); err != nil {
			return false
		}
		// HTTP/2 deadlines can reset an idle stream, so clear after flushing.
		_ = controller.SetWriteDeadline(time.Time{})
		return true
	}
	// Always reconcile on connection, including interrupted-run recovery that
	// changed state without appending an event.
	if !write("retry: 1500\nevent: ready\ndata: {}\n\n") {
		return
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		for _, cursor := range ids {
			data, _ := json.Marshal(struct {
				TaskID  string `json:"task_id"`
				EventID int64  `json:"event_id"`
			}{id, cursor})
			if !write(fmt.Sprintf("id: %d\nevent: progress\ndata: %s\n\n", cursor, data)) {
				return
			}
			after = cursor
		}
		if len(ids) < session.MaxEventPage {
			select {
			case <-r.Context().Done():
				return
			case <-heartbeat.C:
				if !write(": keepalive\n\n") {
					return
				}
			case <-ticker.C:
			}
		}
		ids, err = backend.EventIDs(r.Context(), id, after, session.MaxEventPage)
		if err != nil {
			return
		}
	}
}
