package tasks

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"gopi/internal/agent"
)

func TestQueueBoundFIFOIsolationAndCancellation(t *testing.T) {
	ctx := context.Background()
	entered := make(chan string, 8)
	release := make(chan struct{}, 8)
	e, s := fixture(t, func(id string) (*agent.Agent, error) {
		return &agent.Agent{Model: modelFunc(func(ctx context.Context, ms []agent.Message, _ []agent.ToolDefinition) (agent.Message, error) {
			if len(ms) != 1 || ms[0].Content != id {
				return agent.Message{}, errors.New("cross-task transcript")
			}
			entered <- id
			select {
			case <-release:
				return agent.Message{Content: "done"}, nil
			case <-ctx.Done():
				return agent.Message{}, ctx.Err()
			}
		})}, nil
	})
	e.cfg.MaxConcurrent, e.cfg.MaxQueued = 1, 2
	ids := []string{}
	for _, title := range []string{"A", "B", "C", "D"} {
		task, err := e.Create(ctx, title)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, task.ID)
	}
	start := func(id string) {
		t.Helper()
		if _, err := e.Start(ctx, id, id); err != nil {
			t.Fatal(err)
		}
	}
	next := func(want string) {
		t.Helper()
		select {
		case got := <-entered:
			if got != want {
				t.Fatalf("FIFO got %s want %s", got, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("model did not enter")
		}
	}
	start(ids[0])
	next(ids[0])
	start(ids[1])
	start(ids[2])
	before, err := s.LoadTaskSnapshot(ctx, ids[3])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Start(ctx, ids[3], ids[3]); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("want full: %v", err)
	}
	after, err := s.LoadTaskSnapshot(ctx, ids[3])
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("queue rejection changed task")
	}
	if _, err = e.Start(ctx, ids[1], "second"); !errors.Is(err, ErrBusy) {
		t.Fatalf("queued same-task admission: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	if err = e.Wait(waitCtx, ids[1]); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait returned before queued run: %v", err)
	}
	cancel()
	if err = e.Cancel(ctx, ids[2]); err != nil {
		t.Fatal(err)
	}
	wait(t, e, ids[2])
	start(ids[3])
	release <- struct{}{}
	next(ids[1])
	release <- struct{}{}
	next(ids[3])
	release <- struct{}{}
	for _, id := range ids {
		wait(t, e, id)
	}
	for i, id := range ids {
		got, err := e.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		want := "completed"
		if i == 2 {
			want = "cancelled"
		}
		if got.Status != want {
			t.Fatalf("task %d status %s", i, got.Status)
		}
	}
	select {
	case id := <-entered:
		t.Fatalf("cancelled task executed: %s", id)
	default:
	}
}

func TestQueueFactoryFailureIsDurableAndDoesNotBlockNext(t *testing.T) {
	ctx := context.Background()
	fail := true
	e, _ := fixture(t, func(string) (*agent.Agent, error) {
		if fail {
			return nil, errors.New("setup failed")
		}
		return &agent.Agent{Model: modelFunc(func(context.Context, []agent.Message, []agent.ToolDefinition) (agent.Message, error) {
			return agent.Message{Content: "ok"}, nil
		})}, nil
	})
	task, err := e.Create(ctx, "failure")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Start(ctx, task.ID, "accepted prompt"); err != nil {
		t.Fatal(err)
	}
	wait(t, e, task.ID)
	got, err := e.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "failed" || len(got.Messages) != 1 || len(got.Runs) != 1 {
		t.Fatalf("lost accepted failure: %+v", got)
	}
	fail = false
	if _, err = e.Start(ctx, task.ID, "retry after setup fixed"); err != nil {
		t.Fatal(err)
	}
	wait(t, e, task.ID)
	got, err = e.Get(ctx, task.ID)
	if err != nil || got.Status != "completed" {
		t.Fatal(got, err)
	}
}

func TestQueueCloseCancelsBacklogWithoutDispatch(t *testing.T) {
	ctx := context.Background()
	entered := make(chan string, 2)
	e, _ := fixture(t, func(id string) (*agent.Agent, error) {
		return &agent.Agent{Model: modelFunc(func(ctx context.Context, _ []agent.Message, _ []agent.ToolDefinition) (agent.Message, error) {
			entered <- id
			<-ctx.Done()
			return agent.Message{}, ctx.Err()
		})}, nil
	})
	e.cfg.MaxConcurrent = 1
	a, _ := e.Create(ctx, "active")
	b, _ := e.Create(ctx, "queued")
	if _, err := e.Start(ctx, a.ID, "A"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("not started")
	}
	if _, err := e.Start(ctx, b.ID, "B"); err != nil {
		t.Fatal(err)
	}
	e.Close()
	got, err := e.Get(ctx, b.ID)
	if err != nil || got.Status != "cancelled" {
		t.Fatal(got, err)
	}
	select {
	case id := <-entered:
		t.Fatalf("queued task ran during Close: %s", id)
	default:
	}
}

func TestQueueRestartInterruptsAcceptedUndispatchedWork(t *testing.T) {
	ctx := context.Background()
	calls := 0
	e, s := fixture(t, func(string) (*agent.Agent, error) { calls++; return nil, errors.New("must not replay") })
	task, err := e.Create(ctx, "crash after accept")
	if err != nil {
		t.Fatal(err)
	}
	// Persist the exact crash boundary between admission commit and in-memory
	// registration. No factory or external action has been dispatched.
	e.mu.Lock()
	accepted, err := s.AcceptQueued(ctx, task.ID, 0, []agent.Message{{Role: "user", Content: "durable request"}}, "", nil, 2)
	e.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// Simulate owner death, not graceful Close (which cancels backlog).
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()
	e.closeOnce.Do(e.release)
	restarted, err := New(ctx, s, e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	got, err := restarted.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || got.Status != "interrupted" || len(got.Messages) != 1 || len(got.Runs) != 1 || got.Runs[0].ID != accepted.ID {
		t.Fatalf("unsafe restart: calls=%d task=%+v", calls, got)
	}
}
