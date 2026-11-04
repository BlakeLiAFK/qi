package tasks

import (
	"context"
	"errors"
	"gopi/internal/agent"
	"gopi/internal/session"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestIdempotentCreationConcurrentOwnerScopeAndRestart(t *testing.T) {
	ctx := context.Background()
	e, s := fixture(t, func(string) (*agent.Agent, error) { t.Fatal("creation executed model"); return nil, nil })
	e.cfg.MaxTasks = 2
	const clients = 24
	out := make(chan Task, clients)
	errs := make(chan error, clients)
	var wg sync.WaitGroup
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task, err := e.CreateIdempotent(ctx, "owner", "key", "  Title  ")
			out <- task
			errs <- err
		}()
	}
	wg.Wait()
	close(out)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var original Task
	for task := range out {
		if original.ID == "" {
			original = task
		}
		if !reflect.DeepEqual(original, task) || task.Title != "Title" {
			t.Fatal(task)
		}
	}
	if _, err := e.CreateIdempotent(ctx, "owner", "key", "different"); !errors.Is(err, session.ErrIdempotencyConflict) {
		t.Fatal(err)
	}
	other, err := e.CreateIdempotent(ctx, "other-owner", "key", "Title")
	if err != nil || other.ID == original.ID {
		t.Fatal(other, err)
	}
	if _, err = e.CreateIdempotent(ctx, "owner", "new-key", "Title"); err == nil {
		t.Fatal("cap bypass")
	}
	e.Close()
	next, err := New(ctx, s, e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	retry, err := next.CreateIdempotent(ctx, "owner", "key", "Title")
	if err != nil || !reflect.DeepEqual(original, retry) {
		t.Fatal(retry, err)
	}
	list, err := next.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatal(list, err)
	}
	for _, key := range []string{"", "bad key", strings.Repeat("a", 129)} {
		if _, err = next.CreateIdempotent(ctx, "owner", key, "Title"); err == nil {
			t.Fatal("invalid key accepted", key)
		}
	}
}
