package tasks

import (
	"context"
	"gopi/internal/session"
)

// Memory management is a trusted control-plane operation only. These methods
// are deliberately absent from the model's tool registry and context builder.
func (e *Engine) ListMemory(ctx context.Context, scope session.MemoryScope) ([]session.MemoryRecord, error) {
	return e.store.ListMemory(ctx, scope)
}
func (e *Engine) CreateMemory(ctx context.Context, scope session.MemoryScope, fact session.MemoryFact) (session.MemoryRecord, error) {
	return e.store.CreateMemory(ctx, scope, fact)
}
func (e *Engine) EditMemory(ctx context.Context, scope session.MemoryScope, id string, revision int64, fact session.MemoryFact) (session.MemoryRecord, error) {
	return e.store.EditMemory(ctx, scope, id, revision, fact)
}
func (e *Engine) DeleteMemory(ctx context.Context, scope session.MemoryScope, id string, revision int64) error {
	return e.store.DeleteMemory(ctx, scope, id, revision)
}
func (e *Engine) MemoryHistory(ctx context.Context, scope session.MemoryScope, id string) ([]session.MemoryRecord, error) {
	return e.store.MemoryHistory(ctx, scope, id)
}

func (e *Engine) MemoryDestination() session.MemoryDestination { return e.cfg.MemoryDestination }
