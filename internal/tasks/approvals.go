package tasks

import (
	"context"
	"gopi/internal/session"
	"time"
)

// Approval decisions are durable, exact-action records. They are not executable
// UI cards and are deliberately not exposed as model-callable tools. No current
// engine adapter dispatches external effects using these decisions.
type Approval = session.Approval
type ApprovalAction = session.ApprovalAction

type ApprovalDecision struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
	Digest   string `json:"digest"`
	Allow    bool   `json:"allow"`
}

func (e *Engine) RequestApproval(ctx context.Context, taskID, runID string, action ApprovalAction, expiry time.Time) (Approval, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return Approval{}, ErrClosed
	}
	return e.store.RequestApproval(ctx, taskID, runID, action, expiry)
}
func (e *Engine) Approvals(ctx context.Context, taskID string) ([]Approval, error) {
	return e.store.ListApprovals(ctx, taskID)
}

// DecideApproval records an explicit user decision. Callers must authenticate
// that user and show the complete action, expiry, revision, and digest before
// submission. This does not dispatch the operation or grant generic authority.
func (e *Engine) DecideApproval(ctx context.Context, taskID string, decision ApprovalDecision) (Approval, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return Approval{}, ErrClosed
	}
	return e.store.DecideApproval(ctx, taskID, decision.ID, decision.Revision, decision.Digest, decision.Allow)
}
