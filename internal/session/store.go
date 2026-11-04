// Package session persists conversations in a local SQLite database.
package session

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"gopi/internal/agent"
	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("session not found")
var ErrConflict = errors.New("session revision conflict")

const timestampFormat = "2006-01-02T15:04:05.000000000Z"

type Session struct {
	ID        string    `json:"id"`
	Revision  int64     `json:"revision"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Store struct {
	db   *sql.DB
	path string
}

// Open opens a private local database. Parent directories must already exist.
// The database uses WAL and full synchronization for durable committed writes.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("session database path is required")
	}
	// Reserve the file with private permissions before SQLite opens it. Treat all
	// input as filesystem paths rather than allowing SQLite URI configuration.
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(absolute, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	u := &url.URL{Scheme: "file", Path: absolute}
	query := url.Values{}
	for _, pragma := range []string{"busy_timeout(5000)", "foreign_keys(1)", "synchronous(FULL)"} {
		query.Add("_pragma", pragma)
	}
	u.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db, path: absolute}
	for _, statement := range []string{
		"PRAGMA journal_mode=WAL",
		`CREATE TABLE IF NOT EXISTS sessions (id TEXT PRIMARY KEY, title TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, revision INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS messages (session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE, position INTEGER NOT NULL CHECK(position >= 0), payload TEXT NOT NULL, PRIMARY KEY(session_id,position))`,
	} {
		if _, err = db.Exec(statement); err != nil {
			db.Close()
			return nil, fmt.Errorf("initialize session database: %w", err)
		}
	}
	// Upgrade databases written by the initial schema without rewriting history.
	rows, err := db.Query("PRAGMA table_info(sessions)")
	if err != nil {
		db.Close()
		return nil, err
	}
	hasRevision := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &kind, &notnull, &defaultValue, &pk); err != nil {
			rows.Close()
			db.Close()
			return nil, err
		}
		if name == "revision" {
			hasRevision = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		db.Close()
		return nil, err
	}
	if !hasRevision {
		if _, err = db.Exec("ALTER TABLE sessions ADD COLUMN revision INTEGER NOT NULL DEFAULT 0"); err != nil {
			db.Close()
			return nil, err
		}
	}
	if err = s.initTasks(); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize task storage: %w", err)
	}
	if err = s.initApprovals(); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize approvals: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Create(ctx context.Context, title string) (Session, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return Session{}, err
	}
	now := time.Now().UTC()
	result := Session{ID: hex.EncodeToString(token[:]), Title: title, CreatedAt: now, UpdatedAt: now}
	_, err := s.db.ExecContext(ctx, "INSERT INTO sessions(id,title,created_at,updated_at) VALUES(?,?,?,?)", result.ID, title, now.Format(timestampFormat), now.Format(timestampFormat))
	if err != nil {
		return Session{}, err
	}
	return result, nil
}

type scanner interface{ Scan(...any) error }

func scanSession(row scanner) (Session, error) {
	var result Session
	var created, updated string
	if err := row.Scan(&result.ID, &result.Title, &created, &updated, &result.Revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, ErrNotFound
		}
		return Session{}, err
	}
	var err error
	result.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Session{}, err
	}
	result.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return Session{}, err
	}
	return result, nil
}
func (s *Store) Get(ctx context.Context, id string) (Session, error) {
	return scanSession(s.db.QueryRowContext(ctx, "SELECT id,title,created_at,updated_at,revision FROM sessions WHERE id=?", id))
}
func (s *Store) List(ctx context.Context) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,title,created_at,updated_at,revision FROM sessions ORDER BY updated_at DESC,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Session{}
	for rows.Next() {
		item, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) Messages(ctx context.Context, id string) ([]agent.Message, error) {
	_, history, err := s.Load(ctx, id)
	return history, err
}

// Save atomically replaces a session's transcript, including tool calls and
// tool outputs. Callers should serialize runs for the same session: the last
// successful Save wins. Invalid JSON in tool arguments leaves history intact.
func (s *Store) Save(ctx context.Context, id string, messages []agent.Message) error {
	return s.save(ctx, id, -1, messages)
}

// SaveRevision replaces history only if its revision still matches the loaded
// snapshot. It does not make external tool execution exactly-once. On conflict,
// callers must not blindly replay tools that may already have had side effects.
func (s *Store) SaveRevision(ctx context.Context, id string, revision int64, messages []agent.Message) error {
	if revision < 0 {
		return errors.New("revision must be nonnegative")
	}
	return s.save(ctx, id, revision, messages)
}
func (s *Store) save(ctx context.Context, id string, revision int64, messages []agent.Message) error {
	payloads := make([][]byte, len(messages))
	for i, m := range messages {
		payload, err := json.Marshal(m)
		if err != nil {
			return fmt.Errorf("encode message %d: %w", i, err)
		}
		payloads[i] = payload
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	query := "UPDATE sessions SET updated_at=?, revision=revision+1 WHERE id=?"
	args := []any{time.Now().UTC().Format(timestampFormat), id}
	if revision >= 0 {
		query += " AND revision=?"
		args = append(args, revision)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT 1 FROM sessions WHERE id=?", id).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM messages WHERE session_id=?", id); err != nil {
		return err
	}
	for i, payload := range payloads {
		if _, err = tx.ExecContext(ctx, "INSERT INTO messages(session_id,position,payload) VALUES(?,?,?)", id, i, string(payload)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Load reads session metadata and history in one consistent SQLite snapshot.
func (s *Store) Load(ctx context.Context, id string) (Session, []agent.Message, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Session{}, nil, err
	}
	defer tx.Rollback()
	item, err := scanSession(tx.QueryRowContext(ctx, "SELECT id,title,created_at,updated_at,revision FROM sessions WHERE id=?", id))
	if err != nil {
		return Session{}, nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT payload FROM messages WHERE session_id=? ORDER BY position", id)
	if err != nil {
		return Session{}, nil, err
	}
	history := []agent.Message{}
	for rows.Next() {
		var payload []byte
		if err = rows.Scan(&payload); err != nil {
			rows.Close()
			return Session{}, nil, err
		}
		var m agent.Message
		if err = json.Unmarshal(payload, &m); err != nil {
			rows.Close()
			return Session{}, nil, err
		}
		history = append(history, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Session{}, nil, err
	}
	if err = tx.Commit(); err != nil {
		return Session{}, nil, err
	}
	return item, history, nil
}
