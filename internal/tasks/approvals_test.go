package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"gopi/internal/agent"
	"gopi/internal/session"
	"testing"
	"time"
)

func TestApprovalWrappersDoNotExecuteAndRecoverUnknown(t *testing.T) {
	factory := func(string) (*agent.Agent, error) {
		t.Fatal("approval decision must not instantiate an executor")
		return nil, nil
	}
	e, s := fixture(t, factory)
	ctx := context.Background()
	task, err := e.Create(ctx, "approval")
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.CreateRun(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	a, err := e.RequestApproval(ctx, task.ID, run.ID, ApprovalAction{Kind: "test.noop", Target: "local", Payload: json.RawMessage(`{}`)}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	approved, err := e.DecideApproval(ctx, task.ID, ApprovalDecision{ID: a.ID, Revision: a.Revision, Digest: a.Digest, Allow: true})
	if err != nil || approved.Status != "approved" {
		t.Fatal(approved, err)
	}
	if _, err = e.DecideApproval(ctx, task.ID, ApprovalDecision{ID: a.ID, Revision: a.Revision, Digest: a.Digest, Allow: true}); !errors.Is(err, session.ErrApprovalUnavailable) {
		t.Fatal("stale decision", err)
	}
	list, err := e.Approvals(ctx, task.ID)
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	_, err = s.ClaimApproval(ctx, task.ID, a.ID, approved.Revision, a.Action)
	if err != nil {
		t.Fatal(err)
	}
	e.Close()
	if _, err = e.RequestApproval(ctx, task.ID, run.ID, a.Action, time.Now().Add(time.Hour)); !errors.Is(err, ErrClosed) {
		t.Fatal("closed request", err)
	}
	restarted, err := New(ctx, s, Config{Factory: factory})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	list, err = restarted.Approvals(ctx, task.ID)
	if err != nil || len(list) != 1 || list[0].Status != "unknown" {
		t.Fatal(list, err)
	}
}
