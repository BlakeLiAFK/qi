package tasks

import "context"

func (e *Engine) MarkResultRead(ctx context.Context, taskID, runID string) error {
	return e.store.MarkResultRead(ctx, taskID, runID)
}
