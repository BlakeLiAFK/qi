package session

import (
	"context"
	"errors"
)

var ErrResultUnavailable = errors.New("terminal result is unavailable in this task")

// Both triggers participate in the transaction that makes a run terminal. The
// queue schema migration replaces runs and must reinstall these before commit.
const resultUpdateTrigger = `CREATE TRIGGER IF NOT EXISTS record_terminal_result
 AFTER UPDATE OF status ON runs
 WHEN NEW.status IN ('completed','failed','cancelled','interrupted')
 AND OLD.status IN ('queued','running')
 BEGIN INSERT INTO task_results(session_id,run_id) VALUES(NEW.session_id,NEW.id) ON CONFLICT(run_id) DO NOTHING; END`
const resultInsertTrigger = `CREATE TRIGGER IF NOT EXISTS record_inserted_terminal_result
 AFTER INSERT ON runs WHEN NEW.status IN ('completed','failed','cancelled','interrupted')
 BEGIN INSERT INTO task_results(session_id,run_id) VALUES(NEW.session_id,NEW.id) ON CONFLICT(run_id) DO NOTHING; END`

func (s *Store) initResultRead() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var installed int
	if err = tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='task_result_reads'`).Scan(&installed); err != nil {
		return err
	}
	for _, query := range []string{
		`CREATE TABLE IF NOT EXISTS task_results(seq INTEGER PRIMARY KEY AUTOINCREMENT,session_id TEXT NOT NULL,run_id TEXT NOT NULL UNIQUE,FOREIGN KEY(run_id,session_id) REFERENCES runs(id,session_id))`,
		`CREATE INDEX IF NOT EXISTS task_results_by_task ON task_results(session_id,seq DESC)`,
		`CREATE TABLE IF NOT EXISTS task_result_reads(session_id TEXT PRIMARY KEY REFERENCES sessions(id),through_seq INTEGER NOT NULL CHECK(through_seq>=0))`,
	} {
		if _, err = tx.Exec(query); err != nil {
			return err
		}
	}
	if installed == 0 {
		// Existing terminal history is the upgrade baseline, not new notifications.
		// The transaction serializes migration with writers; later completions flow
		// through the triggers installed below, including crash recovery interrupts.
		if _, err = tx.Exec(`INSERT INTO task_results(session_id,run_id) SELECT session_id,id FROM runs WHERE status IN ('completed','failed','cancelled','interrupted') ORDER BY finished_at,id`); err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO task_result_reads(session_id,through_seq) SELECT session_id,max(seq) FROM task_results GROUP BY session_id`); err != nil {
			return err
		}
	}
	for _, query := range []string{resultUpdateTrigger, resultInsertTrigger} {
		if _, err = tx.Exec(query); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// MarkResultRead advances only as far as this exact terminal run, never to the
// latest result at request time. Stale/repeated ACKs cannot regress the watermark
// or swallow a concurrent completion. The principal is the daemon's single owner.
func (s *Store) MarkResultRead(ctx context.Context, taskID, runID string) error {
	if taskID == "" || runID == "" {
		return ErrResultUnavailable
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO task_result_reads(session_id,through_seq)
 SELECT r.session_id,r.seq FROM task_results r JOIN runs run ON run.id=r.run_id AND run.session_id=r.session_id
 WHERE r.session_id=? AND r.run_id=? AND run.status IN ('completed','failed','cancelled','interrupted')
 ON CONFLICT(session_id) DO UPDATE SET through_seq=MAX(task_result_reads.through_seq,excluded.through_seq)`, taskID, runID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrResultUnavailable
	}
	return nil
}

type ResultReadState struct {
	TerminalRunID string
	UnreadRunID   string
	Unread        bool
}

func readResultState(ctx context.Context, q snapshotReader, taskID string) (ResultReadState, error) {
	var state ResultReadState
	err := q.QueryRowContext(ctx, `SELECT COALESCE(r.run_id,''),COALESCE(r.seq,0)>COALESCE(read.through_seq,0)
 FROM sessions s LEFT JOIN task_results r ON r.seq=(SELECT seq FROM task_results WHERE session_id=s.id ORDER BY seq DESC LIMIT 1)
 LEFT JOIN task_result_reads read ON read.session_id=s.id WHERE s.id=?`, taskID).Scan(&state.TerminalRunID, &state.Unread)
	if state.Unread {
		state.UnreadRunID = state.TerminalRunID
	}
	return state, err
}
