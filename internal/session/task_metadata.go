package session

import (
	"context"
	"strings"
	"time"
)

// TaskMetadata is a sidebar snapshot, not a conversation snapshot. Listing does
// not read message payloads, event payloads, or the full run history.
type TaskMetadata struct {
	UnreadResult bool      `json:"unread_result"`
	UnreadRunID  string    `json:"unread_run_id,omitempty"`
	Archived     bool      `json:"archived"`
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

const taskMetadataQuery = `SELECT s.id,s.title,COALESCE(r.status,'idle'),s.created_at,s.archived,
 MAX(s.updated_at,COALESCE(r.started_at,''),COALESCE(r.finished_at,'')) AS activity_at,
 COALESCE(result.seq,0)>COALESCE(read.through_seq,0),
 CASE WHEN COALESCE(result.seq,0)>COALESCE(read.through_seq,0) THEN result.run_id ELSE '' END
 FROM sessions s
 LEFT JOIN runs r ON r.id=(SELECT latest.id FROM runs latest WHERE latest.session_id=s.id ORDER BY latest.started_at DESC,latest.id DESC LIMIT 1)
 LEFT JOIN task_results result ON result.seq=(SELECT seq FROM task_results WHERE session_id=s.id ORDER BY seq DESC LIMIT 1)
 LEFT JOIN task_result_reads read ON read.session_id=s.id
 WHERE s.archived=0
 ORDER BY activity_at DESC,s.id`

// ListTaskMetadata uses one query with indexed latest-run and terminal-result
// lookups per task.
// Its cost does not grow with the number or size of messages and events.
func (s *Store) ListTaskMetadata(ctx context.Context) ([]TaskMetadata, error) {
	return s.ListTaskMetadataArchived(ctx, false)
}

func (s *Store) ListTaskMetadataArchived(ctx context.Context, archived bool) ([]TaskMetadata, error) {
	query := taskMetadataQuery
	if archived {
		query = strings.Replace(query, "WHERE s.archived=0", "WHERE s.archived=1", 1)
	}
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []TaskMetadata{}
	for rows.Next() {
		var item TaskMetadata
		var created, updated string
		if err := rows.Scan(&item.ID, &item.Title, &item.Status, &created, &item.Archived, &updated, &item.UnreadResult, &item.UnreadRunID); err != nil {
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
