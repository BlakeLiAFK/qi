package webui

import (
	"context"
	"gopi/internal/tasks"
)

// EngineBackend adapts the task engine without exposing run internals to writes.
// List returns only task metadata; conversation content is fetched per task.
type EngineBackend struct{ Engine *tasks.Engine }

func (b EngineBackend) List(ctx context.Context) ([]Task, error) {
	items, err := b.Engine.List(ctx)
	for i := range items {
		items[i].Messages = nil
		items[i].Cards = nil
		items[i].Runs = nil
	}
	return items, err
}
func (b EngineBackend) Create(ctx context.Context, title string) (Task, error) {
	return b.Engine.Create(ctx, title)
}
func (b EngineBackend) Get(ctx context.Context, id string) (Task, error) {
	return b.Engine.Get(ctx, id)
}
func (b EngineBackend) Start(ctx context.Context, id, prompt string) error {
	_, err := b.Engine.Start(ctx, id, prompt)
	return err
}
func (b EngineBackend) Cancel(ctx context.Context, id string) error { return b.Engine.Cancel(ctx, id) }
func (b EngineBackend) Act(ctx context.Context, id string, req ActionRequest) error {
	return b.Engine.Act(ctx, id, req)
}
