package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"gopi/internal/agent"
)

func TestQuestionContinueQueueFullPreservesAnswer(t *testing.T) {
	ctx := context.Background()
	e, _, task, run := questionFixture(t)
	if err := e.AddCard(ctx, task.ID, run.ID, choiceCard("q")); err != nil {
		t.Fatal(err)
	}
	if err := e.Act(ctx, task.ID, ActionRequest{TaskID: task.ID, CardID: "q", Revision: 1, Action: "answer", ChoiceID: "doc"}); err != nil {
		t.Fatal(err)
	}
	e.cfg.MaxConcurrent, e.cfg.MaxQueued = 1, 1
	e.cfg.Factory = func(string) (*agent.Agent, error) {
		return &agent.Agent{Model: modelFunc(func(ctx context.Context, _ []agent.Message, _ []agent.ToolDefinition) (agent.Message, error) {
			<-ctx.Done()
			return agent.Message{}, ctx.Err()
		})}, nil
	}
	a, _ := e.Create(ctx, "active")
	b, _ := e.Create(ctx, "queued")
	if _, err := e.Start(ctx, a.ID, "hold"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Start(ctx, b.ID, "wait"); err != nil {
		t.Fatal(err)
	}
	req := ActionRequest{TaskID: task.ID, CardID: "q", Revision: 2, Action: "continue"}
	if err := e.Act(ctx, task.ID, req); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("want full: %v", err)
	}
	got, err := e.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cards[0].State != "answered" || got.Cards[0].Revision != 2 || len(got.Runs) != 1 || len(got.Messages) != 2 {
		t.Fatalf("full queue consumed answer: %+v", got)
	}
	if err = e.Cancel(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if err = e.Act(ctx, task.ID, req); err != nil {
		t.Fatal(err)
	}
	got, err = e.Get(ctx, task.ID)
	if err != nil || got.Status != "queued" || got.Cards[0].State != "continued" {
		t.Fatal(got, err)
	}
}

func TestQuestionContinueRetainsAcceptedAnswerAfterExpiry(t *testing.T) {
	ctx := context.Background()
	e, s, task, run := questionFixture(t)
	// Restore a historically valid accepted answer whose response deadline has
	// passed. This avoids a sleep and models the same durable state after restart.
	c := choiceCard("old-answer")
	c.TaskID, c.RunID, c.State, c.Revision = task.ID, run.ID, "answered", 2
	c.ExpiresAt = time.Now().Add(-time.Hour)
	c.Answer = &QuestionAnswer{ChoiceID: "doc", AnsweredAt: c.ExpiresAt.Add(-time.Minute)}
	payload, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AppendEvent(ctx, task.ID, run.ID, "card", payload); err != nil {
		t.Fatal(err)
	}
	if err = e.Act(ctx, task.ID, ActionRequest{TaskID: task.ID, CardID: c.ID, Revision: 2, Action: "continue"}); err != nil {
		t.Fatalf("accepted answer expired: %v", err)
	}
	wait(t, e, task.ID)
	got, err := e.Get(ctx, task.ID)
	if err != nil || got.Status != "completed" || got.Cards[0].State != "continued" {
		t.Fatal(got, err)
	}
}
