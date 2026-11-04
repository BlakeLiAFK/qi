package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"gopi/internal/agent"
	"gopi/internal/session"
)

func questionFixture(t *testing.T) (*Engine, *session.Store, Task, Run) {
	e, s := fixture(t, func(string) (*agent.Agent, error) {
		return &agent.Agent{Model: modelFunc(func(context.Context, []agent.Message, []agent.ToolDefinition) (agent.Message, error) {
			return agent.Message{Content: "Done"}, nil
		})}, nil
	})
	task, err := e.Create(context.Background(), "Questions")
	if err != nil {
		t.Fatal(err)
	}
	run, err := e.Start(context.Background(), task.ID, "start")
	if err != nil {
		t.Fatal(err)
	}
	wait(t, e, task.ID)
	return e, s, task, run
}
func choiceCard(id string) Card {
	return Card{ID: id, Kind: "question", Title: "Choose an output", InputType: "choice", Choices: []Action{{ID: "doc", Label: "Document"}, {ID: "slides", Label: "Slides"}}}
}

func TestQuestionSingleUseIsolationAndRestart(t *testing.T) {
	ctx := context.Background()
	e, s, task, run := questionFixture(t)
	c := choiceCard("format")
	if err := e.AddCard(ctx, task.ID, run.ID, c); err != nil {
		t.Fatal(err)
	}
	if err := e.AddCard(ctx, task.ID, run.ID, c); !errors.Is(err, ErrStaleCard) {
		t.Fatal("duplicate", err)
	}
	other, _ := e.Create(ctx, "Other")
	otherRun, _ := e.Start(ctx, other.ID, "start")
	wait(t, e, other.ID)
	if err := e.AddCard(ctx, other.ID, otherRun.ID, c); err != nil {
		t.Fatal(err)
	}
	req := ActionRequest{TaskID: task.ID, CardID: c.ID, Revision: 1, Action: "answer", ChoiceID: "doc"}
	if err := e.Act(ctx, other.ID, req); err == nil {
		t.Fatal("cross-task answer accepted")
	}
	bad := req
	bad.ChoiceID = "unknown"
	if err := e.Act(ctx, task.ID, bad); err == nil {
		t.Fatal("unknown choice")
	}
	bad = req
	bad.Action = "approve"
	if err := e.Act(ctx, task.ID, bad); err == nil {
		t.Fatal("question authorized action")
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
		t.Fatal("successes", successes)
	}
	unchanged, _ := e.Get(ctx, other.ID)
	if unchanged.Cards[0].State != "pending" {
		t.Fatal(unchanged)
	}
	e.Close()
	reopened, err := New(ctx, s, e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, err := reopened.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := restored.Cards[0]
	if got.Revision != 2 || got.State != "answered" || got.Answer == nil || got.Answer.ChoiceID != "doc" || got.RunID != run.ID {
		t.Fatalf("%+v", got)
	}
	if len(restored.Runs) != 1 || len(restored.Messages) != 2 {
		t.Fatal("answer resumed or changed transcript", restored)
	}
	if err := reopened.Act(ctx, task.ID, req); !errors.Is(err, ErrStaleCard) {
		t.Fatal("replay after restart", err)
	}
	output, err := (questionAnswersTool{engine: reopened, taskID: task.ID}).Execute(ctx, json.RawMessage(`{}`))
	if err != nil || !strings.Contains(output, `"choice_id":"doc"`) {
		t.Fatal(output, err)
	}
	output, err = (questionAnswersTool{engine: reopened, taskID: other.ID}).Execute(ctx, json.RawMessage(`{}`))
	if err != nil || strings.Contains(output, `"answer":`) {
		t.Fatal("answer leaked", output, err)
	}
}

func TestQuestionTextValidationExpiryAndToolScope(t *testing.T) {
	ctx := context.Background()
	e, store, task, run := questionFixture(t)
	q := questionTool{engine: e, taskID: task.ID, runID: run.ID}
	for _, raw := range []string{`null`, `{"id":"q","title":"Question","input_type":"text","task_id":"other"}`, `{"id":"q","title":"Question","input_type":"choice","choices":[{"id":"a","label":"A"},{"id":"a","label":"B"}]}`} {
		if _, err := q.Execute(ctx, json.RawMessage(raw)); err == nil {
			t.Fatal("invalid accepted", raw)
		}
	}
	if _, err := q.Execute(ctx, json.RawMessage(`{"id":"q","title":"Question","input_type":"text"}`)); err != nil {
		t.Fatal(err)
	}
	req := ActionRequest{TaskID: task.ID, CardID: "q", Revision: 1, Action: "answer"}
	for _, value := range []string{"", "   ", strings.Repeat("x", 8001)} {
		req.Text = value
		if err := e.Act(ctx, task.ID, req); err == nil {
			t.Fatal("invalid text")
		}
	}
	req.Text = "Keep <script>literal</script>"
	if err := e.Act(ctx, task.ID, req); err != nil {
		t.Fatal(err)
	}
	exp := choiceCard("expired")
	exp.ExpiresAt = time.Now().Add(time.Hour)
	if err := e.AddCard(ctx, task.ID, run.ID, exp); err != nil {
		t.Fatal(err)
	}
	// Expire the persisted card deterministically, rather than racing a short
	// wall-clock deadline against SQLite writes on a busy test machine.
	snapshot, err := e.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var expired Card
	for _, card := range snapshot.Cards {
		if card.ID == exp.ID {
			expired = card
		}
	}
	if expired.ID == "" {
		t.Fatal("created card missing")
	}
	expired.ExpiresAt = time.Now().Add(-time.Hour)
	payload, err := json.Marshal(expired)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(ctx, task.ID, run.ID, "card", payload); err != nil {
		t.Fatal(err)
	}
	if err := e.Act(ctx, task.ID, ActionRequest{TaskID: task.ID, CardID: "expired", Revision: 1, Action: "answer", ChoiceID: "doc"}); !errors.Is(err, ErrStaleCard) {
		t.Fatal("expired accepted", err)
	}
	past := choiceCard("past")
	past.ExpiresAt = time.Now().Add(-time.Second)
	if err := e.AddCard(ctx, task.ID, run.ID, past); err == nil {
		t.Fatal("past expiry")
	}
}

func TestQuestionToolsOptInAndPersistedModelFlow(t *testing.T) {
	ctx := context.Background()
	e, _ := fixture(t, func(string) (*agent.Agent, error) {
		return &agent.Agent{Model: modelFunc(func(_ context.Context, m []agent.Message, defs []agent.ToolDefinition) (agent.Message, error) {
			if len(defs) != 2 || defs[0].Name != "ask_user_question" || defs[1].Name != "get_question_answers" {
				t.Errorf("question tools unavailable: %+v", defs)
			}
			last := m[len(m)-1]
			if last.Role == "tool" {
				return agent.Message{Content: "Saved. Send a message after answering to continue."}, nil
			}
			if last.Content == "continue" {
				return agent.Message{ToolCalls: []agent.ToolCall{{ID: "read", Name: "get_question_answers", Arguments: json.RawMessage(`{}`)}}}, nil
			}
			return agent.Message{ToolCalls: []agent.ToolCall{{ID: "ask", Name: "ask_user_question", Arguments: json.RawMessage(`{"id":"detail","title":"What detail?","input_type":"text"}`)}}}, nil
		})}, nil
	})
	e.cfg.Questions = true
	task, _ := e.Create(ctx, "Model question")
	if _, err := e.Start(ctx, task.ID, "ask"); err != nil {
		t.Fatal(err)
	}
	wait(t, e, task.ID)
	got, err := e.Get(ctx, task.ID)
	if err != nil || len(got.Cards) != 1 || got.Status != "completed" {
		t.Fatal(got, err)
	}
	if err := e.Act(ctx, task.ID, ActionRequest{TaskID: task.ID, CardID: "detail", Revision: 1, Action: "answer", Text: "durable detail"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Start(ctx, task.ID, "continue"); err != nil {
		t.Fatal(err)
	}
	wait(t, e, task.ID)
	got, err = e.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range got.Messages {
		if m.ToolCallID == "read" && strings.Contains(m.Content, "durable detail") {
			found = true
		}
	}
	if !found {
		t.Fatal("model did not receive persisted answer", got.Messages)
	}
}

func TestQuestionPendingSurvivesRestartWithoutResuming(t *testing.T) {
	ctx := context.Background()
	e, s := fixture(t, func(string) (*agent.Agent, error) {
		return &agent.Agent{Model: modelFunc(func(ctx context.Context, _ []agent.Message, _ []agent.ToolDefinition) (agent.Message, error) {
			<-ctx.Done()
			return agent.Message{}, ctx.Err()
		})}, nil
	})
	task, _ := e.Create(ctx, "Pending cancellation")
	run, err := e.Start(ctx, task.ID, "wait")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.AddCard(ctx, task.ID, run.ID, choiceCard("pending")); err != nil {
		t.Fatal(err)
	}
	// Even a cancelled run's unanswered card remains historical user input.
	// Answering after restart records that input but cannot resume the old run.
	if err := e.Cancel(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	wait(t, e, task.ID)
	e.Close()
	next, err := New(ctx, s, e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if err := next.Act(ctx, task.ID, ActionRequest{TaskID: task.ID, CardID: "pending", Revision: 1, Action: "answer", ChoiceID: "doc"}); err != nil {
		t.Fatal(err)
	}
	got, err := next.Get(ctx, task.ID)
	if err != nil || got.Cards[0].State != "answered" || len(got.Runs) != 1 || got.Status != "cancelled" {
		t.Fatal(got, err)
	}
}

func TestQuestionToolsDisabledByDefault(t *testing.T) {
	e, _ := fixture(t, func(string) (*agent.Agent, error) {
		return &agent.Agent{Model: modelFunc(func(_ context.Context, _ []agent.Message, defs []agent.ToolDefinition) (agent.Message, error) {
			if len(defs) != 0 {
				t.Errorf("unexpected tools without explicit opt-in: %+v", defs)
			}
			return agent.Message{Content: "No tools"}, nil
		})}, nil
	})
	task, _ := e.Create(context.Background(), "Plain")
	if _, err := e.Start(context.Background(), task.ID, "hello"); err != nil {
		t.Fatal(err)
	}
	wait(t, e, task.ID)
}
