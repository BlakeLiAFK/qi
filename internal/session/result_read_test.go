package session

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func resultFixture(t *testing.T) (*Store, string, string) {
	t.Helper()
	s, path := openTest(t)
	task, err := s.Create(context.Background(), "result task")
	if err != nil {
		t.Fatal(err)
	}
	return s, path, task.ID
}
func finishResult(t *testing.T, s *Store, task, status string) Run {
	t.Helper()
	r, err := s.CreateRun(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishRun(context.Background(), r.ID, status, ""); err != nil {
		t.Fatal(err)
	}
	return r
}
func assertResult(t *testing.T, s *Store, task, latest string, unread bool) {
	t.Helper()
	snapshot, err := s.LoadTaskSnapshot(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	state := snapshot.ResultRead
	if state.Unread != unread || state.TerminalRunID != latest {
		t.Fatalf("state %+v want %s/%v", state, latest, unread)
	}
	items, err := s.ListTaskMetadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.ID == task {
			if item.UnreadResult != unread || item.UnreadRunID != state.UnreadRunID {
				t.Fatalf("metadata mismatch %+v %+v", item, state)
			}
			return
		}
	}
	t.Fatal("task absent")
}
func TestResultReadTerminalStatesAndPersistence(t *testing.T) {
	ctx := context.Background()
	s, path, task := resultFixture(t)
	assertResult(t, s, task, "", false)
	for _, status := range []string{"completed", "failed", "cancelled", "interrupted"} {
		r := finishResult(t, s, task, status)
		assertResult(t, s, task, r.ID, true)
		if err := s.MarkResultRead(ctx, task, r.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkResultRead(ctx, task, r.ID); err != nil {
			t.Fatal("idempotency", err)
		}
		assertResult(t, s, task, r.ID, false)
	}
	s.Close()
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	snapshot, err := reopened.LoadTaskSnapshot(ctx, task)
	if err != nil || snapshot.ResultRead.Unread {
		t.Fatal(snapshot, err)
	}
	newest := finishResult(t, reopened, task, "completed")
	assertResult(t, reopened, task, newest.ID, true)
}
func TestResultReadOldAndCrossTaskACK(t *testing.T) {
	ctx := context.Background()
	s, _, task := resultFixture(t)
	old := finishResult(t, s, task, "completed")
	snapshot, err := s.LoadTaskSnapshot(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	newer := finishResult(t, s, task, "failed")
	if snapshot.ResultRead.TerminalRunID != old.ID {
		t.Fatal("wrong old snapshot")
	}
	if err = s.MarkResultRead(ctx, task, old.ID); err != nil {
		t.Fatal(err)
	}
	assertResult(t, s, task, newer.ID, true)
	other, err := s.Create(ctx, "other")
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{other.ID, newer.ID}, {task, "missing"}, {"missing", newer.ID}, {task, ""}} {
		if err = s.MarkResultRead(ctx, pair[0], pair[1]); !errors.Is(err, ErrResultUnavailable) {
			t.Fatal(pair, err)
		}
	}
	if err = s.MarkResultRead(ctx, task, newer.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.MarkResultRead(ctx, task, old.ID); err != nil {
		t.Fatal(err)
	}
	assertResult(t, s, task, newer.ID, false)
}
func TestResultReadRejectsActiveAndQueueMigration(t *testing.T) {
	ctx := context.Background()
	s, _, task := resultFixture(t)
	r, err := s.CreateRun(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.MarkResultRead(ctx, task, r.ID); !errors.Is(err, ErrResultUnavailable) {
		t.Fatal(err)
	}
	if err = s.FinishRun(ctx, r.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	// Replacing runs must preserve journal references and reinstall both triggers.
	if err = s.EnsureQueueSchema(ctx); err != nil {
		t.Fatal(err)
	}
	queued, err := s.AcceptQueued(ctx, task, 0, queuePrompt("next"), "", nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.MarkResultRead(ctx, task, queued.ID); !errors.Is(err, ErrResultUnavailable) {
		t.Fatal(err)
	}
	if err = s.CancelQueued(ctx, task); err != nil {
		t.Fatal(err)
	}
	assertResult(t, s, task, queued.ID, true)
	if err = s.MarkResultRead(ctx, task, queued.ID); err != nil {
		t.Fatal(err)
	}
	assertResult(t, s, task, queued.ID, false)
}
func TestResultReadLegacyMigrationBaselinesHistory(t *testing.T) {
	ctx := context.Background()
	s, path, task := resultFixture(t)
	legacy := finishResult(t, s, task, "completed")
	active, err := s.CreateRun(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	// Emulate the previous release, which had neither journal nor watermarks.
	if _, err = s.db.Exec(`DROP TRIGGER record_terminal_result; DROP TRIGGER record_inserted_terminal_result; DROP TABLE task_result_reads; DROP TABLE task_results`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	assertResult(t, s, task, legacy.ID, false)
	if err = s.InterruptRuns(ctx); err != nil {
		t.Fatal(err)
	}
	assertResult(t, s, task, active.ID, true)
	// Ordinary reopen must not silently re-baseline this unread recovery result.
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	assertResult(t, s, task, active.ID, true)
}
func TestResultReadConcurrentTabsMonotone(t *testing.T) {
	ctx := context.Background()
	s, path, task := resultFixture(t)
	old := finishResult(t, s, task, "completed")
	newer := finishResult(t, s, task, "completed")
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); errs <- s.MarkResultRead(ctx, task, old.ID) }()
		go func() { defer wg.Done(); errs <- second.MarkResultRead(ctx, task, newer.ID) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertResult(t, s, task, newer.ID, false)
	last := finishResult(t, s, task, "completed")
	if err = s.MarkResultRead(ctx, task, newer.ID); err != nil {
		t.Fatal(err)
	}
	assertResult(t, s, task, last.ID, true)
}

func TestResultJournalAtomicWithCompletion(t *testing.T) {
	ctx := context.Background()
	s, _, task := resultFixture(t)
	run, err := s.CreateRun(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`CREATE TRIGGER fail_result BEFORE INSERT ON task_results BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishRun(ctx, run.ID, "completed", ""); err == nil {
		t.Fatal("completion escaped journal failure")
	}
	snapshot, err := s.LoadTaskSnapshot(ctx, task)
	if err != nil || snapshot.Runs[0].Status != "running" || snapshot.ResultRead.Unread {
		t.Fatal(snapshot, err)
	}
	if _, err = s.db.Exec(`DROP TRIGGER fail_result`); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishRun(ctx, run.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	assertResult(t, s, task, run.ID, true)
}

func TestResultReadMigrationFailureRollsBackEntireSchema(t *testing.T) {
	ctx := context.Background()
	s, _, task := resultFixture(t)
	old := finishResult(t, s, task, "completed")
	// A legacy orphan is a deterministic SQLite constraint failure during the
	// backfill, after the new tables/index were created. No production test hook.
	if _, err := s.db.Exec(`DROP TRIGGER record_terminal_result; DROP TRIGGER record_inserted_terminal_result; DROP TABLE task_result_reads; DROP TABLE task_results;
 PRAGMA foreign_keys=OFF;
 INSERT INTO runs(id,session_id,status,started_at,finished_at) VALUES('orphan','missing','completed','2026-01-01T00:00:00Z','2026-01-01T00:00:01Z');
 PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if err := s.initResultRead(); err == nil {
		t.Fatal("accepted invalid legacy foreign key")
	}
	var schemaCount int
	if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name IN ('task_results','task_result_reads','task_results_by_task','record_terminal_result','record_inserted_terminal_result')`).Scan(&schemaCount); err != nil || schemaCount != 0 {
		t.Fatal("partial migration survived rollback", schemaCount, err)
	}
	var runCount int
	if err := s.db.QueryRow(`SELECT count(*) FROM runs`).Scan(&runCount); err != nil || runCount != 2 {
		t.Fatal("migration modified legacy history", runCount, err)
	}
	if _, err := s.db.Exec(`DELETE FROM runs WHERE id='orphan'`); err != nil {
		t.Fatal(err)
	}
	if err := s.initResultRead(); err != nil {
		t.Fatal("migration retry failed", err)
	}
	assertResult(t, s, task, old.ID, false)
	next := finishResult(t, s, task, "completed")
	if err := s.MarkResultRead(ctx, task, next.ID); err != nil {
		t.Fatal(err)
	}
	assertResult(t, s, task, next.ID, false)
}
