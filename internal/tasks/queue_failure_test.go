package tasks

import (
	"context"
	"database/sql"
	"gopi/internal/testutil"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopi/internal/agent"
	"gopi/internal/session"
)

func TestQueueCancellationAfterDispatcherFailure(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(testutil.PrivateDir(t), "queue.db")
	s, err := session.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	entered := make(chan string, 3)
	release := make(chan struct{})
	e, err := New(ctx, s, Config{MaxConcurrent: 1, MaxQueued: 2, Factory: func(id string) (*agent.Agent, error) {
		return &agent.Agent{Model: modelFunc(func(ctx context.Context, _ []agent.Message, _ []agent.ToolDefinition) (agent.Message, error) {
			entered <- id
			select {
			case <-release:
				return agent.Message{Content: "done"}, nil
			case <-ctx.Done():
				return agent.Message{}, ctx.Err()
			}
		})}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	a, _ := e.Create(ctx, "active")
	b, _ := e.Create(ctx, "queued to cancel")
	c, _ := e.Create(ctx, "queued untouched")
	for _, task := range []Task{a, b, c} {
		if _, err = e.Start(ctx, task.ID, task.Title); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("active model did not enter")
	}
	// Inject a real SQLite claim failure after admissions committed, before the
	// next dispatch. Cancellation writes must remain available.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TRIGGER reject_dispatch BEFORE UPDATE OF status ON runs WHEN NEW.status='running' BEGIN SELECT RAISE(ABORT,'injected claim failure'); END`); err != nil {
		t.Fatal(err)
	}
	close(release)
	waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err = e.Wait(waitCtx, b.ID); err == nil || !strings.Contains(err.Error(), "dispatcher stopped") {
		t.Fatalf("want dispatcher error: %v", err)
	}
	if _, err = e.Start(ctx, b.ID, "must not replay"); err == nil {
		t.Fatal("failed dispatcher accepted more work")
	}
	if err = e.Cancel(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	got, err := e.Get(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "cancelled" || len(got.Messages) != 1 || got.Messages[0].Content != b.Title {
		t.Fatalf("cancel did not reach durable queue: %+v", got)
	}
	other, err := e.Get(ctx, c.ID)
	if err != nil || other.Status != "queued" {
		t.Fatalf("cancel crossed task boundary: %+v %v", other, err)
	}
	if err = e.Cancel(ctx, b.ID); err != nil {
		t.Fatalf("repeat cancellation: %v", err)
	}
	select {
	case id := <-entered:
		t.Fatalf("work replayed after claim failure: %s", id)
	default:
	}
	e.Close()
	other, err = e.Get(ctx, c.ID)
	if err != nil || other.Status != "cancelled" {
		t.Fatalf("shutdown missed durable orphan queue: %+v %v", other, err)
	}

}
