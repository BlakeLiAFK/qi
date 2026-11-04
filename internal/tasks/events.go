package tasks

import "context"

// EventIDs exposes committed progress cursors only; it does not execute work.
func (e *Engine) EventIDs(ctx context.Context, id string, after int64, limit int) ([]int64, error) {
	return e.store.EventIDs(ctx, id, after, limit)
}
