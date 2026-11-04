package session

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

func TestRunLifecycleAndEvents(t *testing.T) {
	s, path := openTest(t)
	ctx := context.Background()
	a, err := s.Create(ctx, "A")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Create(ctx, "B")
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.CreateRun(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateRun(ctx, a.ID); !errors.Is(err, ErrRunActive) {
		t.Fatalf("duplicate run: %v", err)
	}
	other, err := s.CreateRun(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	event, err := s.AppendEvent(ctx, a.ID, run.ID, "message", json.RawMessage(`{"content":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.AppendEvent(ctx, a.ID, run.ID, "message", json.RawMessage(`{"content":"world"}`))
	if err != nil {
		t.Fatal(err)
	}
	if second.ID <= event.ID {
		t.Fatal("event IDs not ordered")
	}
	events, err := s.Events(ctx, a.ID, event.ID)
	if err != nil || len(events) != 1 || events[0].ID != second.ID {
		t.Fatalf("cursor: %v %v", events, err)
	}
	if _, err = s.AppendEvent(ctx, b.ID, run.ID, "message", json.RawMessage(`{}`)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross session event accepted: %v", err)
	}
	if _, err = s.AppendEvent(ctx, a.ID, run.ID, "message", json.RawMessage(`bad`)); err == nil {
		t.Fatal("invalid JSON event accepted")
	}
	if err = s.FinishRun(ctx, run.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishRun(ctx, run.ID, "failed", "late error"); !errors.Is(err, ErrRunFinished) {
		t.Fatalf("overwrote terminal state: %v", err)
	}
	if err = s.FinishRun(ctx, other.ID, "invalid", ""); err == nil {
		t.Fatal("invalid status")
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.InterruptRuns(ctx); err != nil {
		t.Fatal(err)
	}
	runs, err := s.Runs(ctx, a.ID)
	if err != nil || len(runs) != 1 || runs[0].Status != "completed" {
		t.Fatalf("completed changed: %v %v", runs, err)
	}
	runs, err = s.Runs(ctx, b.ID)
	if err != nil || len(runs) != 1 || runs[0].Status != "interrupted" || runs[0].FinishedAt.IsZero() {
		t.Fatalf("recovery: %v %v", runs, err)
	}
	events, err = s.Events(ctx, a.ID, 0)
	if err != nil || len(events) != 2 {
		t.Fatalf("event durability: %v %v", events, err)
	}
	if _, err = s.CreateRun(ctx, b.ID); err != nil {
		t.Fatalf("new run after interrupted: %v", err)
	}
}
func TestConcurrentRuns(t *testing.T) {
	s, path := openTest(t)
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	ctx := context.Background()
	item, err := s.Create(ctx, "race")
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, store := range []*Store{s, other} {
		wg.Add(1)
		go func(store *Store) { defer wg.Done(); <-start; _, err := store.CreateRun(ctx, item.ID); results <- err }(store)
	}
	close(start)
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for err := range results {
		if err == nil {
			wins++
		} else if errors.Is(err, ErrRunActive) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
	}
}
func TestMissingRuns(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	if _, err := s.CreateRun(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.FinishRun(ctx, "missing", "completed", ""); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.Runs(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.Events(ctx, "missing", 0); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
