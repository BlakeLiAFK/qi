package session

import (
	"context"
	"time"
)

// TaskMetadata is a sidebar snapshot, not a conversation snapshot. Listing does
// not read message payloads, event payloads, or the full run history.
type TaskMetadata struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const taskMetadataQuery = `SELECT s.id,s.title,COALESCE(r.status,'idle'),s.created_at,
 MAX(s.updated_at,COALESCE(r.started_at,''),COALESCE(r.finished_at,'')) AS activity_at
 FROM sessions s
 LEFT JOIN runs r ON r.id=(SELECT latest.id FROM runs latest WHERE latest.session_id=s.id ORDER BY latest.started_at DESC,latest.id DESC LIMIT 1)
 ORDER BY activity_at DESC,s.id`

// ListTaskMetadata uses one query and one indexed latest-run lookup per task.
// Its cost does not grow with the number or size of messages and events.
func (s *Store) ListTaskMetadata(ctx context.Context) ([]TaskMetadata, error) {
	rows, err := s.db.QueryContext(ctx, taskMetadataQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []TaskMetadata{}
	for rows.Next() {
		var item TaskMetadata
		var created, updated string
		if err := rows.Scan(&item.ID, &item.Title, &item.Status, &created, &updated); err != nil {
			return nil, err
		}
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, err
		}
		item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
