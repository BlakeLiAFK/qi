package tasks

import (
	"context"
	"errors"
	"gopi/internal/agent"
	"gopi/internal/session"
	"strings"
	"sync"
	"testing"
	"time"
)

func memoryFact(content string) session.MemoryFact {
	return session.MemoryFact{Content: content, SourceRef: "user:reviewed", ApprovalRef: "explicit:create", Kind: "verified", Confidence: 1, Confidentiality: "private"}
}
func TestMemorySelectionPerRunIsolationRetryAndRestart(t *testing.T) {
	ctx := context.Background()
	seen := make(chan []agent.Message, 10)
	e, s := fixture(t, func(string) (*agent.Agent, error) {
		return &agent.Agent{Model: modelFunc(func(_ context.Context, m []agent.Message, defs []agent.ToolDefinition) (agent.Message, error) {
			if len(defs) != 0 {
				t.Error("memory granted tools")
			}
			seen <- m
			return agent.Message{Content: "done"}, nil
		})}, nil
	})
	e.cfg.MemoryDestination = session.MemoryDestination{ID: "test-provider", Provider: "fixture", Model: "test", Endpoint: "https://example.test/model"}
	a, _ := e.Create(ctx, "a")
	b, _ := e.Create(ctx, "b")
	record, err := e.CreateMemory(ctx, session.MemoryScope{TaskID: a.ID}, memoryFact("SENTINEL private fact"))
	if err != nil {
		t.Fatal(err)
	}
	selection := []session.MemorySelection{{ID: record.ID, Revision: 1}}
	if _, err = e.SubmitWithMemory(ctx, b.ID, "owner", "other", "hello", nil, selection, true, "test-provider"); !errors.Is(err, session.ErrMemoryNotFound) {
		t.Fatal(err)
	}
	if _, err = e.SubmitWithMemory(ctx, a.ID, "owner", "no-consent", "hello", nil, selection, false, ""); err == nil {
		t.Fatal("missing disclosure accepted")
	}
	var wg sync.WaitGroup
	runs := make(chan Run, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run, err := e.SubmitWithMemory(ctx, a.ID, "owner", "same", "hello", nil, selection, true, "test-provider")
			if err != nil {
				t.Error(err)
			}
			runs <- run
		}()
	}
	wg.Wait()
	close(runs)
	var accepted Run
	for run := range runs {
		if accepted.ID != "" && accepted.ID != run.ID {
			t.Fatal("duplicate runs")
		}
		accepted = run
	}
	wait(t, e, a.ID)
	first := <-seen
	if !strings.Contains(first[0].Content, "SENTINEL") || !strings.Contains(first[0].Content, `"source_ref":"user:reviewed"`) || !strings.Contains(first[0].Content, `"revision":1`) {
		t.Fatal(first)
	}
	stored, _ := e.Get(ctx, a.ID)
	if strings.Contains(stored.Messages[0].Content, "SENTINEL") || len(stored.MemorySelections[accepted.ID]) != 1 {
		t.Fatal(stored)
	}
	if err = e.DeleteMemory(ctx, record.Scope, record.ID, record.Revision); err != nil {
		t.Fatal(err)
	}
	retry, err := e.SubmitWithMemory(ctx, a.ID, "owner", "same", "hello", nil, selection, true, "test-provider")
	if err != nil || retry.ID != accepted.ID {
		t.Fatal(retry, err)
	}
	if _, err = e.SubmitWithMemory(ctx, a.ID, "owner", "same", "hello", nil, nil, false, ""); !errors.Is(err, session.ErrIdempotencyConflict) {
		t.Fatal(err)
	}
	if _, err = e.Submit(ctx, a.ID, "owner", "next", "next", nil); err != nil {
		t.Fatal(err)
	}
	wait(t, e, a.ID)
	next := <-seen
	for _, m := range next {
		if strings.Contains(m.Content, "SENTINEL") {
			t.Fatal("hidden carryover", next)
		}
	}
	if _, err = e.Start(ctx, b.ID, "unrelated"); err != nil {
		t.Fatal(err)
	}
	wait(t, e, b.ID)
	for _, m := range <-seen {
		if strings.Contains(m.Content, "SENTINEL") {
			t.Fatal("cross task leak")
		}
	}
	e.Close()
	restarted, err := New(ctx, s, e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	retry, err = restarted.SubmitWithMemory(ctx, a.ID, "owner", "same", "hello", nil, selection, true, "test-provider")
	if err != nil || retry.ID != accepted.ID {
		t.Fatal(retry, err)
	}
	snapshot, err := s.TurnMemory(ctx, a.ID, accepted.ID)
	if err != nil || len(snapshot) != 1 || snapshot[0].Content != record.Content {
		t.Fatal(snapshot, err)
	}
	wrong, err := s.TurnMemory(ctx, b.ID, accepted.ID)
	if err != nil || len(wrong) != 0 {
		t.Fatal("snapshot crossed task", wrong, err)
	}
	select {
	case m := <-seen:
		t.Fatal("retry called provider", m)
	default:
	}
}
func TestMemorySelectionExpiryRevisionScopeAndLimits(t *testing.T) {
	ctx := context.Background()
	e, _ := fixture(t, func(string) (*agent.Agent, error) {
		return &agent.Agent{Model: modelFunc(func(context.Context, []agent.Message, []agent.ToolDefinition) (agent.Message, error) {
			return agent.Message{Content: "done"}, nil
		})}, nil
	})
	e.cfg.MemoryDestination = session.MemoryDestination{ID: "test-provider", Provider: "fixture", Model: "test", Endpoint: "https://example.test/model"}
	task, _ := e.Create(ctx, "one")
	global, err := e.CreateMemory(ctx, session.MemoryScope{Global: true}, memoryFact("global fact"))
	if err != nil {
		t.Fatal(err)
	}
	sel := []session.MemorySelection{{ID: global.ID, Revision: 1}}
	if _, err = e.SubmitWithMemory(ctx, task.ID, "o", "scope", "hello", nil, sel, true, "test-provider"); !errors.Is(err, session.ErrMemoryNotFound) {
		t.Fatal(err)
	}
	sel[0].Global = true
	sel[0].Revision = 2
	if _, err = e.SubmitWithMemory(ctx, task.ID, "o", "stale", "hello", nil, sel, true, "test-provider"); !errors.Is(err, session.ErrMemoryConflict) {
		t.Fatal(err)
	}
	sel[0].Revision = 1
	if _, err = e.SubmitWithMemory(ctx, task.ID, "o", "duplicate", "hello", nil, append(sel, sel[0]), true, "test-provider"); err == nil {
		t.Fatal("duplicate accepted")
	}
	fact := memoryFact("expired")
	past := time.Now().Add(-time.Second)
	fact.ExpiresAt = &past
	expired, _ := e.CreateMemory(ctx, session.MemoryScope{TaskID: task.ID}, fact)
	if _, err = e.SubmitWithMemory(ctx, task.ID, "o", "expiry", "hello", nil, []session.MemorySelection{{ID: expired.ID, Revision: 1}}, true, "test-provider"); !errors.Is(err, session.ErrMemoryNotFound) {
		t.Fatal(err)
	}
	e.cfg.MaxPromptBytes = 30
	if _, err = e.SubmitWithMemory(ctx, task.ID, "o", "budget", "hello", nil, sel, true, "test-provider"); err == nil {
		t.Fatal("overbudget accepted")
	}
	e.cfg.MaxPromptBytes = 64000
	if _, err = e.SubmitWithMemory(ctx, task.ID, "o", "scope", "hello", nil, sel, true, "test-provider"); err != nil {
		t.Fatal("failed acceptance consumed key", err)
	}
	wait(t, e, task.ID)
}
func TestMemoryProjectionRechecksExpiryAndDoesNotMutateHistory(t *testing.T) {
	called := false
	past := time.Now().Add(-time.Second)
	m := turnMemoryModel{base: modelFunc(func(context.Context, []agent.Message, []agent.ToolDefinition) (agent.Message, error) {
		called = true
		return agent.Message{}, nil
	}), records: []session.MemoryRecord{{MemoryFact: session.MemoryFact{ExpiresAt: &past}}}, userIndex: 0}
	h := []agent.Message{{Role: "user", Content: "original"}}
	if _, err := m.Complete(context.Background(), h, nil); err == nil || called {
		t.Fatal("expired data reached provider")
	}
	m.records[0].ExpiresAt = nil
	if _, err := m.Complete(context.Background(), h, nil); err != nil || !called || h[0].Content != "original" {
		t.Fatal(err, h)
	}
}

