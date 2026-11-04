package tasks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"gopi/internal/agent"
	"gopi/internal/session"
	"os"
	"sync"
	"sync/atomic"
	"testing"
)

func TestSubmissionConcurrentRetryAndRestart(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int32
	e, s := fixture(t, func(string) (*agent.Agent, error) {
		return &agent.Agent{Model: modelFunc(func(context.Context, []agent.Message, []agent.ToolDefinition) (agent.Message, error) {
			calls.Add(1)
			return agent.Message{Content: "done"}, nil
		})}, nil
	})
	task, _ := e.Create(ctx, "retry")
	const clients = 24
	results := make(chan Run, clients)
	errs := make(chan error, clients)
	var wg sync.WaitGroup
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run, err := e.Submit(ctx, task.ID, "owner", "key", "same prompt", nil)
			results <- run
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	id := ""
	for run := range results {
		if id == "" {
			id = run.ID
		}
		if run.ID != id || run.Status != "queued" {
			t.Fatalf("different acceptance: %+v", run)
		}
	}
	wait(t, e, task.ID)
	if calls.Load() != 1 {
		t.Fatal("duplicate execution", calls.Load())
	}
	if _, err := e.Submit(ctx, task.ID, "owner", "key", "changed", nil); !errors.Is(err, session.ErrIdempotencyConflict) {
		t.Fatal(err)
	}
	e.Close()
	next, err := New(ctx, s, e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	retry, err := next.Submit(ctx, task.ID, "owner", "key", "same prompt", []string{})
	if err != nil || retry.ID != id {
		t.Fatal(retry, err)
	}
	if calls.Load() != 1 {
		t.Fatal("restart replayed")
	}
	got, _ := next.Get(ctx, task.ID)
	if len(got.Runs) != 1 {
		t.Fatal(got)
	}
	// Principal and task are both part of the key namespace.
	if _, err = next.Submit(ctx, task.ID, "other-owner", "key", "changed", nil); err != nil {
		t.Fatal(err)
	}
	wait(t, next, task.ID)
	other, _ := next.Create(ctx, "other")
	if _, err = next.Submit(ctx, other.ID, "owner", "key", "same prompt", nil); err != nil {
		t.Fatal(err)
	}
	wait(t, next, other.ID)
}
func TestSubmissionRetryUsesOriginalSkillSnapshot(t *testing.T) {
	e, _, path := skillFixture(t, 0)
	ctx := context.Background()
	task, _ := e.Create(ctx, "skill retry")
	run, err := e.Submit(ctx, task.ID, "owner", "skill-key", "request", []string{"review"})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, e, task.ID)
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	retry, err := e.Submit(ctx, task.ID, "owner", "skill-key", "request", []string{"review"})
	if err != nil || run.ID != retry.ID {
		t.Fatal(retry, err)
	}
	if _, err = e.Submit(ctx, task.ID, "owner", "skill-key", "request", nil); !errors.Is(err, session.ErrIdempotencyConflict) {
		t.Fatal(err)
	}
}

func TestSubmissionCrashAfterAtomicCommitNeverDispatchesOnRetry(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int32
	e, s := fixture(t, func(string) (*agent.Agent, error) { calls.Add(1); return nil, errors.New("must not execute") })
	task, _ := e.Create(ctx, "crash boundary")
	// Compute the public normalized request identity by exactly the same format.
	payload := []byte(`{"Version":1,"Prompt":"accepted","Skills":[]}`)
	digest := sha256.Sum256(payload)
	identity := session.AdmissionIdentity{Principal: "owner", Key: "crash", Digest: hex.EncodeToString(digest[:])}
	accepted, err := s.AcceptQueued(ctx, task.ID, 0, []agent.Message{{Role: "user", Content: "accepted"}}, "", nil, 2, identity)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate process death after commit, before in-memory registration.
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()
	e.closeOnce.Do(e.release)
	next, err := New(ctx, s, e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	retry, err := next.Submit(ctx, task.ID, "owner", "crash", "accepted", nil)
	if err != nil || retry.ID != accepted.ID {
		t.Fatal(retry, err)
	}
	got, err := next.Get(ctx, task.ID)
	if err != nil || got.Status != "interrupted" || len(got.Runs) != 1 || calls.Load() != 0 {
		t.Fatal(got, err, calls.Load())
	}
}
