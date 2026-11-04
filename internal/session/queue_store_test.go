package session

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"gopi/internal/agent"
)

func queueStore(t *testing.T) *Store {
	t.Helper()
	s, _ := openTest(t)
	release, err := s.AcquireEngineLock()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	if err = s.EnsureQueueSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}
func queuePrompt(text string) []agent.Message { return []agent.Message{{Role: "user", Content: text}} }

func TestQueueMigrationPreservesReferences(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	a, err := s.Create(ctx, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.CreateRun(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AppendEvent(ctx, a.ID, r.ID, "card", json.RawMessage(`{"id":"q"}`)); err != nil {
		t.Fatal(err)
	}
	approval, err := s.RequestApproval(ctx, a.ID, r.ID, ApprovalAction{Kind: "test.noop", Target: "local", Payload: json.RawMessage(`{}`)}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	release, err := s.AcquireEngineLock()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for i := 0; i < 2; i++ {
		if err = s.EnsureQueueSchema(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var fk int
	if err = s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatal("foreign keys not restored", fk, err)
	}
	rows, err := s.db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	bad := rows.Next()
	rows.Close()
	if bad {
		t.Fatal("migration broke foreign keys")
	}
	events, err := s.Events(ctx, a.ID, 0)
	if err != nil || len(events) != 1 || events[0].RunID != r.ID {
		t.Fatal(events, err)
	}
	storedApproval, err := s.GetApproval(ctx, a.ID, approval.ID)
	if err != nil || storedApproval.RunID != r.ID || storedApproval.Digest != approval.Digest || storedApproval.Status != "pending" {
		t.Fatal(storedApproval, err)
	}
	if err = s.FinishRun(ctx, r.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	queued, err := s.AcceptQueued(ctx, a.ID, 0, queuePrompt("next"), "", nil, 10)
	if err != nil || queued.Status != "queued" {
		t.Fatal(queued, err)
	}
	if _, err = s.AcceptQueued(ctx, a.ID, 1, queuePrompt("overlap"), "", nil, 10); !errors.Is(err, ErrRunActive) {
		t.Fatal("overlapping queued turn accepted", err)
	}
	stored, messages, err := s.Load(ctx, a.ID)
	if err != nil || stored.Revision != 1 || len(messages) != 1 || messages[0].Content != "next" {
		t.Fatal("rejected overlap changed accepted prompt", stored, messages, err)
	}
	if _, err = s.CreateRun(ctx, a.ID); !errors.Is(err, ErrRunActive) {
		t.Fatal("queued did not reserve task", err)
	}
}

func TestQueueFIFOQuotaCancellationAndRecovery(t *testing.T) {
	s := queueStore(t)
	ctx := context.Background()
	a, _ := s.Create(ctx, "A")
	b, _ := s.Create(ctx, "B")
	c, _ := s.Create(ctx, "C")
	qa, err := s.AcceptQueued(ctx, a.ID, 0, queuePrompt("A"), "", nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	qb, err := s.AcceptQueued(ctx, b.ID, 0, queuePrompt("B"), "", nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptQueued(ctx, c.ID, 0, queuePrompt("C"), "", nil, 2); !errors.Is(err, ErrQueueFull) {
		t.Fatal(err)
	}
	snap, err := s.LoadTaskSnapshot(ctx, c.ID)
	if err != nil || len(snap.Messages) != 0 || len(snap.Runs) != 0 || snap.Session.Revision != 0 {
		t.Fatal("full queue changed input", snap, err)
	}
	pending, err := s.QueuedRuns(ctx)
	if err != nil || len(pending) != 2 || pending[0].ID != qa.ID || pending[1].ID != qb.ID {
		t.Fatal(pending, err)
	}
	run, rev, err := s.ClaimNextQueued(ctx)
	if err != nil || run.ID != qa.ID || run.Status != "running" || rev != 1 {
		t.Fatal(run, rev, err)
	}
	if err = s.CancelQueued(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.ClaimNextQueued(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	qc, err := s.AcceptQueued(ctx, c.ID, 0, queuePrompt("C"), "", nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.InterruptRuns(ctx); err != nil {
		t.Fatal(err)
	}
	for _, r := range []Run{qa, qc} {
		runs, err := s.Runs(ctx, r.SessionID)
		if err != nil || len(runs) != 1 || runs[0].Status != "interrupted" {
			t.Fatal(runs, err)
		}
	}
	runs, _ := s.Runs(ctx, b.ID)
	if runs[0].Status != "cancelled" {
		t.Fatal(runs)
	}
	if pending, err = s.QueuedRuns(ctx); err != nil || len(pending) != 0 {
		t.Fatal("restart replayed queue", pending, err)
	}
}

func TestQueueAtomicQuestionAndFailure(t *testing.T) {
	s := queueStore(t)
	ctx := context.Background()
	a, _ := s.Create(ctx, "A")
	b, _ := s.Create(ctx, "B")
	source, err := s.CreateRun(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishRun(ctx, source.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	card := json.RawMessage(`{"id":"q","state":"continued"}`)
	if _, err = s.AcceptQueued(ctx, b.ID, 0, queuePrompt("wrong task"), source.ID, card, 10); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err = s.AcceptQueued(ctx, b.ID, 0, queuePrompt("fills queue"), "", nil, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptQueued(ctx, a.ID, 0, queuePrompt("answer"), source.ID, card, 1); !errors.Is(err, ErrQueueFull) {
		t.Fatal(err)
	}
	snapshot, err := s.LoadTaskSnapshot(ctx, a.ID)
	if err != nil || len(snapshot.Events) != 0 || len(snapshot.Runs) != 1 || snapshot.Session.Revision != 0 {
		t.Fatal("queue full consumed card", snapshot, err)
	}
	if err = s.CancelQueued(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`CREATE TRIGGER reject_history BEFORE INSERT ON messages BEGIN SELECT RAISE(ABORT, 'injected history failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptQueued(ctx, a.ID, 0, queuePrompt("answer"), source.ID, card, 10); err == nil {
		t.Fatal("missing history failure")
	}
	snapshot, err = s.LoadTaskSnapshot(ctx, a.ID)
	if err != nil || len(snapshot.Events) != 0 || len(snapshot.Runs) != 1 || len(snapshot.Messages) != 0 || snapshot.Session.Revision != 0 {
		t.Fatal("history failure consumed card", snapshot, err)
	}
	if _, err = s.db.Exec(`DROP TRIGGER reject_history`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`CREATE TRIGGER reject_queue BEFORE INSERT ON dispatch_records BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptQueued(ctx, a.ID, 0, queuePrompt("answer"), source.ID, card, 10); err == nil {
		t.Fatal("missing injected failure")
	}
	snap, err := s.LoadTaskSnapshot(ctx, a.ID)
	if err != nil || len(snap.Runs) != 1 || len(snap.Events) != 0 || len(snap.Messages) != 0 || snap.Session.Revision != 0 {
		t.Fatal("partial queue acceptance", snap, err)
	}
	if _, err = s.db.Exec(`DROP TRIGGER reject_queue`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptQueued(ctx, a.ID, 0, queuePrompt("answer"), source.ID, card, 10); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptQueued(ctx, a.ID, 0, queuePrompt("duplicate"), source.ID, card, 10); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	snap, err = s.LoadTaskSnapshot(ctx, a.ID)
	if err != nil || len(snap.Runs) != 2 || len(snap.Events) != 1 || len(snap.Messages) != 1 || snap.Session.Revision != 1 {
		t.Fatal(snap, err)
	}
	if err = s.InterruptRuns(ctx); err != nil {
		t.Fatal(err)
	}
	snap, err = s.LoadTaskSnapshot(ctx, a.ID)
	if err != nil || len(snap.Runs) != 2 || snap.Runs[1].Status != "interrupted" || len(snap.Events) != 1 || string(snap.Events[0].Payload) != string(card) {
		t.Fatal("recovery lost consumed card", snap, err)
	}
	if _, _, err = s.ClaimNextQueued(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatal("recovery replayed card", err)
	}
}

func TestQueueMigrationRollsBackOnInvalidReferences(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	// Simulate damaged historical data. Migration must not publish a new schema
	// or leave foreign-key enforcement off when validation rejects the upgrade.
	for _, q := range []string{`PRAGMA foreign_keys=OFF`, `INSERT INTO events(session_id,run_id,type,payload,created_at) VALUES('missing','missing','card','{}','2026-01-01T00:00:00.000000000Z')`, `PRAGMA foreign_keys=ON`} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	release, err := s.AcquireEngineLock()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err = s.EnsureQueueSchema(ctx); err == nil {
		t.Fatal("invalid foreign key accepted")
	}
	var enabled, count int
	if err = s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil || enabled != 1 {
		t.Fatal(enabled, err)
	}
	if err = s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='dispatch_records'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial migration", count, err)
	}
}
