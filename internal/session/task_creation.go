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

// CreateIdempotent saves the initial task and its owner-scoped client key in
// one transaction. Replays return the immutable creation response even at cap.
// A key is never expired or evicted: one record exists per created task.
func (s *Store) CreateIdempotent(ctx context.Context, title string, identity AdmissionIdentity, maxTasks int) (Session, error) {
	if err := identity.Validate(); err != nil {
		return Session{}, err
	}
	if len(title) == 0 || len(title) > 160 {
		return Session{}, errors.New("invalid task title")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, err
	}
	defer tx.Rollback()
	var digest, payload string
	err = tx.QueryRowContext(ctx, `SELECT digest,created_session FROM task_creation_keys WHERE principal=? AND key=?`, identity.Principal, identity.Key).Scan(&digest, &payload)
	if err == nil {
		if digest != identity.Digest {
			return Session{}, ErrIdempotencyConflict
		}
		var result Session
		err = json.Unmarshal([]byte(payload), &result)
		return result, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Session{}, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sessions`).Scan(&count); err != nil {
		return Session{}, err
	}
	if maxTasks <= 0 || count >= maxTasks {
		return Session{}, errors.New("task capacity reached")
	}
	var token [16]byte
	if _, err = rand.Read(token[:]); err != nil {
		return Session{}, err
	}
	now := time.Now().UTC()
	result := Session{ID: hex.EncodeToString(token[:]), Title: title, CreatedAt: now, UpdatedAt: now}
	if _, err = tx.ExecContext(ctx, `INSERT INTO sessions(id,title,created_at,updated_at) VALUES(?,?,?,?)`, result.ID, title, now.Format(timestampFormat), now.Format(timestampFormat)); err != nil {
		return Session{}, err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return Session{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_creation_keys(principal,key,digest,session_id,created_session) VALUES(?,?,?,?,?)`, identity.Principal, identity.Key, identity.Digest, result.ID, string(data)); err != nil {
		return Session{}, err
	}
	if err = tx.Commit(); err != nil {
		return Session{}, err
	}
	return result, nil
}
