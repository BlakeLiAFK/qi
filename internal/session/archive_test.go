package session

import (
	"context"
	"encoding/json"
	"errors"
	"gopi/internal/artifacts"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestArchivePreservesHistoryArtifactsAuditAndReplayAcrossRestart(t *testing.T) {
	s := queueStore(t)
	ctx := context.Background()
	task, _ := s.Create(ctx, "keep everything")
	other, _ := s.Create(ctx, "other")
	identity := AdmissionIdentity{"owner", "request", strings.Repeat("a", 64)}
	run, err := s.AcceptQueued(ctx, task.ID, 0, queuePrompt("private prompt"), "", nil, 2, identity)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetArchived(ctx, task.ID, true); !errors.Is(err, ErrRunActive) {
		t.Fatal(err)
	}
	if _, _, err = s.ClaimNextQueued(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.SetArchived(ctx, task.ID, true); !errors.Is(err, ErrRunActive) {
		t.Fatal(err)
	}
	if _, err = s.AppendEvent(ctx, task.ID, run.ID, "test", json.RawMessage(`{"private":true}`)); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishRun(ctx, run.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	artifact := artifacts.Artifact{ID: "artifact", TaskID: task.ID, Name: "report", Path: "report.txt", MediaType: "text/plain", Size: 10, SHA256: strings.Repeat("b", 64), CreatedAt: time.Now().UTC()}
	if _, err = s.SaveArtifact(ctx, artifact); err != nil {
		t.Fatal(err)
	}
	before, _ := s.LoadTaskSnapshot(ctx, task.ID)
	for i := 0; i < 2; i++ {
		if err = s.SetArchived(ctx, task.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	listed, err := s.List(ctx)
	if err != nil || len(listed) != 1 || listed[0].ID != other.ID {
		t.Fatal(listed, err)
	}
	archived, err := s.ListTaskMetadataArchived(ctx, true)
	if err != nil || len(archived) != 1 || archived[0].ID != task.ID || !archived[0].Archived {
		t.Fatal(archived, err)
	}
	if _, err = s.CreateRun(ctx, task.ID); !errors.Is(err, ErrArchived) {
		t.Fatal(err)
	}
	if _, err = s.AcceptQueued(ctx, task.ID, 1, queuePrompt("new"), "", nil, 2); !errors.Is(err, ErrArchived) {
		t.Fatal(err)
	}
	replay, err := s.AcceptQueued(ctx, task.ID, 0, queuePrompt("private prompt"), "", nil, 2, identity)
	if err != nil || replay.ID != run.ID {
		t.Fatal(replay, err)
	}
	var count int
	if err = s.db.QueryRow(`SELECT count(*) FROM task_archive_events WHERE session_id=?`, task.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	path := s.path
	s.Close()
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after, err := reopened.LoadTaskSnapshot(ctx, task.ID)
	if err != nil || !after.Session.Archived || !reflect.DeepEqual(before.Messages, after.Messages) || !reflect.DeepEqual(before.Runs, after.Runs) {
		t.Fatal(after, err)
	}
	events, err := reopened.Events(ctx, task.ID, 0)
	if err != nil || len(events) != 1 {
		t.Fatal(events, err)
	}
	saved, err := reopened.GetArtifact(ctx, task.ID, artifact.ID)
	if err != nil || saved.SHA256 != artifact.SHA256 {
		t.Fatal(saved, err)
	}
	if _, err = reopened.GetArtifact(ctx, other.ID, artifact.ID); err == nil {
		t.Fatal("cross-task artifact leak")
	}
	if prior, found, err := reopened.LookupAdmission(ctx, task.ID, identity); err != nil || !found || prior.ID != run.ID {
		t.Fatal(prior, found, err)
	}
	if err = reopened.SetArchived(ctx, task.ID, false); err != nil {
		t.Fatal(err)
	}
	if err = reopened.SetArchived(ctx, "missing", true); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	listed, err = reopened.List(ctx)
	if err != nil || len(listed) != 2 {
		t.Fatal(listed, err)
	}
}

func TestArchiveRacesAdmission(t *testing.T) {
	s := queueStore(t)
	ctx := context.Background()
	// Direct storage races verify the invariant without relying on engine.mu.
	for i := 0; i < 40; i++ {
		task, err := s.Create(ctx, "race")
		if err != nil {
			t.Fatal(err)
		}
		var archiveErr, admitErr error
		var run Run
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); <-start; archiveErr = s.SetArchived(ctx, task.ID, true) }()
		go func() {
			defer wg.Done()
			<-start
			run, admitErr = s.AcceptQueued(ctx, task.ID, 0, queuePrompt("race"), "", nil, 100)
		}()
		close(start)
		wg.Wait()
		if archiveErr == nil && admitErr == nil {
			t.Fatal("archived and admitted together")
		}
		if archiveErr != nil && !errors.Is(archiveErr, ErrRunActive) {
			t.Fatal(archiveErr)
		}
		if admitErr != nil && !errors.Is(admitErr, ErrArchived) {
			t.Fatal(admitErr)
		}
		if admitErr == nil {
			if err = s.FinishRun(ctx, run.ID, "cancelled", ""); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestArchiveAtomicAuditFailureAndIndependentConnectionRace(t *testing.T) {
	s := queueStore(t)
	ctx := context.Background()
	task, _ := s.Create(ctx, "rollback")
	if _, err := s.db.Exec(`CREATE TRIGGER reject_archive_audit BEFORE INSERT ON task_archive_events BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetArchived(ctx, task.ID, true); err == nil {
		t.Fatal("expected failure")
	}
	stored, err := s.Get(ctx, task.ID)
	if err != nil || stored.Archived {
		t.Fatal(stored, err)
	}
	if _, err = s.db.Exec(`DROP TRIGGER reject_archive_audit`); err != nil {
		t.Fatal(err)
	}
	other, err := Open(s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	for i := 0; i < 30; i++ {
		task, err := s.Create(ctx, "connection race")
		if err != nil {
			t.Fatal(err)
		}
		var archiveErr, runErr error
		var run Run
		var wg sync.WaitGroup
		wg.Add(2)
		start := make(chan struct{})
		go func() { defer wg.Done(); <-start; archiveErr = s.SetArchived(ctx, task.ID, true) }()
		go func() { defer wg.Done(); <-start; run, runErr = other.CreateRun(ctx, task.ID) }()
		close(start)
		wg.Wait()
		if archiveErr == nil && runErr == nil {
			t.Fatal("archive raced run admission")
		}
		if archiveErr != nil && !errors.Is(archiveErr, ErrRunActive) {
			t.Fatal(archiveErr)
		}
		if runErr != nil && !errors.Is(runErr, ErrArchived) {
			t.Fatal(runErr)
		}
		if runErr == nil {
			if err = s.FinishRun(ctx, run.ID, "cancelled", ""); err != nil {
				t.Fatal(err)
			}
		}
	}
}
