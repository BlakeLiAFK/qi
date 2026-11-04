package tasks

import "context"

// SetArchived shares admission's mutex; the store also guards against active
// runs atomically so callers using another connection cannot bypass it.
func (e *Engine) SetArchived(ctx context.Context, id string, archived bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return ErrClosed
	}
	if _, ok := e.active[id]; ok {
		return ErrBusy
	}
	return e.store.SetArchived(ctx, id, archived)
}
