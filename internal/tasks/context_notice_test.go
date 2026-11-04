package tasks

import (
	"context"
	"encoding/json"
	"testing"

	"gopi/internal/agent"
	"gopi/internal/contextbudget"
)

func TestContextTrimNoticePersistsWithoutLosingHistory(t *testing.T) {
	ctx := context.Background()
	e, _ := fixture(t, func(string) (*agent.Agent, error) {
		next := modelFunc(func(context.Context, []agent.Message, []agent.ToolDefinition) (agent.Message, error) {
			return agent.Message{Content: "answer"}, nil
		})
		return &agent.Agent{Model: &contextbudget.Model{Next: next, Config: contextbudget.Config{MaxInputBytes: 100}}}, nil
	})
	task, err := e.Create(ctx, "context notice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Start(ctx, task.ID, "first prompt"); err != nil {
		t.Fatal(err)
	}
	wait(t, e, task.ID)
	first, err := e.Get(ctx, task.ID)
	if err != nil || first.ContextNotice != nil {
		t.Fatalf("initial task=%#v err=%v", first, err)
	}
	run, err := e.Start(ctx, task.ID, "second prompt")
	if err != nil {
		t.Fatal(err)
	}
	wait(t, e, task.ID)
	got, err := e.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "completed" || len(got.Messages) != 4 {
		t.Fatalf("lost history or failed: %#v", got)
	}
	if got.ContextNotice == nil || got.ContextNotice.DroppedMessages != 2 || got.ContextNotice.RunID != run.ID || got.ContextNotice.InputBytes <= 0 {
		t.Fatalf("notice=%#v", got.ContextNotice)
	}
	events, err := e.Events(ctx, task.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, ev := range events {
		if ev.Type == "context_trim" {
			var event agent.Event
			if err := json.Unmarshal(ev.Payload, &event); err != nil {
				t.Fatal(err)
			}
			if event.DroppedMessages != 2 || event.Message != nil {
				t.Fatalf("event=%#v", event)
			}
			count++
		}
	}
	if count != 1 {
		t.Fatalf("trim count=%d", count)
	}
	again, err := e.Get(ctx, task.ID)
	if err != nil || again.ContextNotice == nil || *again.ContextNotice != *got.ContextNotice {
		t.Fatalf("notice changed on read: %#v %v", again.ContextNotice, err)
	}
}
