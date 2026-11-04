package session

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

var ErrRunActive = errors.New("session already has an active run")
var ErrRunFinished = errors.New("run has already finished")

type Run struct {
	ID         string    `json:"id"`
	SessionID  string    `json:"session_id"`
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}
type StoredEvent struct {
	ID        int64           `json:"id"`
	SessionID string          `json:"session_id"`
	RunID     string          `json:"run_id"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

// Event is a persisted lifecycle event. IDs order committed events; they do not
// provide exactly-once execution of tools or other external effects.
type Event = StoredEvent

func (s *Store) initTasks() error {
	for _, query := range []string{
		`CREATE TABLE IF NOT EXISTS task_archive_events (id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL REFERENCES sessions(id), archived INTEGER NOT NULL CHECK(archived IN (0,1)), created_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS task_creation_keys (principal TEXT NOT NULL, key TEXT NOT NULL, digest TEXT NOT NULL, session_id TEXT NOT NULL UNIQUE REFERENCES sessions(id), created_session TEXT NOT NULL, PRIMARY KEY(principal,key))`,
		`CREATE TABLE IF NOT EXISTS runs (id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id), status TEXT NOT NULL CHECK(status IN ('running','completed','failed','cancelled','interrupted')), error TEXT NOT NULL DEFAULT '', started_at TEXT NOT NULL, finished_at TEXT NOT NULL DEFAULT '', UNIQUE(id,session_id))`,
		`CREATE UNIQUE INDEX IF NOT EXISTS one_running_per_session ON runs(session_id) WHERE status='running'`,
		`CREATE INDEX IF NOT EXISTS runs_by_session ON runs(session_id,started_at)`,
		`CREATE INDEX IF NOT EXISTS latest_run_by_session ON runs(session_id,started_at DESC,id DESC)`,
		`CREATE TABLE IF NOT EXISTS events (id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL REFERENCES sessions(id), run_id TEXT NOT NULL, type TEXT NOT NULL, payload TEXT NOT NULL, created_at TEXT NOT NULL, FOREIGN KEY(run_id,session_id) REFERENCES runs(id,session_id))`,
		`CREATE INDEX IF NOT EXISTS events_by_session ON events(session_id,id)`,
		`CREATE INDEX IF NOT EXISTS events_by_session_type_run ON events(session_id,type,run_id,id)`,
	} {
		if _, err := s.db.Exec(query); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) CreateRun(ctx context.Context, sessionID string) (Run, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Run{}, err
	}
	run := Run{ID: hex.EncodeToString(id[:]), SessionID: sessionID, Status: "running", StartedAt: time.Now().UTC()}
	// The partial unique index arbitrates concurrent process attempts atomically.
	result, err := s.db.ExecContext(ctx, `INSERT INTO runs(id,session_id,status,started_at) SELECT ?,id,'running',? FROM sessions WHERE id=? AND archived=0 ON CONFLICT DO NOTHING`, run.ID, run.StartedAt.Format(timestampFormat), sessionID)
	if err != nil {
		return Run{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return Run{}, err
	}
	if count == 0 {
		if item, err := s.Get(ctx, sessionID); err != nil {
			return Run{}, err
		} else if item.Archived {
			return Run{}, ErrArchived
		}
		return Run{}, ErrRunActive
	}
	return run, nil
}
func (s *Store) FinishRun(ctx context.Context, runID, status, errorText string) error {
	switch status {
	case "completed", "failed", "cancelled", "interrupted":
	default:
		return errors.New("invalid terminal run status")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE runs SET status=?,error=?,finished_at=? WHERE id=? AND status IN ('queued','running')`, status, errorText, time.Now().UTC().Format(timestampFormat), runID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		var found string
		err = s.db.QueryRowContext(ctx, "SELECT id FROM runs WHERE id=?", runID).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return ErrRunFinished
	}
	return nil
}
func (s *Store) Runs(ctx context.Context, sessionID string) ([]Run, error) {
	if _, err := s.Get(ctx, sessionID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,session_id,status,error,started_at,finished_at FROM runs WHERE session_id=? ORDER BY started_at,id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Run{}
	for rows.Next() {
		var run Run
		var started, finished string
		if err = rows.Scan(&run.ID, &run.SessionID, &run.Status, &run.Error, &started, &finished); err != nil {
			return nil, err
		}
		run.StartedAt, err = time.Parse(time.RFC3339Nano, started)
		if err != nil {
			return nil, err
		}
		if finished != "" {
			run.FinishedAt, err = time.Parse(time.RFC3339Nano, finished)
			if err != nil {
				return nil, err
			}
		}
		result = append(result, run)
	}
	return result, rows.Err()
}
func (s *Store) AppendEvent(ctx context.Context, sessionID, runID, eventType string, payload json.RawMessage) (Event, error) {
	if eventType == "" || !json.Valid(payload) {
		return Event{}, errors.New("event needs a type and valid JSON payload")
	}
	event := Event{SessionID: sessionID, RunID: runID, Type: eventType, Payload: append(json.RawMessage(nil), payload...), CreatedAt: time.Now().UTC()}
	result, err := s.db.ExecContext(ctx, `INSERT INTO events(session_id,run_id,type,payload,created_at) SELECT session_id,id,?,?,? FROM runs WHERE id=? AND session_id=?`, eventType, string(payload), event.CreatedAt.Format(timestampFormat), runID, sessionID)
	if err != nil {
		return Event{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return Event{}, err
	}
	if count == 0 {
		return Event{}, ErrNotFound
	}
	event.ID, err = result.LastInsertId()
	if err != nil {
		return Event{}, err
	}
	return event, nil
}
func (s *Store) Events(ctx context.Context, sessionID string, afterID int64) ([]Event, error) {
	if _, err := s.Get(ctx, sessionID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,session_id,run_id,type,payload,created_at FROM events WHERE session_id=? AND id>? ORDER BY id`, sessionID, afterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Event{}
	for rows.Next() {
		var event Event
		var payload, created string
		if err = rows.Scan(&event.ID, &event.SessionID, &event.RunID, &event.Type, &payload, &created); err != nil {
			return nil, err
		}
		event.Payload = json.RawMessage(payload)
		event.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, err
		}
		result = append(result, event)
	}
	return result, rows.Err()
}

// InterruptRuns marks abandoned queued/running work after an exclusive-owner restart.
// Do not call while another process is executing runs in this database. This
// never retries external effects whose outcome may be unknown after a crash.
func (s *Store) InterruptRuns(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET status='interrupted',error='process stopped before run completion; external tool outcomes may be unknown',finished_at=? WHERE status IN ('queued','running')`, time.Now().UTC().Format(timestampFormat))
	return err
}
