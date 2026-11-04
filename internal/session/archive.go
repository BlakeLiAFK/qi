package session

import (
	"context"
	"errors"
	"time"
)

var ErrArchived = errors.New("task is archived; restore it before starting a turn")

// Count includes archived tasks: hiding a task never evicts retained history or
// releases the database's task retention budget.
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sessions`).Scan(&n)
	return n, err
}

// SetArchived changes only visibility, atomically with an audit entry. Both
// transitions reject outstanding work; repeats of the same state are harmless.
func (s *Store) SetArchived(ctx context.Context, id string, archived bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Take SQLite's write lock before reading state, so concurrent connections
	// cannot admit a run between checking outstanding work and archiving.
	result, err := tx.ExecContext(ctx, `UPDATE sessions SET archived=archived WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	var busy int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE session_id=? AND status IN ('queued','running')`, id).Scan(&busy); err != nil {
		return err
	}
	if busy > 0 {
		return ErrRunActive
	}
	var current bool
	if err = tx.QueryRowContext(ctx, `SELECT archived FROM sessions WHERE id=?`, id).Scan(&current); err != nil {
		return err
	}
	if current == archived {
		return tx.Commit()
	}
	now := time.Now().UTC().Format(timestampFormat)
	if _, err = tx.ExecContext(ctx, `UPDATE sessions SET archived=?,updated_at=? WHERE id=?`, archived, now, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_archive_events(session_id,archived,created_at) VALUES(?,?,?)`, id, archived, now); err != nil {
		return err
	}
	return tx.Commit()
}
