package session

import (
	"context"
	"database/sql"
	"gopi/internal/artifacts"
	"time"
)

func (s *Store) initArtifacts(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS artifacts(id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES sessions(id), name TEXT NOT NULL, path TEXT NOT NULL, media_type TEXT NOT NULL, size INTEGER NOT NULL, sha256 TEXT NOT NULL, created_at TEXT NOT NULL)`)
	return err
}

// The quota and deduplication predicates run in the same SQLite write statement,
// so concurrent registrations cannot both consume the last quota slot.
func (s *Store) SaveArtifact(ctx context.Context, a artifacts.Artifact) (artifacts.Artifact, error) {
	if err := s.initArtifacts(ctx); err != nil {
		return artifacts.Artifact{}, err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO artifacts(id,task_id,name,path,media_type,size,sha256,created_at)
 SELECT ?,?,?,?,?,?,?,? WHERE (SELECT COUNT(*) FROM artifacts WHERE task_id=?) < ?
 AND NOT EXISTS(SELECT 1 FROM artifacts WHERE task_id=? AND path=? AND sha256=?)`,
		a.ID, a.TaskID, a.Name, a.Path, a.MediaType, a.Size, a.SHA256, a.CreatedAt.Format(timestampFormat), a.TaskID, artifacts.MaxPerTask, a.TaskID, a.Path, a.SHA256)
	if err != nil {
		return artifacts.Artifact{}, err
	}
	var id string
	err = s.db.QueryRowContext(ctx, `SELECT id FROM artifacts WHERE task_id=? AND path=? AND sha256=? ORDER BY created_at,id LIMIT 1`, a.TaskID, a.Path, a.SHA256).Scan(&id)
	if err != nil {
		if err == sql.ErrNoRows {
			return artifacts.Artifact{}, artifacts.ErrQuota
		}
		return artifacts.Artifact{}, err
	}
	return s.GetArtifact(ctx, a.TaskID, id)
}
func (s *Store) ListArtifacts(ctx context.Context, task string) ([]artifacts.Artifact, error) {
	if err := s.initArtifacts(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,task_id,name,path,media_type,size,sha256,created_at FROM artifacts WHERE task_id=? ORDER BY created_at,id`, task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []artifacts.Artifact{}
	for rows.Next() {
		var a artifacts.Artifact
		var at string
		if err = rows.Scan(&a.ID, &a.TaskID, &a.Name, &a.Path, &a.MediaType, &a.Size, &a.SHA256, &at); err != nil {
			return nil, err
		}
		a.CreatedAt, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}
func (s *Store) GetArtifact(ctx context.Context, task, id string) (artifacts.Artifact, error) {
	if err := s.initArtifacts(ctx); err != nil {
		return artifacts.Artifact{}, err
	}
	var a artifacts.Artifact
	var at string
	err := s.db.QueryRowContext(ctx, `SELECT id,task_id,name,path,media_type,size,sha256,created_at FROM artifacts WHERE task_id=? AND id=?`, task, id).Scan(&a.ID, &a.TaskID, &a.Name, &a.Path, &a.MediaType, &a.Size, &a.SHA256, &at)
	if err != nil {
		return artifacts.Artifact{}, err
	}
	a.CreatedAt, err = time.Parse(time.RFC3339Nano, at)
	return a, err
}
