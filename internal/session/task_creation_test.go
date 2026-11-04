package session

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestTaskCreationAtomicRollbackReopenAndCapacity(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	identity := AdmissionIdentity{"owner", "creation", strings.Repeat("a", 64)}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_creation_key BEFORE INSERT ON task_creation_keys BEGIN SELECT RAISE(ABORT,'injected key failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateIdempotent(ctx, "title", identity, 1); err == nil {
		t.Fatal("expected rollback")
	}
	list, err := s.List(ctx)
	if err != nil || len(list) != 0 {
		t.Fatal("orphaned task", list, err)
	}
	if _, err = s.db.Exec(`DROP TRIGGER reject_creation_key`); err != nil {
		t.Fatal(err)
	}
	created, err := s.CreateIdempotent(ctx, "title", identity, 1)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s.CreateIdempotent(ctx, "title", identity, 1)
	if err != nil || !reflect.DeepEqual(retry, created) {
		t.Fatal(retry, err)
	}
	changed := identity
	changed.Digest = strings.Repeat("b", 64)
	if _, err = s.CreateIdempotent(ctx, "changed", changed, 1); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal(err)
	}
	fresh := identity
	fresh.Key = "fresh"
	if _, err = s.CreateIdempotent(ctx, "title", fresh, 1); err == nil {
		t.Fatal("capacity bypassed")
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
	retry, err = reopened.CreateIdempotent(ctx, "title", identity, 1)
	if err != nil || !reflect.DeepEqual(retry, created) {
		t.Fatal("reopen lost acceptance", retry, err)
	}
	list, err = reopened.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
}
