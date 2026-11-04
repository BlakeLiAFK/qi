package session

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxMemoryRecords      = 1024
	MaxMemoryVersions     = 16
	MaxMemoryContentBytes = 4096
)

var (
	ErrMemoryNotFound = errors.New("memory not found in selected scope")
	ErrMemoryConflict = errors.New("memory revision conflict")
	ErrMemoryLimit    = errors.New("memory storage limit reached")
)

// MemoryScope must select exactly one task or explicitly select global memory.
// It is a storage filter, not an authentication or authorization credential.
type MemoryScope struct {
	TaskID string `json:"task_id,omitempty"`
	Global bool   `json:"global,omitempty"`
}

func (s MemoryScope) key() (string, error) {
	if s.Global && s.TaskID == "" {
		return "global", nil
	}
	if !s.Global && validMemoryText(s.TaskID, 128) {
		return "task:" + s.TaskID, nil
	}
	return "", errors.New("select exactly one memory task or explicit global scope")
}

// MemoryFact is user-curated data, never executable policy or tool authority.
// ApprovalRef identifies the user's approval of this exact content and scope.
// These references are audit metadata; trusted callers must verify approval.
// Sensitive information and credentials are deliberately unsupported.
type MemoryFact struct {
	Content         string     `json:"content"`
	SourceRef       string     `json:"source_ref"`
	ApprovalRef     string     `json:"approval_ref"`
	Kind            string     `json:"kind"` // preference, verified, or inferred
	Confidence      float64    `json:"confidence"`
	Confidentiality string     `json:"confidentiality"` // public or private
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
}

type MemoryRecord struct {
	ID        string      `json:"id"`
	Scope     MemoryScope `json:"scope"`
	Revision  int64       `json:"revision"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
	MemoryFact
}

func validateMemoryID(id string) error {
	if len(id) != 32 {
		return errors.New("invalid memory ID")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return errors.New("invalid memory ID")
	}
	return nil
}

func requireMemoryMutation(result sql.Result) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrMemoryConflict
	}
	return nil
}

func validMemoryText(s string, max int) bool {
	return strings.TrimSpace(s) != "" && len(s) <= max && utf8.ValidString(s) && !strings.ContainsRune(s, '\x00')
}
func (f MemoryFact) validate() error {
	if !validMemoryText(f.Content, MaxMemoryContentBytes) || !validMemoryText(f.SourceRef, 512) || !validMemoryText(f.ApprovalRef, 512) {
		return errors.New("memory requires bounded content, source, and explicit approval reference")
	}
	if f.Kind != "preference" && f.Kind != "verified" && f.Kind != "inferred" {
		return errors.New("invalid memory kind")
	}
	if math.IsNaN(f.Confidence) || math.IsInf(f.Confidence, 0) || f.Confidence < 0 || f.Confidence > 1 {
		return errors.New("memory confidence must be between zero and one")
	}
	if f.Confidentiality != "private" && f.Confidentiality != "public" {
		return errors.New("memory confidentiality must be public or private; sensitive data and secrets are unsupported")
	}
	if f.ExpiresAt != nil && (f.ExpiresAt.IsZero() || f.ExpiresAt.Year() < 1 || f.ExpiresAt.Year() > 9999) {
		return errors.New("invalid memory expiry")
	}
	return nil
}

func (s *Store) initMemory() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS curated_memory (
 id TEXT PRIMARY KEY, scope TEXT NOT NULL, task_id TEXT REFERENCES sessions(id) ON DELETE CASCADE,
 revision INTEGER NOT NULL CHECK(revision BETWEEN 1 AND 16),
 CHECK((scope='global' AND task_id IS NULL) OR (task_id IS NOT NULL AND scope='task:' || task_id))
 );
 CREATE INDEX IF NOT EXISTS curated_memory_scope ON curated_memory(scope);
 CREATE TABLE IF NOT EXISTS curated_memory_versions (
 memory_id TEXT NOT NULL REFERENCES curated_memory(id) ON DELETE CASCADE,
 revision INTEGER NOT NULL CHECK(revision BETWEEN 1 AND 16),
 payload TEXT NOT NULL CHECK(length(CAST(payload AS BLOB)) <= 32768),
 PRIMARY KEY(memory_id,revision)
 );
 CREATE TABLE IF NOT EXISTS turn_memory (run_id TEXT PRIMARY KEY, session_id TEXT NOT NULL, payload TEXT NOT NULL, destination TEXT NOT NULL, FOREIGN KEY(run_id,session_id) REFERENCES runs(id,session_id));`)
	return err
}

