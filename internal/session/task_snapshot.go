package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"gopi/internal/agent"
)

// TaskSnapshot contains one consistent SQLite snapshot. In particular, a
// completed run cannot be paired with history read before its final response.
type TaskSnapshot struct {
	Session  Session
	Messages []agent.Message
	Runs     []Run
	// Events is a UI projection, not the audit log. Use Store.Events for replay.
	Events []Event
}

type snapshotReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) LoadTaskSnapshot(ctx context.Context, id string) (TaskSnapshot, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return TaskSnapshot{}, err
	}
	defer tx.Rollback()
	result, err := readTaskSnapshot(ctx, tx, id)
	if err != nil {
		return TaskSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return TaskSnapshot{}, err
	}
	return result, nil
}

// All reads must use the same transaction. The interface allows deterministic
// regression tests to commit another connection's completion between reads.
func readTaskSnapshot(ctx context.Context, q snapshotReader, id string) (TaskSnapshot, error) {
	result := TaskSnapshot{Messages: []agent.Message{}, Runs: []Run{}, Events: []Event{}}
	var err error
	result.Session, err = scanSession(q.QueryRowContext(ctx, "SELECT id,title,created_at,updated_at,revision FROM sessions WHERE id=?", id))
	if err != nil {
		return TaskSnapshot{}, err
	}
	rows, err := q.QueryContext(ctx, "SELECT payload FROM messages WHERE session_id=? ORDER BY position", id)
	if err != nil {
		return TaskSnapshot{}, err
	}
	err = func() error {
		defer rows.Close()
		for rows.Next() {
			var payload []byte
			if err := rows.Scan(&payload); err != nil {
				return err
			}
			var message agent.Message
			if err := json.Unmarshal(payload, &message); err != nil {
				return fmt.Errorf("decode stored message: %w", err)
			}
			result.Messages = append(result.Messages, message)
		}
		return rows.Err()
	}()
	if err != nil {
		return TaskSnapshot{}, err
	}
	rows, err = q.QueryContext(ctx, `SELECT id,session_id,status,error,started_at,finished_at FROM runs WHERE session_id=? ORDER BY started_at,id`, id)
	if err != nil {
		return TaskSnapshot{}, err
	}
	err = func() error {
		defer rows.Close()
		for rows.Next() {
			var run Run
			var started, finished string
			if err := rows.Scan(&run.ID, &run.SessionID, &run.Status, &run.Error, &started, &finished); err != nil {
				return err
			}
			var err error
			run.StartedAt, err = time.Parse(time.RFC3339Nano, started)
			if err != nil {
				return err
			}
			if finished != "" {
				run.FinishedAt, err = time.Parse(time.RFC3339Nano, finished)
				if err != nil {
					return err
				}
			}
			result.Runs = append(result.Runs, run)
		}
		return rows.Err()
	}()
	if err != nil {
		return TaskSnapshot{}, err
	}
	rows, err = q.QueryContext(ctx, snapshotEventsQuery, sql.Named("task", id))
	if err != nil {
		return TaskSnapshot{}, err
	}
	err = func() error {
		defer rows.Close()
		for rows.Next() {
			var event Event
			var payload, created string
			if err := rows.Scan(&event.ID, &event.SessionID, &event.RunID, &event.Type, &payload, &created); err != nil {
				return err
			}
			event.Payload = json.RawMessage(payload)
			var err error
			event.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
			if err != nil {
				return err
			}
			result.Events = append(result.Events, event)
		}
		return rows.Err()
	}()
	if err != nil {
		return TaskSnapshot{}, err
	}
	return result, nil
}

// Old preview payloads are never loaded by polling. The current request is
// selected through indexed metadata lookups in the same read transaction as
// history and run status. A closing event suppresses it even if status is still
// running. A malformed producer exceeding 256 deltas fails closed.
const snapshotEventsQuery = `WITH
 active_run AS (SELECT id FROM runs WHERE session_id=:task AND status='running'),
 request_start AS (SELECT id,run_id FROM events WHERE session_id=:task
   AND run_id=(SELECT id FROM active_run) AND type='model_start' ORDER BY id DESC LIMIT 1),
 open_request AS (SELECT id,run_id FROM request_start s WHERE NOT EXISTS (
   SELECT 1 FROM events WHERE session_id=:task AND run_id=s.run_id
   AND type IN ('message','error','run_end') AND id>s.id)),
 delta_ids AS (SELECT id FROM events WHERE session_id=:task AND type='model_delta'
   AND run_id=(SELECT run_id FROM open_request) AND id>(SELECT id FROM open_request)
   ORDER BY id LIMIT 257),
 selected_ids AS (
   SELECT id FROM events WHERE session_id=:task AND type IN ('card','context_trim')
   UNION ALL SELECT id FROM open_request WHERE (SELECT COUNT(*) FROM delta_ids)<=256
   UNION ALL SELECT id FROM delta_ids WHERE (SELECT COUNT(*) FROM delta_ids)<=256
 )
 SELECT id,session_id,run_id,type,payload,created_at FROM events
 WHERE id IN (SELECT id FROM selected_ids) ORDER BY id`
