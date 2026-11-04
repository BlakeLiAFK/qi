package session

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestAdmissionIdentityAtomicRollbackReplayAndBound(t *testing.T) {
	s := queueStore(t)
	ctx := context.Background()
	task, _ := s.Create(ctx, "atomic")
	identity := AdmissionIdentity{"owner", "key", strings.Repeat("a", 64)}
	before, _ := s.LoadTaskSnapshot(ctx, task.ID)
	if _, err := s.db.Exec(`CREATE TRIGGER fail_identity BEFORE INSERT ON admission_keys BEGIN SELECT RAISE(ABORT,'injected identity failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptQueued(ctx, task.ID, 0, queuePrompt("prompt"), "", nil, 1, identity); err == nil {
		t.Fatal("expected write failure")
	}
	after, _ := s.LoadTaskSnapshot(ctx, task.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed identity write left partial admission")
	}
	if _, found, err := s.LookupAdmission(ctx, task.ID, identity); err != nil || found {
		t.Fatal(found, err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER fail_identity`); err != nil {
		t.Fatal(err)
	}
	accepted, err := s.AcceptQueued(ctx, task.ID, 0, queuePrompt("prompt"), "", nil, 1, identity)
	if err != nil {
		t.Fatal(err)
	}
	// Replay bypasses stale revision and saturated queue, preserving response.
	retry, err := s.AcceptQueued(ctx, task.ID, 0, queuePrompt("prompt"), "", nil, 1, identity)
	if err != nil || !reflect.DeepEqual(accepted, retry) {
		t.Fatal(retry, err)
	}
	changed := identity
	changed.Digest = strings.Repeat("b", 64)
	if _, err = s.AcceptQueued(ctx, task.ID, 0, queuePrompt("different"), "", nil, 1, changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<9999) INSERT INTO admission_keys SELECT session_id,principal,'fill-'||x,digest,accepted_run,run_id FROM admission_keys,n WHERE key='key'`); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishRun(ctx, accepted.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	fresh := identity
	fresh.Key = "new"
	if _, err = s.AcceptQueued(ctx, task.ID, 1, queuePrompt("next"), "", nil, 1, fresh); !errors.Is(err, ErrIdempotencyCapacity) {
		t.Fatal(err)
	}
	if _, found, err := s.LookupAdmission(ctx, task.ID, identity); err != nil || !found {
		t.Fatal("capacity evicted old key", err)
	}
}
func TestAdmissionIdentityValidation(t *testing.T) {
	for _, key := range []string{"", "contains space", "line\nbreak", strings.Repeat("a", 129), "非ascii"} {
		if err := (AdmissionIdentity{"owner", key, strings.Repeat("a", 64)}).Validate(); err == nil {
			t.Fatal(key)
		}
	}
}

func TestAdmissionSurvivesDatabaseReopen(t *testing.T) {
	s := queueStore(t)
	ctx := context.Background()
	task, _ := s.Create(ctx, "reopen")
	identity := AdmissionIdentity{"owner", "reopen", strings.Repeat("c", 64)}
	accepted, err := s.AcceptQueued(ctx, task.ID, 0, queuePrompt("accepted"), "", nil, 1, identity)
	if err != nil {
		t.Fatal(err)
	}
	path := s.path
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	retry, found, err := reopened.LookupAdmission(ctx, task.ID, identity)
	if err != nil || !found || !reflect.DeepEqual(retry, accepted) {
		t.Fatal(retry, found, err)
	}
}
