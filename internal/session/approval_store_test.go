package session

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func approvalFixture(t *testing.T) (*Store, string, Approval) {
	t.Helper()
	s, path := openTest(t)
	ctx := context.Background()
	task, err := s.Create(ctx, "approval task")
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.CreateRun(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.RequestApproval(ctx, task.ID, run.ID, ApprovalAction{Kind: "test.noop", Target: "local", Payload: json.RawMessage(`{"b":2,"a":1}`)}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return s, path, a
}
func TestApprovalCanonicalBinding(t *testing.T) {
	action := ApprovalAction{Kind: "test.noop", Target: "local", Payload: json.RawMessage(`{"b":2, "a":1}`)}
	a, first, err := NormalizeApprovalAction(action)
	if err != nil {
		t.Fatal(err)
	}
	action.Payload = json.RawMessage(`{ "a": 1,"b":2 }`)
	_, second, err := NormalizeApprovalAction(action)
	if err != nil || first != second {
		t.Fatalf("normalization: %s %s %v", first, second, err)
	}
	if string(a.Payload) != `{"a":1,"b":2}` {
		t.Fatal(string(a.Payload))
	}
	for _, bad := range []string{`{"a":1,"a":2}`, `{"nested":{"a":1,"a":2}}`, `{} {}`, ``, `{"a": NaN}`, `[1}`} {
		action.Payload = json.RawMessage(bad)
		if _, _, err = NormalizeApprovalAction(action); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	action.Payload = json.RawMessage(`{"a":9007199254740993}`)
	normalized, _, err := NormalizeApprovalAction(action)
	if err != nil || string(normalized.Payload) != string(action.Payload) {
		t.Fatal("lost precision", normalized, err)
	}
}
func TestApprovalDecisionClaimCompletion(t *testing.T) {
	s, _, a := approvalFixture(t)
	ctx := context.Background()
	if _, err := s.ClaimApproval(ctx, a.TaskID, a.ID, a.Revision, a.Action); !errors.Is(err, ErrApprovalUnavailable) {
		t.Fatal("unapproved claim", err)
	}
	for _, trial := range []struct {
		task, digest string
		rev          int64
	}{{"other", a.Digest, a.Revision}, {a.TaskID, "wrong", a.Revision}, {a.TaskID, a.Digest, a.Revision + 1}} {
		if _, err := s.DecideApproval(ctx, trial.task, a.ID, trial.rev, trial.digest, true); !errors.Is(err, ErrApprovalUnavailable) {
			t.Fatal("invalid decision", err)
		}
	}
	approved, err := s.DecideApproval(ctx, a.TaskID, a.ID, a.Revision, a.Digest, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DecideApproval(ctx, a.TaskID, a.ID, a.Revision, a.Digest, true); !errors.Is(err, ErrApprovalUnavailable) {
		t.Fatal("repeated decision", err)
	}
	changed := a.Action
	changed.Target = "elsewhere"
	if _, err = s.ClaimApproval(ctx, a.TaskID, a.ID, approved.Revision, changed); !errors.Is(err, ErrApprovalUnavailable) {
		t.Fatal("changed target", err)
	}
	changed = a.Action
	changed.Payload = json.RawMessage(`{"a":1,"b":3}`)
	if _, err = s.ClaimApproval(ctx, a.TaskID, a.ID, approved.Revision, changed); !errors.Is(err, ErrApprovalUnavailable) {
		t.Fatal("changed payload", err)
	}
	claim, err := s.ClaimApproval(ctx, a.TaskID, a.ID, approved.Revision, a.Action)
	if err != nil {
		t.Fatal(err)
	}
	done, err := s.FinishApproval(ctx, a.TaskID, a.ID, claim.Revision, "succeeded")
	if err != nil || done.Status != "succeeded" {
		t.Fatal(done, err)
	}
	if _, err = s.ClaimApproval(ctx, a.TaskID, a.ID, done.Revision, a.Action); !errors.Is(err, ErrApprovalUnavailable) {
		t.Fatal("replay", err)
	}
	if _, err = s.FinishApproval(ctx, a.TaskID, a.ID, claim.Revision, "failed"); !errors.Is(err, ErrApprovalUnavailable) {
		t.Fatal("duplicate completion", err)
	}
}
func TestApprovalDenyAndExpiry(t *testing.T) {
	s, _, a := approvalFixture(t)
	ctx := context.Background()
	denied, err := s.DecideApproval(ctx, a.TaskID, a.ID, a.Revision, a.Digest, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimApproval(ctx, a.TaskID, a.ID, denied.Revision, a.Action); !errors.Is(err, ErrApprovalUnavailable) {
		t.Fatal("denied claim", err)
	}
	for _, approve := range []bool{false, true} {
		fresh, err := s.RequestApproval(ctx, a.TaskID, a.RunID, a.Action, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if approve {
			fresh, err = s.DecideApproval(ctx, a.TaskID, fresh.ID, fresh.Revision, fresh.Digest, true)
			if err != nil {
				t.Fatal(err)
			}
		}
		// Deterministically expire without timing-sensitive sleeps.
		if _, err = s.db.Exec(`UPDATE approvals SET expires_at=? WHERE id=?`, time.Now().Add(-time.Second).UnixNano(), fresh.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = s.DecideApproval(ctx, a.TaskID, fresh.ID, fresh.Revision, fresh.Digest, true); !errors.Is(err, ErrApprovalUnavailable) {
			t.Fatal("expired decision", err)
		}
		if _, err = s.ClaimApproval(ctx, a.TaskID, fresh.ID, fresh.Revision, a.Action); !errors.Is(err, ErrApprovalUnavailable) {
			t.Fatal("expired claim", err)
		}
	}
}
func TestApprovalCrossConnectionSingleUseAndRecovery(t *testing.T) {
	s, path, a := approvalFixture(t)
	ctx := context.Background()
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	approved, err := s.DecideApproval(ctx, a.TaskID, a.ID, a.Revision, a.Digest, true)
	if err != nil {
		t.Fatal(err)
	}
	var won atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			db := s
			if i%2 == 0 {
				db = other
			}
			_, err := db.ClaimApproval(ctx, a.TaskID, a.ID, approved.Revision, a.Action)
			if err == nil {
				won.Add(1)
			} else if !errors.Is(err, ErrApprovalUnavailable) {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatal("claims won", won.Load())
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := reopened.GetApproval(ctx, a.TaskID, a.ID)
	if err != nil || persisted.Status != "executing" {
		t.Fatal(persisted, err)
	}
	if err = reopened.InterruptApprovals(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := reopened.GetApproval(ctx, a.TaskID, a.ID)
	if err != nil || recovered.Status != "unknown" {
		t.Fatal(recovered, err)
	}
	if _, err = reopened.ClaimApproval(ctx, a.TaskID, a.ID, recovered.Revision, a.Action); !errors.Is(err, ErrApprovalUnavailable) {
		t.Fatal("unknown replay", err)
	}
	all, err := reopened.ListApprovals(ctx, a.TaskID)
	if err != nil || len(all) != 1 {
		t.Fatal(all, err)
	}
}
func TestApprovalTaskRunIsolation(t *testing.T) {
	s, _, a := approvalFixture(t)
	ctx := context.Background()
	task, err := s.Create(ctx, "other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RequestApproval(ctx, task.ID, a.RunID, a.Action, time.Now().Add(time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-task request", err)
	}
	if _, err = s.GetApproval(ctx, task.ID, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-task read", err)
	}
	if _, err = s.RequestApproval(ctx, a.TaskID, a.RunID, a.Action, time.Now().Add(-time.Second)); err == nil {
		t.Fatal("past expiry accepted")
	}
}

func TestApprovalLateClaimCannotDispatch(t *testing.T) {
	// Simulate an atomic claim whose database write waited past the expiry.
	_, err := approvalFreshTransition(Approval{Status: "executing", ExpiresAt: time.Now().Add(-time.Millisecond)}, nil)
	if !errors.Is(err, ErrApprovalUnavailable) {
		t.Fatal("late claim accepted", err)
	}
}
