package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// MemorySelection names a reviewed current revision. Non-global selections can
// only refer to the task being submitted, never a caller-supplied other task.
type MemorySelection struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
	Global   bool   `json:"global"`
}

// selectTurnMemory runs inside admission's transaction; edits, expiry, deletion,
// snapshot persistence and the idempotency record cannot race acceptance.
func selectTurnMemory(ctx context.Context, tx *sql.Tx, task string, selected []MemorySelection) ([]MemoryRecord, error) {
	if len(selected) > 32 {
		return nil, ErrMemoryLimit
	}
	records := []MemoryRecord{}
	seen := map[string]bool{}
	for _, item := range selected {
		if err := validateMemoryID(item.ID); err != nil {
			return nil, err
		}
		if item.Revision < 1 || seen[item.ID] {
			return nil, errors.New("invalid memory selection")
		}
		seen[item.ID] = true
		scope := MemoryScope{TaskID: task}
		if item.Global {
			scope = MemoryScope{Global: true}
		}
		key, err := scope.key()
		if err != nil {
			return nil, err
		}
		record, err := decodeMemory(tx.QueryRowContext(ctx, memorySelect+` WHERE m.scope=? AND m.id=?`, key, item.ID))
		if err != nil {
			return nil, err
		}
		if record.Revision != item.Revision {
			return nil, ErrMemoryConflict
		}
		if record.ExpiresAt != nil && !record.ExpiresAt.After(time.Now().UTC()) {
			return nil, ErrMemoryNotFound
		}
		records = append(records, record)
	}
	return records, nil
}

// TurnMemory is an audit snapshot, not retrieval. It is scoped to an exact run
// and task and survives later edits/deletion of the curated source record.
func (s *Store) TurnMemory(ctx context.Context, task, run string) ([]MemoryRecord, error) {
	var payload string
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM turn_memory WHERE session_id=? AND run_id=?`, task, run).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result []MemoryRecord
	err = json.Unmarshal([]byte(payload), &result)
	return result, err
}

func (s *Store) TaskTurnMemory(ctx context.Context, task string) (map[string][]MemoryRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT run_id,payload FROM turn_memory WHERE session_id=?`, task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string][]MemoryRecord{}
	for rows.Next() {
		var run, payload string
		if err := rows.Scan(&run, &payload); err != nil {
			return nil, err
		}
		var records []MemoryRecord
		if err := json.Unmarshal([]byte(payload), &records); err != nil {
			return nil, err
		}
		result[run] = records
	}
	return result, rows.Err()
}

func (s *Store) TaskMemoryDestinations(ctx context.Context, task string) (map[string]MemoryDestination, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT run_id,destination FROM turn_memory WHERE session_id=?`, task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]MemoryDestination{}
	for rows.Next() {
		var run, payload string
		if err := rows.Scan(&run, &payload); err != nil {
			return nil, err
		}
		var d MemoryDestination
		if err := json.Unmarshal([]byte(payload), &d); err != nil {
			return nil, err
		}
		result[run] = d
	}
	return result, rows.Err()
}
