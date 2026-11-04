package tasks

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gopi/internal/agent"
	"gopi/internal/session"
)

type modelFunc func(context.Context, []agent.Message, []agent.ToolDefinition) (agent.Message, error)

func (f modelFunc) Complete(ctx context.Context, m []agent.Message, t []agent.ToolDefinition) (agent.Message, error) {
	return f(ctx, m, t)
}
func fixture(t *testing.T, f func(string) (*agent.Agent, error)) (*Engine, *session.Store) {
	t.Helper()
	s, err := session.Open(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(context.Background(), s, Config{Factory: f})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close(); s.Close() })
	return e, s
}
func wait(t *testing.T, e *Engine, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := e.Wait(ctx, id); err != nil {
		t.Fatal(err)
	}
}
func TestConcurrentIsolationAndSingleTurn(t *testing.T) {
	entered := make(chan string, 2)
	release := make(chan struct{})
	defer close(release)
	var mu sync.Mutex
	seen := map[string][]agent.Message{}
	e, _ := fixture(t, func(id string) (*agent.Agent, error) {
		return &agent.Agent{Model: modelFunc(func(ctx context.Context, m []agent.Message, _ []agent.ToolDefinition) (agent.Message, error) {
			mu.Lock()
			seen[id] = m
			mu.Unlock()
			entered <- id
			select {
			case <-ctx.Done():
				return agent.Message{}, ctx.Err()
			case <-release:
				return agent.Message{Content: "ok"}, nil
			}
		})}, nil
	})
	ctx := context.Background()
	a, _ := e.Create(ctx, "A")
	b, _ := e.Create(ctx, "B")
	if _, err := e.Start(ctx, a.ID, "private A"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Start(ctx, a.ID, "second"); !errors.Is(err, ErrBusy) {
		t.Fatalf("want busy: %v", err)
	}
	if _, err := e.Start(ctx, b.ID, "private B"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("tasks not concurrent")
		}
	}
	mu.Lock()
	for id, ms := range seen {
		if len(ms) != 1 {
			t.Fatal(ms)
		}
		if id == a.ID && ms[0].Content != "private A" {
			t.Fatal(ms)
		}
		if id == b.ID && ms[0].Content != "private B" {
			t.Fatal(ms)
		}
	}
	mu.Unlock()
	if err := e.Cancel(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	wait(t, e, a.ID)
	got, _ := e.Get(ctx, a.ID)
	if got.Status != "cancelled" {
		t.Fatal(got.Status)
	}
	e.Cancel(ctx, b.ID)
	wait(t, e, b.ID)
}
func TestPersistedTurnsAndCards(t *testing.T) {
	e, s := fixture(t, func(string) (*agent.Agent, error) {
		return &agent.Agent{Model: modelFunc(func(_ context.Context, m []agent.Message, _ []agent.ToolDefinition) (agent.Message, error) {
			return agent.Message{Content: "reply to " + m[len(m)-1].Content}, nil
		})}, nil
	})
	ctx := context.Background()
	task, _ := e.Create(ctx, "Durable")
	run, err := e.Start(ctx, task.ID, "hello")
	if err != nil {
		t.Fatal(err)
	}
	wait(t, e, task.ID)
	task, err = e.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "completed" || len(task.Messages) != 2 {
		t.Fatalf("%+v", task)
	}
	events, _ := e.Events(ctx, task.ID, 0)
	if len(events) != 3 {
		t.Fatalf("events=%d", len(events))
	}
	if err := e.AddCard(ctx, task.ID, run.ID, Card{ID: "approval", Title: "Review", Summary: "Decision only"}); err != nil {
		t.Fatal(err)
	}
	request := ActionRequest{CardID: "approval", Revision: 1, Action: "approve"}
	if err := e.Act(ctx, task.ID, request); err != nil {
		t.Fatal(err)
	}
	if err := e.Act(ctx, task.ID, request); !errors.Is(err, ErrStaleCard) {
		t.Fatalf("replay: %v", err)
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
	if len(restored.Cards) != 1 || restored.Cards[0].State != "approved" {
		t.Fatalf("%+v", restored)
	}
	_, err = reopened.Start(ctx, task.ID, "again")
	if err != nil {
		t.Fatal(err)
	}
	wait(t, reopened, task.ID)
	restored, _ = reopened.Get(ctx, task.ID)
	if len(restored.Messages) != 4 || len(restored.Runs) != 2 {
		t.Fatal(restored)
	}
}
func TestRestartDoesNotReplayUnknownTools(t *testing.T) {
	var received []agent.Message
	e, s := fixture(t, func(string) (*agent.Agent, error) {
		return &agent.Agent{Model: modelFunc(func(_ context.Context, m []agent.Message, _ []agent.ToolDefinition) (agent.Message, error) {
			received = m
			return agent.Message{Content: "checked"}, nil
		})}, nil
	})
	ctx := context.Background()
	task, _ := e.Create(ctx, "Interrupted")
	if err := s.Save(ctx, task.ID, []agent.Message{{Role: "user", Content: "do work"}, {Role: "assistant", ToolCalls: []agent.ToolCall{{ID: "call1", Name: "external", Arguments: []byte(`{}`)}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRun(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	e.Close()
	restarted, err := New(ctx, s, e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	got, _ := restarted.Get(ctx, task.ID)
	if got.Status != "interrupted" {
		t.Fatal(got.Status)
	}
	if _, err := restarted.Start(ctx, task.ID, "continue carefully"); err != nil {
		t.Fatal(err)
	}
	wait(t, restarted, task.ID)
	if len(received) != 4 || received[2].Role != "tool" || !strings.Contains(received[2].Content, "unknown") {
		t.Fatalf("unrepaired history: %+v", received)
	}
}
func TestRequestCancellationDoesNotCancelTurn(t *testing.T) {
	e, _ := fixture(t, func(string) (*agent.Agent, error) {
		return &agent.Agent{Model: modelFunc(func(ctx context.Context, _ []agent.Message, _ []agent.ToolDefinition) (agent.Message, error) {
			select {
			case <-ctx.Done():
				return agent.Message{}, ctx.Err()
			case <-time.After(20 * time.Millisecond):
				return agent.Message{Content: "done"}, nil
			}
		})}, nil
	})
	task, _ := e.Create(context.Background(), "Independent")
	ctx, cancel := context.WithCancel(context.Background())
	_, err := e.Start(ctx, task.ID, "hello")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	wait(t, e, task.ID)
	got, _ := e.Get(context.Background(), task.ID)
	if got.Status != "completed" {
		t.Fatal(got.Status)
	}
}

func TestExclusiveOwnerAndLimits(t *testing.T) {
	e, s := fixture(t, func(string) (*agent.Agent, error) {
		return &agent.Agent{Model: modelFunc(func(context.Context, []agent.Message, []agent.ToolDefinition) (agent.Message, error) {
			return agent.Message{Content: "ok"}, nil
		})}, nil
	})
	if other, err := New(context.Background(), s, e.cfg); err == nil {
		other.Close()
		t.Fatal("second engine obtained ownership")
	}
	e.cfg.MaxTasks = 1
	e.cfg.MaxPromptBytes = 4
	task, err := e.Create(context.Background(), "one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Create(context.Background(), "two"); err == nil {
		t.Fatal("missing task limit")
	}
	if _, err := e.Start(context.Background(), task.ID, "oversized"); err == nil {
		t.Fatal("missing prompt limit")
	}
	e.Close()
	other, err := New(context.Background(), s, e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	other.Close()
}

func TestPanicFailsOnlyItsTask(t *testing.T) {
	e, _ := fixture(t, func(string) (*agent.Agent, error) {
		return &agent.Agent{Model: modelFunc(func(context.Context, []agent.Message, []agent.ToolDefinition) (agent.Message, error) {
			panic("broken model")
		})}, nil
	})
	task, _ := e.Create(context.Background(), "Panic")
	if _, err := e.Start(context.Background(), task.ID, "hello"); err != nil {
		t.Fatal(err)
	}
	wait(t, e, task.ID)
	got, _ := e.Get(context.Background(), task.ID)
	if got.Status != "failed" {
		t.Fatal(got.Status)
	}
}
