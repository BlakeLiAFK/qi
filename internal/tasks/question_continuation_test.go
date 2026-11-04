package tasks

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"gopi/internal/agent"
)

func TestQuestionContinueSingleUseAndRestart(t *testing.T) {
	ctx := context.Background()
	e, s, task, run := questionFixture(t)
	if err := e.AddCard(ctx, task.ID, run.ID, choiceCard("q")); err != nil {
		t.Fatal(err)
	}
	answer := ActionRequest{TaskID: task.ID, CardID: "q", Revision: 1, Action: "answer", ChoiceID: "doc"}
	if err := e.Act(ctx, task.ID, answer); err != nil {
		t.Fatal(err)
	}
	e.Close()
	e, err := New(ctx, s, e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	req := ActionRequest{TaskID: task.ID, CardID: "q", Revision: 2, Action: "continue"}
	bad := req
	bad.TaskID = "other"
	if err = e.Act(ctx, task.ID, bad); !errors.Is(err, ErrStaleCard) {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- e.Act(ctx, task.ID, req) }()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrStaleCard) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatal(successes)
	}
	wait(t, e, task.ID)
	got, err := e.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Runs) != 2 || got.Cards[0].State != "continued" || got.Cards[0].Revision != 3 {
		t.Fatalf("%+v", got)
	}
	if !strings.Contains(got.Messages[2].Content, `"choice_id":"doc"`) {
		t.Fatal(got.Messages)
	}
	e.Close()
	reopened, err := New(ctx, s, e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err = reopened.Act(ctx, task.ID, req); !errors.Is(err, ErrStaleCard) {
		t.Fatal(err)
	}
}

func TestQuestionContinueCancellationAndNoReplay(t *testing.T) {
	ctx := context.Background()
	e, s, task, run := questionFixture(t)
	if err := e.AddCard(ctx, task.ID, run.ID, choiceCard("q")); err != nil {
		t.Fatal(err)
	}
	if err := e.Act(ctx, task.ID, ActionRequest{TaskID: task.ID, CardID: "q", Revision: 1, Action: "answer", ChoiceID: "doc"}); err != nil {
		t.Fatal(err)
	}
	e.cfg.Factory = func(string) (*agent.Agent, error) {
		return &agent.Agent{Model: modelFunc(func(ctx context.Context, _ []agent.Message, _ []agent.ToolDefinition) (agent.Message, error) {
			<-ctx.Done()
			return agent.Message{}, ctx.Err()
		})}, nil
	}
	req := ActionRequest{TaskID: task.ID, CardID: "q", Revision: 2, Action: "continue"}
	if err := e.Act(ctx, task.ID, req); err != nil {
		t.Fatal(err)
	}
	if err := e.Cancel(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	wait(t, e, task.ID)
	e.Close()
	reopened, err := New(ctx, s, e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.Get(ctx, task.ID)
	if err != nil || len(got.Runs) != 2 || got.Status != "cancelled" {
		t.Fatal(got, err)
	}
	if err = reopened.Act(ctx, task.ID, req); !errors.Is(err, ErrStaleCard) {
		t.Fatal(err)
	}
}

func TestQuestionContinueRejectsHistoricalCard(t *testing.T) {
	ctx := context.Background()
	e, _, task, run := questionFixture(t)
	if err := e.AddCard(ctx, task.ID, run.ID, choiceCard("q")); err != nil {
		t.Fatal(err)
	}
	if err := e.Act(ctx, task.ID, ActionRequest{TaskID: task.ID, CardID: "q", Revision: 1, Action: "answer", ChoiceID: "doc"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Start(ctx, task.ID, "new work"); err != nil {
		t.Fatal(err)
	}
	wait(t, e, task.ID)
	if err := e.Act(ctx, task.ID, ActionRequest{TaskID: task.ID, CardID: "q", Revision: 2, Action: "continue"}); err == nil {
		t.Fatal("old question resumed")
	}
}
