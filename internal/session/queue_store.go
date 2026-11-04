package session

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"gopi/internal/agent"
)

var ErrQueueFull = errors.New("task queue capacity reached")

// EnsureQueueSchema upgrades the run constraint only under exclusive engine
// ownership. Foreign keys are disabled on the sole database connection only for
// the atomic table replacement, then restored before this method returns.
func (s *Store) EnsureQueueSchema(ctx context.Context) (resultErr error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer func() {
		_, restoreErr := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`)
		if restoreErr == nil {
			var enabled int
			restoreErr = conn.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&enabled)
			if restoreErr == nil && enabled != 1 {
				restoreErr = errors.New("foreign key enforcement was not restored")
			}
		}
		resultErr = errors.Join(resultErr, restoreErr)
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Schema version is represented by the persisted dispatch table, installed in
	// this same transaction. Existing rows and foreign-key target names survive.
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='dispatch_records'`).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		for _, q := range []string{
			`CREATE TABLE runs_queued_upgrade (id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id), status TEXT NOT NULL CHECK(status IN ('queued','running','completed','failed','cancelled','interrupted')), error TEXT NOT NULL DEFAULT '', started_at TEXT NOT NULL, finished_at TEXT NOT NULL DEFAULT '', UNIQUE(id,session_id))`,
			`INSERT INTO runs_queued_upgrade SELECT * FROM runs`,
			`DROP TABLE runs`,
			`ALTER TABLE runs_queued_upgrade RENAME TO runs`,
			`CREATE UNIQUE INDEX one_running_per_session ON runs(session_id) WHERE status IN ('queued','running')`,
			`CREATE INDEX runs_by_session ON runs(session_id,started_at)`,
			`CREATE INDEX latest_run_by_session ON runs(session_id,started_at DESC,id DESC)`,
			`CREATE TABLE dispatch_records (seq INTEGER PRIMARY KEY AUTOINCREMENT, run_id TEXT NOT NULL UNIQUE, session_id TEXT NOT NULL, revision INTEGER NOT NULL, FOREIGN KEY(run_id,session_id) REFERENCES runs(id,session_id))`,
		} {
			if _, err = tx.ExecContext(ctx, q); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS admission_keys (session_id TEXT NOT NULL, principal TEXT NOT NULL, key TEXT NOT NULL, digest TEXT NOT NULL, accepted_run TEXT NOT NULL, run_id TEXT NOT NULL, PRIMARY KEY(session_id,principal,key), FOREIGN KEY(run_id,session_id) REFERENCES runs(id,session_id))`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	invalid := rows.Next()
	checkErr := rows.Err()
	rows.Close()
	if checkErr != nil {
		return checkErr
	}
	if invalid {
		return errors.New("queue migration would leave invalid foreign keys")
	}
	var integrity string
	if err = tx.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return errors.New("queue migration integrity check failed")
	}
	return tx.Commit()
}

// AcceptQueued durably accepts a prompt and optionally consumes a question card
// in one transaction. The caller serializes card validation under engine.mu.
func (s *Store) AcceptQueued(ctx context.Context, id string, revision int64, history []agent.Message, sourceRun string, card json.RawMessage, maxQueued int, identities ...AdmissionIdentity) (Run, error) {
	if len(identities) > 1 {
		return Run{}, errors.New("only one admission identity allowed")
	}
	if len(identities) == 1 {
		if err := identities[0].Validate(); err != nil {
			return Run{}, err
		}
	}
	if revision < 0 {
		return Run{}, ErrConflict
	}
	if len(card) > 0 && (!json.Valid(card) || sourceRun == "") {
		return Run{}, errors.New("card requires valid JSON and source run")
	}
	payloads := make([][]byte, len(history))
	for i, m := range history {
		p, err := json.Marshal(m)
		if err != nil {
			return Run{}, err
		}
		payloads[i] = p
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return Run{}, err
	}
	now := time.Now().UTC()
	run := Run{ID: hex.EncodeToString(token[:]), SessionID: id, Status: "queued", StartedAt: now}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()
	if len(identities) == 1 {
		if prior, found, err := lookupAdmission(ctx, tx, id, identities[0]); err != nil || found {
			return prior, err
		}
		var keys int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM admission_keys WHERE session_id=?`, id).Scan(&keys); err != nil {
			return Run{}, err
		}
		if keys >= MaxAdmissionKeysPerTask {
			return Run{}, ErrIdempotencyCapacity
		}
	}
	var archived bool
	if err = tx.QueryRowContext(ctx, `SELECT archived FROM sessions WHERE id=?`, id).Scan(&archived); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, ErrNotFound
		}
		return Run{}, err
	}
	if archived {
		return Run{}, ErrArchived
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE status='queued'`).Scan(&count); err != nil {
		return Run{}, err
	}
	if maxQueued <= 0 || count >= maxQueued {
		return Run{}, ErrQueueFull
	}
	result, err := tx.ExecContext(ctx, `UPDATE sessions SET revision=revision+1,updated_at=? WHERE id=? AND revision=?`, now.Format(timestampFormat), id, revision)
	if err != nil {
		return Run{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return Run{}, err
	}
	if n != 1 {
		return Run{}, ErrConflict
	}
	result, err = tx.ExecContext(ctx, `INSERT INTO runs(id,session_id,status,started_at) VALUES(?,?,'queued',?) ON CONFLICT DO NOTHING`, run.ID, id, now.Format(timestampFormat))
	if err != nil {
		return Run{}, err
	}
	n, err = result.RowsAffected()
	if err != nil {
		return Run{}, err
	}
	if n != 1 {
		return Run{}, ErrRunActive
	}
	if len(card) > 0 {
		result, err = tx.ExecContext(ctx, `INSERT INTO events(session_id,run_id,type,payload,created_at) SELECT session_id,id,'card',?,? FROM runs WHERE id=? AND session_id=?`, string(card), now.Format(timestampFormat), sourceRun, id)
		if err != nil {
			return Run{}, err
		}
		n, err = result.RowsAffected()
		if err != nil {
			return Run{}, err
		}
		if n != 1 {
			return Run{}, ErrNotFound
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM messages WHERE session_id=?`, id); err != nil {
		return Run{}, err
	}
	for i, p := range payloads {
		if _, err = tx.ExecContext(ctx, `INSERT INTO messages(session_id,position,payload) VALUES(?,?,?)`, id, i, string(p)); err != nil {
			return Run{}, err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO dispatch_records(run_id,session_id,revision) VALUES(?,?,?)`, run.ID, id, revision+1); err != nil {
		return Run{}, err
	}
	if len(identities) == 1 {
		identity := identities[0]
		payload, err := json.Marshal(run)
		if err != nil {
			return Run{}, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO admission_keys(session_id,principal,key,digest,accepted_run,run_id) VALUES(?,?,?,?,?,?)`, id, identity.Principal, identity.Key, identity.Digest, string(payload), run.ID); err != nil {
			return Run{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return Run{}, err
	}
	return run, nil
}

// ClaimNextQueued consumes FIFO dispatch state before starting any model work.
// A crash after claim is interrupted at startup, never automatically retried.
func (s *Store) ClaimNextQueued(ctx context.Context) (Run, int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, 0, err
	}
	defer tx.Rollback()
	var run Run
	var revision int64
	var started string
	err = tx.QueryRowContext(ctx, `SELECT r.id,r.session_id,r.status,r.started_at,d.revision FROM dispatch_records d JOIN runs r ON r.id=d.run_id AND r.session_id=d.session_id WHERE r.status='queued' ORDER BY d.seq LIMIT 1`).Scan(&run.ID, &run.SessionID, &run.Status, &started, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, 0, ErrNotFound
	}
	if err != nil {
		return Run{}, 0, err
	}
	run.StartedAt, err = time.Parse(time.RFC3339Nano, started)
	if err != nil {
		return Run{}, 0, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE runs SET status='running' WHERE id=? AND status='queued'`, run.ID)
	if err != nil {
		return Run{}, 0, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return Run{}, 0, err
	}
	if n != 1 {
		return Run{}, 0, ErrConflict
	}
	if err = tx.Commit(); err != nil {
		return Run{}, 0, err
	}
	run.Status = "running"
	return run, revision, nil
}

func (s *Store) QueuedRuns(ctx context.Context) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id,r.session_id,r.status,r.started_at FROM dispatch_records d JOIN runs r ON r.id=d.run_id AND r.session_id=d.session_id WHERE r.status='queued' ORDER BY d.seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		var run Run
		var started string
		if err = rows.Scan(&run.ID, &run.SessionID, &run.Status, &started); err != nil {
			return nil, err
		}
		run.StartedAt, err = time.Parse(time.RFC3339Nano, started)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}
func (s *Store) CancelQueued(ctx context.Context, id string) error {
	if _, err := s.Get(ctx, id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET status='cancelled',error='cancelled before dispatch',finished_at=? WHERE session_id=? AND status='queued'`, time.Now().UTC().Format(timestampFormat), id)
	return err
}
