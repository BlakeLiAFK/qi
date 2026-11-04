package webui

import (
	"context"
	"gopi/internal/skills"
	"gopi/internal/tasks"
)

// EngineBackend adapts the task engine without exposing run internals to writes.
// List returns only task metadata; conversation content is fetched per task.
type EngineBackend struct{ Engine *tasks.Engine }

func (b EngineBackend) List(ctx context.Context) ([]Task, error) {
	return b.Engine.List(ctx)
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

func (b EngineBackend) Approvals(ctx context.Context, id string) ([]tasks.Approval, error) {
	return b.Engine.Approvals(ctx, id)
}
func (b EngineBackend) DecideApproval(ctx context.Context, id string, decision tasks.ApprovalDecision) (tasks.Approval, error) {
	return b.Engine.DecideApproval(ctx, id, decision)
}

func (b EngineBackend) AvailableSkills() []skills.Metadata { return b.Engine.AvailableSkills() }
func (b EngineBackend) StartWithSkills(ctx context.Context, id, prompt string, names []string) error {
	_, err := b.Engine.StartWithSkills(ctx, id, prompt, names)
	return err
}

// The web server is explicitly single-user. Authentication tokens may rotate;
// the durable principal names the owner, not a transient bearer credential.
func (b EngineBackend) Submit(ctx context.Context, id, key, prompt string, names []string) (tasks.Run, error) {
	return b.Engine.Submit(ctx, id, "local-owner", key, prompt, names)
}

func (b EngineBackend) CreateIdempotent(ctx context.Context, key, title string) (Task, error) {
	return b.Engine.CreateIdempotent(ctx, "local-owner", key, title)
}
