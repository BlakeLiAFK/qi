package tasks

import (
	"context"
	"testing"

	"gopi/internal/agent"
)

func TestResultReadEngineProjectionAndACK(t *testing.T) {
	e, _ := fixture(t, func(string) (*agent.Agent, error) {
		return &agent.Agent{Model: modelFunc(func(context.Context, []agent.Message, []agent.ToolDefinition) (agent.Message, error) {
			return agent.Message{Role: "assistant", Content: "done"}, nil
		})}, nil
	})
	ctx := context.Background()
	task, err := e.Create(ctx, "result")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Start(ctx, task.ID, "hello"); err != nil {
		t.Fatal(err)
	}
	wait(t, e, task.ID)
	detail, err := e.Get(ctx, task.ID)
	if err != nil || !detail.UnreadResult || detail.UnreadRunID == "" || detail.TerminalRunID != detail.UnreadRunID {
		t.Fatal(detail, err)
	}
	list, err := e.List(ctx)
	if err != nil || len(list) != 1 || !list[0].UnreadResult || list[0].UnreadRunID != detail.TerminalRunID || len(list[0].Messages) != 0 {
		t.Fatal(list, err)
	}
	// Reading either representation is not an acknowledgment.
	again, err := e.Get(ctx, task.ID)
	if err != nil || !again.UnreadResult {
		t.Fatal(again, err)
	}
	if err = e.MarkResultRead(ctx, task.ID, detail.TerminalRunID); err != nil {
		t.Fatal(err)
	}
	read, err := e.Get(ctx, task.ID)
	if err != nil || read.UnreadResult || read.UnreadRunID != "" || read.TerminalRunID != detail.TerminalRunID {
		t.Fatal(read, err)
	}
}