// CreateMemory is only for a trusted, explicitly approved user operation. There
// is intentionally no transcript extractor, model tool, or default global read.
func (s *Store) CreateMemory(ctx context.Context, scope MemoryScope, fact MemoryFact) (MemoryRecord, error) {
	key, err := scope.key()
	if err != nil {
		return MemoryRecord{}, err
	}
	if err = fact.validate(); err != nil {
		return MemoryRecord{}, err
	}
	var token [16]byte
	if _, err = rand.Read(token[:]); err != nil {
		return MemoryRecord{}, err
	}
	now := time.Now().UTC()
	record := MemoryRecord{ID: hex.EncodeToString(token[:]), Scope: scope, Revision: 1, CreatedAt: now, UpdatedAt: now, MemoryFact: fact}
	payload, err := json.Marshal(record)
	if err != nil {
		return MemoryRecord{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MemoryRecord{}, err
	}
	defer tx.Rollback()
	var task any
	if !scope.Global {
		task = scope.TaskID
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO curated_memory(id,scope,task_id,revision) SELECT ?,?,?,1 WHERE (SELECT COUNT(*) FROM curated_memory) < ?`, record.ID, key, task, MaxMemoryRecords)
	if err != nil {
		return MemoryRecord{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return MemoryRecord{}, err
	}
	if count == 0 {
		return MemoryRecord{}, ErrMemoryLimit
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO curated_memory_versions(memory_id,revision,payload) VALUES(?,1,?)`, record.ID, string(payload)); err != nil {
		return MemoryRecord{}, err
	}
	if err = tx.Commit(); err != nil {
		return MemoryRecord{}, err
	}
	return record, nil
}

const memorySelect = `SELECT v.payload FROM curated_memory m JOIN curated_memory_versions v ON v.memory_id=m.id AND v.revision=m.revision`

func decodeMemory(row scanner) (MemoryRecord, error) {
	var payload string
	if err := row.Scan(&payload); errors.Is(err, sql.ErrNoRows) {
		return MemoryRecord{}, ErrMemoryNotFound
	} else if err != nil {
		return MemoryRecord{}, err
	}
	var record MemoryRecord
	err := json.Unmarshal([]byte(payload), &record)
	return record, err
}

// GetMemory returns even expired records for explicit user inspection/editing.
func (s *Store) GetMemory(ctx context.Context, scope MemoryScope, id string) (MemoryRecord, error) {
	if err := validateMemoryID(id); err != nil {
		return MemoryRecord{}, err
	}
	key, err := scope.key()
	if err != nil {
		return MemoryRecord{}, err
	}
	return decodeMemory(s.db.QueryRowContext(ctx, memorySelect+` WHERE m.scope=? AND m.id=?`, key, id))
}

// ListMemory is an explicit management operation. It includes expired records,
// never combines scopes, and must not be wired into automatic prompt building.
func (s *Store) ListMemory(ctx context.Context, scope MemoryScope) ([]MemoryRecord, error) {
	key, err := scope.key()
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, memorySelect+` WHERE m.scope=? ORDER BY m.id LIMIT ?`, key, MaxMemoryRecords)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []MemoryRecord{}
	for rows.Next() {
		record, err := decodeMemory(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// RetrieveMemory selects only explicitly named, unexpired records in one scope.
// It returns all-or-nothing on missing/expired IDs. Global selection is never
// implied by a task selection. Returned content remains untrusted reference data.
func (s *Store) RetrieveMemory(ctx context.Context, scope MemoryScope, ids []string) ([]MemoryRecord, error) {
	if _, err := scope.key(); err != nil {
		return nil, err
	}
	if len(ids) > 32 {
		return nil, ErrMemoryLimit
	}
	for _, id := range ids {
		if err := validateMemoryID(id); err != nil {
			return nil, err
		}
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	key, _ := scope.key()
	records := []MemoryRecord{}
	seen := map[string]bool{}
	now := time.Now().UTC()
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		record, err := decodeMemory(tx.QueryRowContext(ctx, memorySelect+` WHERE m.scope=? AND m.id=?`, key, id))
		if err != nil {
			return nil, err
		}
		if record.ExpiresAt != nil && !record.ExpiresAt.After(now) {
			return nil, ErrMemoryNotFound
		}
		records = append(records, record)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return records, nil
}

// EditMemory preserves prior versions and requires a different explicit approval
// reference for the replacement. The caller must verify approval freshness and
// revocations, including reuse of older references; a new string is not proof.
// Scope cannot be changed by editing a record.
func (s *Store) EditMemory(ctx context.Context, scope MemoryScope, id string, revision int64, fact MemoryFact) (MemoryRecord, error) {
	if err := validateMemoryID(id); err != nil {
		return MemoryRecord{}, err
	}
	key, err := scope.key()
	if err != nil {
		return MemoryRecord{}, err
	}
	if err = fact.validate(); err != nil {
		return MemoryRecord{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MemoryRecord{}, err
	}
	defer tx.Rollback()
	record, err := decodeMemory(tx.QueryRowContext(ctx, memorySelect+` WHERE m.scope=? AND m.id=?`, key, id))
	if err != nil {
		return MemoryRecord{}, err
	}
	if record.Revision != revision {
		return MemoryRecord{}, ErrMemoryConflict
	}
	if revision >= MaxMemoryVersions {
		return MemoryRecord{}, ErrMemoryLimit
	}
	if record.ApprovalRef == fact.ApprovalRef {
		return MemoryRecord{}, errors.New("memory edit requires a new approval reference")
	}
	record.Revision++
	record.UpdatedAt = time.Now().UTC()
	record.MemoryFact = fact
	payload, err := json.Marshal(record)
	if err != nil {
		return MemoryRecord{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE curated_memory SET revision=? WHERE id=? AND scope=? AND revision=?`, record.Revision, id, key, revision)
	if err != nil {
		return MemoryRecord{}, err
	}
	if err = requireMemoryMutation(result); err != nil {
		return MemoryRecord{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO curated_memory_versions(memory_id,revision,payload) VALUES(?,?,?)`, id, record.Revision, string(payload)); err != nil {
		return MemoryRecord{}, err
	}
	if err = tx.Commit(); err != nil {
		return MemoryRecord{}, err
	}
	return record, nil
}

// MemoryHistory is user inspection only; previous facts are not retrieval hits.
func (s *Store) MemoryHistory(ctx context.Context, scope MemoryScope, id string) ([]MemoryRecord, error) {
	if err := validateMemoryID(id); err != nil {
		return nil, err
	}
	key, err := scope.key()
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT v.payload FROM curated_memory m JOIN curated_memory_versions v ON v.memory_id=m.id WHERE m.scope=? AND m.id=? ORDER BY v.revision`, key, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []MemoryRecord{}
	for rows.Next() {
		record, err := decodeMemory(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, ErrMemoryNotFound
	}
	return records, nil
}

// DeleteMemory removes every version from live storage. It does not promise
// forensic erasure of SQLite pages, WAL, backups, or already exported context.
// Callers must obtain explicit user authorization before deletion.
func (s *Store) DeleteMemory(ctx context.Context, scope MemoryScope, id string, revision int64) error {
	if err := validateMemoryID(id); err != nil {
		return err
	}
	key, err := scope.key()
	if err != nil {
		return err
	}
	if revision < 1 {
		return fmt.Errorf("invalid memory revision")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	record, err := decodeMemory(tx.QueryRowContext(ctx, memorySelect+` WHERE m.scope=? AND m.id=?`, key, id))
	if err != nil {
		return err
	}
	if record.Revision != revision {
		return ErrMemoryConflict
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM curated_memory WHERE id=? AND scope=? AND revision=?`, id, key, revision)
	if err != nil {
		return err
	}
	if err = requireMemoryMutation(result); err != nil {
		return err
	}
	return tx.Commit()
}
