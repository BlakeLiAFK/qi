package session

import (
	"context"
	"errors"
)

const MaxEventPage = 128

// EventIDs returns durable, task-scoped invalidation cursors, never tool payloads.
// Pages have a fixed memory bound even when an event contains a large result.
func (s *Store) EventIDs(ctx context.Context, sessionID string, after int64, limit int) ([]int64, error) {
	if after < 0 || limit < 1 || limit > MaxEventPage {
		return nil, errors.New("invalid event cursor or page size")
	}
	if _, err := s.Get(ctx, sessionID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM events WHERE session_id=? AND id>? ORDER BY id LIMIT ?`, sessionID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
