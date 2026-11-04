package tasks

import (
	"context"
	"errors"
	"gopi/internal/agent"
	"gopi/internal/session"
	"testing"
)

func TestArchiveCapacityAndAdmission(t *testing.T) {
	ctx := context.Background()
	e, _ := fixture(t, func(string) (*agent.Agent, error) {
		return &agent.Agent{Model: modelFunc(func(context.Context, []agent.Message, []agent.ToolDefinition) (agent.Message, error) {
			return agent.Message{Role: "assistant", Content: "done"}, nil
		})}, nil
	})
	e.cfg.MaxTasks = 1
	task, err := e.CreateIdempotent(ctx, "owner", "create", "retained")
	if err != nil {
		t.Fatal(err)
	}
	if err = e.SetArchived(ctx, task.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Create(ctx, "over cap"); err == nil {
		t.Fatal("archive freed retention capacity")
	}
	if _, err = e.CreateIdempotent(ctx, "owner", "new", "over cap"); err == nil {
		t.Fatal("idempotent creation bypassed capacity")
	}
	replay, err := e.CreateIdempotent(ctx, "owner", "create", "retained")
	if err != nil || replay.ID != task.ID {
		t.Fatal(replay, err)
	}
	got, err := e.Get(ctx, task.ID)
	if err != nil || !got.Archived {
		t.Fatal(got, err)
	}
	if _, err = e.Start(ctx, task.ID, "blocked"); !errors.Is(err, session.ErrArchived) {
		t.Fatal(err)
	}
	list, err := e.List(ctx)
	if err != nil || len(list) != 0 {
		t.Fatal(list, err)
	}
	if err = e.SetArchived(ctx, task.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Start(ctx, task.ID, "allowed"); err != nil {
		t.Fatal(err)
	}
	wait(t, e, task.ID)
	got, err = e.Get(ctx, task.ID)
	if err != nil || got.Archived || len(got.Messages) != 2 {
		t.Fatal(got, err)
	}
}
