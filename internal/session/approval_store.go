package session

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrApprovalUnavailable covers stale, expired, denied, or already consumed
// decisions. Callers must never dispatch an action after this error.
var ErrApprovalUnavailable = errors.New("approval is stale, expired, denied, or already consumed")

// ApprovalAction is an exact operation description, not an executable command.
// Executors must allowlist Kind and bind every consequential input in Payload.
// Strings are preserved exactly; no path/URL/argument rewriting is permitted
// between approval and dispatch. Digesting does not authorize an operation.
type ApprovalAction struct {
	Kind    string          `json:"kind"`
	Target  string          `json:"target"`
	Payload json.RawMessage `json:"payload"`
}
type Approval struct {
	ID        string         `json:"id"`
	TaskID    string         `json:"task_id"`
	RunID     string         `json:"run_id"`
	Revision  int64          `json:"revision"`
	Digest    string         `json:"digest"`
	Action    ApprovalAction `json:"action"`
	Status    string         `json:"status"`
	CreatedAt time.Time      `json:"created_at"`
	ExpiresAt time.Time      `json:"expires_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// canonicalValue rejects ambiguous duplicate keys rather than silently choosing
// a different value from an executor's JSON decoder. Numbers retain precision.
func canonicalValue(d *json.Decoder) (any, error) {
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return t, nil
	}
	switch delim {
	case '{':
		m := map[string]any{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return nil, e
			}
			key, ok := k.(string)
			if !ok {
				return nil, errors.New("invalid object key")
			}
			if _, exists := m[key]; exists {
				return nil, errors.New("duplicate JSON key")
			}
			v, e := canonicalValue(d)
			if e != nil {
				return nil, e
			}
			m[key] = v
		}
		_, err = d.Token()
		return m, err
	case '[':
		a := []any{}
		for d.More() {
			v, e := canonicalValue(d)
			if e != nil {
				return nil, e
			}
			a = append(a, v)
		}
		_, err = d.Token()
		return a, err
	default:
		return nil, errors.New("unexpected JSON delimiter")
	}
}
func NormalizeApprovalAction(action ApprovalAction) (ApprovalAction, string, error) {
	if !utf8.ValidString(action.Kind) || !utf8.ValidString(action.Target) || !utf8.Valid(action.Payload) || !json.Valid(action.Payload) || strings.TrimSpace(action.Kind) == "" || strings.TrimSpace(action.Target) == "" || len(action.Kind) > 128 || len(action.Target) > 4096 || len(action.Payload) > 1024*1024 {
		return ApprovalAction{}, "", errors.New("invalid approval action")
	}
	d := json.NewDecoder(bytes.NewReader(action.Payload))
	d.UseNumber()
	v, err := canonicalValue(d)
	if err != nil {
		return ApprovalAction{}, "", err
	}
	if _, err = d.Token(); err != io.EOF {
		return ApprovalAction{}, "", errors.New("trailing JSON data")
	}
	action.Payload, err = json.Marshal(v)
	if err != nil {
		return ApprovalAction{}, "", err
	}
	data, err := json.Marshal(action)
	if err != nil {
		return ApprovalAction{}, "", err
	}
	digest := sha256.Sum256(append([]byte("qi-approval-v1\x00"), data...))
	return action, hex.EncodeToString(digest[:]), nil
}
func (s *Store) initApprovals() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS approvals (
 id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES sessions(id), run_id TEXT NOT NULL,
 revision INTEGER NOT NULL CHECK(revision>0), digest TEXT NOT NULL, action TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('pending','approved','denied','executing','succeeded','failed','unknown')),
 created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
 FOREIGN KEY(run_id,task_id) REFERENCES runs(id,session_id))`)
	return err
}

const approvalColumns = `id,task_id,run_id,revision,digest,action,status,created_at,expires_at,updated_at`