func TestQueuedMemorySnapshotSurvivesEditDeleteAndExpiresBeforeDispatch(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "edited-deleted", true: "expired"}[expired], func(t *testing.T) {
			ctx := context.Background()
			entered := make(chan struct{})
			release := make(chan struct{})
			seen := make(chan []agent.Message, 1)
			e, _ := fixture(t, func(id string) (*agent.Agent, error) {
				return &agent.Agent{Model: modelFunc(func(_ context.Context, m []agent.Message, _ []agent.ToolDefinition) (agent.Message, error) {
					if m[0].Content == "block" {
						close(entered)
						<-release
					} else {
						seen <- m
					}
					return agent.Message{Content: "done"}, nil
				})}, nil
			})
			e.cfg.MaxConcurrent = 1
			e.cfg.MemoryDestination = session.MemoryDestination{ID: "destination"}
			blocker, _ := e.Create(ctx, "blocker")
			task, _ := e.Create(ctx, "selected")
			if _, err := e.Start(ctx, blocker.ID, "block"); err != nil {
				t.Fatal(err)
			}
			<-entered
			fact := memoryFact("accepted snapshot")
			expiry := time.Now().Add(200 * time.Millisecond)
			if expired {
				fact.ExpiresAt = &expiry
			}
			rec, err := e.CreateMemory(ctx, session.MemoryScope{TaskID: task.ID}, fact)
			if err != nil {
				t.Fatal(err)
			}
			run, err := e.SubmitWithMemory(ctx, task.ID, "owner", "queued", "selected", nil, []session.MemorySelection{{ID: rec.ID, Revision: 1}}, true, "destination")
			if err != nil {
				t.Fatal(err)
			}
			taskView, _ := e.Get(ctx, task.ID)
			if taskView.Status != "queued" {
				t.Fatalf("not genuinely queued: %s", taskView.Status)
			}
			fact.Content = "replacement must not be sent"
			fact.ApprovalRef = "new approval"
			edited, err := e.EditMemory(ctx, rec.Scope, rec.ID, 1, fact)
			if err != nil {
				t.Fatal(err)
			}
			if err = e.DeleteMemory(ctx, rec.Scope, rec.ID, edited.Revision); err != nil {
				t.Fatal(err)
			}
			if expired {
				time.Sleep(time.Until(expiry) + 10*time.Millisecond)
			}
			close(release)
			wait(t, e, blocker.ID)
			wait(t, e, task.ID)
			if expired {
				select {
				case m := <-seen:
					t.Fatal("expired queued snapshot reached provider", m)
				default:
				}
				view, _ := e.Get(ctx, task.ID)
				if view.Runs[0].Status != "failed" {
					t.Fatal(view.Runs)
				}
			} else {
				m := <-seen
				if !strings.Contains(m[0].Content, "accepted snapshot") || strings.Contains(m[0].Content, "replacement must not be sent") {
					t.Fatal(m)
				}
			}
			snapshot, err := e.store.TurnMemory(ctx, task.ID, run.ID)
			if err != nil || len(snapshot) != 1 || snapshot[0].Revision != 1 {
				t.Fatal(snapshot, err)
			}
		})
	}
}
func TestMemoryDestinationChangesRejectNewAdmissionButPreserveRetry(t *testing.T) {
	e, _ := fixture(t, func(string) (*agent.Agent, error) {
		return &agent.Agent{Model: modelFunc(func(context.Context, []agent.Message, []agent.ToolDefinition) (agent.Message, error) {
			return agent.Message{Content: "done"}, nil
		})}, nil
	})
	ctx := context.Background()
	e.cfg.MemoryDestination = session.MemoryDestination{ID: "original"}
	task, _ := e.Create(ctx, "one")
	rec, _ := e.CreateMemory(ctx, session.MemoryScope{TaskID: task.ID}, memoryFact("fact"))
	sel := []session.MemorySelection{{ID: rec.ID, Revision: 1}}
	for _, dest := range []string{"", "wrong"} {
		if _, err := e.SubmitWithMemory(ctx, task.ID, "owner", "key", "hello", nil, sel, true, dest); err == nil {
			t.Fatal("unreviewed destination admitted")
		}
	}
	run, err := e.SubmitWithMemory(ctx, task.ID, "owner", "key", "hello", nil, sel, true, "original")
	if err != nil {
		t.Fatal(err)
	}
	wait(t, e, task.ID)
	e.Close()
	cfg := e.cfg
	cfg.MemoryDestination = session.MemoryDestination{ID: "changed"}
	next, err := New(ctx, e.store, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	retry, err := next.SubmitWithMemory(ctx, task.ID, "owner", "key", "hello", nil, sel, true, "original")
	if err != nil || retry.ID != run.ID {
		t.Fatal(retry, err)
	}
	if _, err = next.SubmitWithMemory(ctx, task.ID, "owner", "new-key", "hello", nil, sel, true, "original"); err == nil {
		t.Fatal("stale provider consent admitted")
	}
	d, err := e.store.TaskMemoryDestinations(ctx, task.ID)
	if err != nil || d[run.ID].ID != "original" {
		t.Fatal(d, err)
	}
}
