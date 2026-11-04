package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

var ErrIdempotencyConflict = errors.New("idempotency key already used with a different request")
var ErrIdempotencyCapacity = errors.New("task idempotency capacity reached; create a new task")

// AdmissionIdentity is scoped to an authenticated principal and task. Keys are
// retained for the task lifetime, never silently expired or evicted.
type AdmissionIdentity struct{ Principal, Key, Digest string }

const MaxAdmissionKeysPerTask = 10000

func (i AdmissionIdentity) Validate() error {
	if len(i.Principal) == 0 || len(i.Principal) > 128 || len(i.Key) == 0 || len(i.Key) > 128 || len(i.Digest) != 64 {
		return errors.New("invalid admission identity")
	}
	for _, c := range i.Key {
		if c < 33 || c > 126 {
			return errors.New("invalid idempotency key")
		}
	}
	return nil
}

type admissionReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func lookupAdmission(ctx context.Context, q admissionReader, task string, identity AdmissionIdentity) (Run, bool, error) {
	var digest, payload string
	err := q.QueryRowContext(ctx, `SELECT digest,accepted_run FROM admission_keys WHERE session_id=? AND principal=? AND key=?`, task, identity.Principal, identity.Key).Scan(&digest, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, err
	}
	if digest != identity.Digest {
		return Run{}, false, ErrIdempotencyConflict
	}
	var run Run
	err = json.Unmarshal([]byte(payload), &run)
	return run, true, err
}
func (s *Store) LookupAdmission(ctx context.Context, task string, identity AdmissionIdentity) (Run, bool, error) {
	if err := identity.Validate(); err != nil {
		return Run{}, false, err
	}
	return lookupAdmission(ctx, s.db, task, identity)
}