func scanApproval(row scanner) (Approval, error) {
	var a Approval
	var raw string
	var created, expires, updated int64
	err := row.Scan(&a.ID, &a.TaskID, &a.RunID, &a.Revision, &a.Digest, &raw, &a.Status, &created, &expires, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	if err = json.Unmarshal([]byte(raw), &a.Action); err != nil {
		return a, err
	}
	a.CreatedAt = time.Unix(0, created).UTC()
	a.ExpiresAt = time.Unix(0, expires).UTC()
	a.UpdatedAt = time.Unix(0, updated).UTC()
	return a, nil
}
func (s *Store) RequestApproval(ctx context.Context, taskID, runID string, action ApprovalAction, expiry time.Time) (Approval, error) {
	normalized, digest, err := NormalizeApprovalAction(action)
	if err != nil {
		return Approval{}, err
	}
	now := time.Now().UTC()
	if !expiry.After(now) || expiry.After(now.Add(365*24*time.Hour)) {
		return Approval{}, errors.New("approval expiry must be in the next year")
	}
	var token [16]byte
	if _, err = rand.Read(token[:]); err != nil {
		return Approval{}, err
	}
	id := hex.EncodeToString(token[:])
	raw, _ := json.Marshal(normalized)
	return scanApproval(s.db.QueryRowContext(ctx, `INSERT INTO approvals(id,task_id,run_id,revision,digest,action,status,created_at,expires_at,updated_at) SELECT ?,session_id,id,1,?,?,'pending',?,?,? FROM runs WHERE id=? AND session_id=? RETURNING `+approvalColumns, id, digest, string(raw), now.UnixNano(), expiry.UnixNano(), now.UnixNano(), runID, taskID))
}
func (s *Store) GetApproval(ctx context.Context, taskID, id string) (Approval, error) {
	return scanApproval(s.db.QueryRowContext(ctx, `SELECT `+approvalColumns+` FROM approvals WHERE task_id=? AND id=?`, taskID, id))
}
func (s *Store) ListApprovals(ctx context.Context, taskID string) ([]Approval, error) {
	if _, err := s.Get(ctx, taskID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+approvalColumns+` FROM approvals WHERE task_id=? ORDER BY created_at,id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Approval{}
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}
func approvalTransition(a Approval, err error) (Approval, error) {
	if errors.Is(err, ErrNotFound) {
		return Approval{}, ErrApprovalUnavailable
	}
	return a, err
}

// Check again after SQLite contention: the timestamp passed to the atomic SQL
// predicate may predate a wait for the writer lock. A late claim stays consumed
// and must not dispatch. It is conservatively recovered as unknown on restart.
func approvalFreshTransition(a Approval, err error) (Approval, error) {
	a, err = approvalTransition(a, err)
	if err == nil && !a.ExpiresAt.After(time.Now()) {
		return Approval{}, ErrApprovalUnavailable
	}
	return a, err
}

// DecideApproval must only be called from the authenticated user decision path,
// never from a model tool or an informational card. Revision and digest must be
// the values actually presented to the user.
func (s *Store) DecideApproval(ctx context.Context, taskID, id string, revision int64, digest string, allow bool) (Approval, error) {
	status := "denied"
	if allow {
		status = "approved"
	}
	now := time.Now().UTC().UnixNano()
	return approvalFreshTransition(scanApproval(s.db.QueryRowContext(ctx, `UPDATE approvals SET status=?,revision=revision+1,updated_at=? WHERE task_id=? AND id=? AND revision=? AND digest=? AND status='pending' AND expires_at>? RETURNING `+approvalColumns, status, now, taskID, id, revision, digest, now)))
}

// ClaimApproval durably consumes a decision before any dispatch. A crash after
// this point is UNKNOWN, not evidence that dispatch did or did not occur. Never
// retry a claim or effect blindly after a database error with uncertain commit.
// A future executor must dispatch the returned canonical Action, not untrusted
// original request bytes, and must retain its own allowlist and validation.
func (s *Store) ClaimApproval(ctx context.Context, taskID, id string, revision int64, action ApprovalAction) (Approval, error) {
	_, digest, err := NormalizeApprovalAction(action)
	if err != nil {
		return Approval{}, err
	}
	now := time.Now().UTC().UnixNano()
	return approvalFreshTransition(scanApproval(s.db.QueryRowContext(ctx, `UPDATE approvals SET status='executing',revision=revision+1,updated_at=? WHERE task_id=? AND id=? AND revision=? AND digest=? AND status='approved' AND expires_at>? RETURNING `+approvalColumns, now, taskID, id, revision, digest, now)))
}
func (s *Store) FinishApproval(ctx context.Context, taskID, id string, revision int64, outcome string) (Approval, error) {
	if outcome != "succeeded" && outcome != "failed" && outcome != "unknown" {
		return Approval{}, fmt.Errorf("invalid approval outcome %q", outcome)
	}
	return approvalTransition(scanApproval(s.db.QueryRowContext(ctx, `UPDATE approvals SET status=?,revision=revision+1,updated_at=? WHERE task_id=? AND id=? AND revision=? AND status='executing' RETURNING `+approvalColumns, outcome, time.Now().UTC().UnixNano(), taskID, id, revision)))
}

// InterruptApprovals requires exclusive engine ownership. It never replays work.
func (s *Store) InterruptApprovals(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE approvals SET status='unknown',revision=revision+1,updated_at=? WHERE status='executing'`, time.Now().UTC().UnixNano())
	return err
}
