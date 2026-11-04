package session

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

func memoryFixture(t *testing.T) (*Store, MemoryScope, MemoryFact) {
	t.Helper()
	s, _ := openTest(t)
	task, err := s.Create(context.Background(), "memory task")
	if err != nil {
		t.Fatal(err)
	}
	return s, MemoryScope{TaskID: task.ID}, MemoryFact{Content: "Use concise examples", SourceRef: "user-message:1", ApprovalRef: "user-message:2", Kind: "preference", Confidence: 1, Confidentiality: "private"}
}

func TestMemoryExplicitScopes(t *testing.T) {
	s, scope, fact := memoryFixture(t)
	ctx := context.Background()
	global, err := s.CreateMemory(ctx, MemoryScope{Global: true}, fact)
	if err != nil {
		t.Fatal(err)
	}
	local, err := s.CreateMemory(ctx, scope, fact)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []MemoryScope{{}, {Global: true, TaskID: scope.TaskID}} {
		if _, err = s.CreateMemory(ctx, bad, fact); err == nil {
			t.Fatal("accepted ambiguous scope")
		}
		if _, err = s.ListMemory(ctx, bad); err == nil {
			t.Fatal("listed ambiguous scope")
		}
		if _, err = s.RetrieveMemory(ctx, bad, nil); err == nil {
			t.Fatal("retrieved ambiguous scope")
		}
	}
	other, _ := s.Create(ctx, "other")
	for _, wrong := range []struct {
		scope MemoryScope
		id    string
	}{{scope, global.ID}, {MemoryScope{Global: true}, local.ID}, {MemoryScope{TaskID: other.ID}, local.ID}} {
		if _, err = s.GetMemory(ctx, wrong.scope, wrong.id); !errors.Is(err, ErrMemoryNotFound) {
			t.Fatalf("cross scope get: %v", err)
		}
		if _, err = s.RetrieveMemory(ctx, wrong.scope, []string{wrong.id}); !errors.Is(err, ErrMemoryNotFound) {
			t.Fatalf("cross scope retrieval: %v", err)
		}
		if _, err = s.MemoryHistory(ctx, wrong.scope, wrong.id); !errors.Is(err, ErrMemoryNotFound) {
			t.Fatalf("cross scope history: %v", err)
		}
		if _, err = s.EditMemory(ctx, wrong.scope, wrong.id, 1, fact); !errors.Is(err, ErrMemoryNotFound) {
			t.Fatalf("cross scope edit: %v", err)
		}
		if err = s.DeleteMemory(ctx, wrong.scope, wrong.id, 1); !errors.Is(err, ErrMemoryNotFound) {
			t.Fatalf("cross scope delete: %v", err)
		}
	}
	list, err := s.ListMemory(ctx, scope)
	if err != nil || len(list) != 1 || list[0].ID != local.ID {
		t.Fatalf("task list: %+v %v", list, err)
	}
	selected, err := s.RetrieveMemory(ctx, MemoryScope{Global: true}, []string{global.ID, global.ID})
	if err != nil || len(selected) != 1 {
		t.Fatalf("explicit selection: %+v %v", selected, err)
	}
	empty, err := s.RetrieveMemory(ctx, MemoryScope{Global: true}, nil)
	if err != nil || len(empty) != 0 {
		t.Fatal("empty selection broadened retrieval")
	}
	if _, err = s.CreateMemory(ctx, MemoryScope{TaskID: "missing"}, fact); err == nil {
		t.Fatal("accepted absent task")
	}
}

func TestMemoryVersionsDeletionAndReopen(t *testing.T) {
	s, scope, fact := memoryFixture(t)
	ctx := context.Background()
	r, err := s.CreateMemory(ctx, scope, fact)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.EditMemory(ctx, scope, r.ID, 1, fact); err == nil {
		t.Fatal("reused approval")
	}
	fact.Content = "Use detailed examples"
	fact.ApprovalRef = "user-message:3"
	edited, err := s.EditMemory(ctx, scope, r.ID, 1, fact)
	if err != nil {
		t.Fatal(err)
	}
	if edited.Revision != 2 || !edited.CreatedAt.Equal(r.CreatedAt) {
		t.Fatal("invalid version metadata")
	}
	if _, err = s.EditMemory(ctx, scope, r.ID, 1, fact); !errors.Is(err, ErrMemoryConflict) {
		t.Fatalf("stale edit: %v", err)
	}
	if err = s.DeleteMemory(ctx, scope, r.ID, 1); !errors.Is(err, ErrMemoryConflict) {
		t.Fatalf("stale delete: %v", err)
	}
	path := s.path
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	history, err := reopened.MemoryHistory(ctx, scope, r.ID)
	if err != nil || len(history) != 2 || history[0].Content != r.Content || history[1].Content != fact.Content {
		t.Fatalf("history: %+v %v", history, err)
	}
	retrieved, err := reopened.RetrieveMemory(ctx, scope, []string{r.ID})
	if err != nil || len(retrieved) != 1 || retrieved[0].Revision != 2 {
		t.Fatalf("retrieved: %+v %v", retrieved, err)
	}
	if err = reopened.DeleteMemory(ctx, scope, r.ID, 2); err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.RetrieveMemory(ctx, scope, []string{r.ID}); !errors.Is(err, ErrMemoryNotFound) {
		t.Fatal(err)
	}
	if _, err = reopened.MemoryHistory(ctx, scope, r.ID); !errors.Is(err, ErrMemoryNotFound) {
		t.Fatal(err)
	}
	var n int
	if err = reopened.db.QueryRow(`SELECT COUNT(*) FROM curated_memory_versions WHERE memory_id=?`, r.ID).Scan(&n); err != nil || n != 0 {
		t.Fatal("deleted history retained", n, err)
	}
}

func TestMemoryValidationAndExpiry(t *testing.T) {
	s, scope, base := memoryFixture(t)
	ctx := context.Background()
	for name, mutate := range map[string]func(*MemoryFact){
		"approval": func(f *MemoryFact) { f.ApprovalRef = "" }, "source": func(f *MemoryFact) { f.SourceRef = " " },
		"content":    func(f *MemoryFact) { f.Content = strings.Repeat("x", MaxMemoryContentBytes+1) },
		"confidence": func(f *MemoryFact) { f.Confidence = math.NaN() }, "negative": func(f *MemoryFact) { f.Confidence = -1 },
		"secret": func(f *MemoryFact) { f.Confidentiality = "secret" }, "sensitive": func(f *MemoryFact) { f.Confidentiality = "sensitive" },
		"kind": func(f *MemoryFact) { f.Kind = "automatic" }, "null": func(f *MemoryFact) { f.Content = "x\x00y" },
		"utf8": func(f *MemoryFact) { f.Content = string([]byte{0xff}) },
	} {
		t.Run(name, func(t *testing.T) {
			f := base
			mutate(&f)
			if _, err := s.CreateMemory(ctx, scope, f); err == nil {
				t.Fatal("invalid fact accepted")
			}
		})
	}
	past := time.Now().Add(-time.Hour)
	base.ExpiresAt = &past
	r, err := s.CreateMemory(ctx, scope, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RetrieveMemory(ctx, scope, []string{r.ID}); !errors.Is(err, ErrMemoryNotFound) {
		t.Fatalf("expired retrieval: %v", err)
	}
	if _, err = s.GetMemory(ctx, scope, r.ID); err != nil {
		t.Fatal("expired record not inspectable", err)
	}
	if _, err = s.RetrieveMemory(ctx, scope, make([]string, 33)); !errors.Is(err, ErrMemoryLimit) {
		t.Fatal("unbounded retrieval", err)
	}
}

func TestMemoryBoundsAndTaskDeletion(t *testing.T) {
	s, scope, fact := memoryFixture(t)
	ctx := context.Background()
	r, err := s.CreateMemory(ctx, scope, fact)
	if err != nil {
		t.Fatal(err)
	}
	for v := int64(1); v < MaxMemoryVersions; v++ {
		fact.ApprovalRef = fmt.Sprintf("approval:%d", v)
		r, err = s.EditMemory(ctx, scope, r.ID, v, fact)
		if err != nil {
			t.Fatal(err)
		}
	}
	fact.ApprovalRef = "approval:overflow"
	if _, err = s.EditMemory(ctx, scope, r.ID, MaxMemoryVersions, fact); !errors.Is(err, ErrMemoryLimit) {
		t.Fatal("unbounded versions", err)
	}
	// Populate the bounded index directly to avoid 1,023 unrelated disk syncs.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < MaxMemoryRecords; i++ {
		if _, err = tx.Exec(`INSERT INTO curated_memory(id,scope,task_id,revision) VALUES(?,?,?,1)`, fmt.Sprintf("fixture-%d", i), "task:"+scope.TaskID, scope.TaskID); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateMemory(ctx, MemoryScope{Global: true}, fact); !errors.Is(err, ErrMemoryLimit) {
		t.Fatal("unbounded records", err)
	}
	if _, err = s.db.Exec(`DELETE FROM sessions WHERE id=?`, scope.TaskID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM curated_memory_versions`).Scan(&n); err != nil || n != 0 {
		t.Fatal("task deletion left facts", n, err)
	}
}

func TestMemoryConcurrentRevisionGuard(t *testing.T) {
	s, scope, fact := memoryFixture(t)
	ctx := context.Background()
	r, err := s.CreateMemory(ctx, scope, fact)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	start := make(chan struct{})
	results := make(chan error, 2)
	for i, store := range []*Store{s, second} {
		go func(i int, store *Store) {
			<-start
			f := fact
			f.Content = fmt.Sprintf("revision candidate %d", i)
			f.ApprovalRef = fmt.Sprintf("approval candidate %d", i)
			_, err := store.EditMemory(ctx, scope, r.ID, 1, f)
			results <- err
		}(i, store)
	}
	close(start)
	successes := 0
	for i := 0; i < 2; i++ {
		if <-results == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly one writer, got %d", successes)
	}
	history, err := s.MemoryHistory(ctx, scope, r.ID)
	if err != nil || len(history) != 2 {
		t.Fatalf("concurrent history: %+v %v", history, err)
	}
}

func TestMemoryMutationRollback(t *testing.T) {
	s, scope, fact := memoryFixture(t)
	ctx := context.Background()
	exec := func(query string) {
		t.Helper()
		if _, err := s.db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE TRIGGER reject_memory_version BEFORE INSERT ON curated_memory_versions BEGIN SELECT RAISE(ABORT, 'injected version failure'); END`)
	if _, err := s.CreateMemory(ctx, scope, fact); err == nil {
		t.Fatal("create ignored injected failure")
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM curated_memory`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed create left index row", count, err)
	}
	exec(`DROP TRIGGER reject_memory_version`)
	r, err := s.CreateMemory(ctx, scope, fact)
	if err != nil {
		t.Fatal(err)
	}
	exec(`CREATE TRIGGER reject_memory_version BEFORE INSERT ON curated_memory_versions BEGIN SELECT RAISE(ABORT, 'injected version failure'); END`)
	changed := fact
	changed.Content = "changed preference"
	changed.ApprovalRef = "approval:changed"
	if _, err = s.EditMemory(ctx, scope, r.ID, 1, changed); err == nil {
		t.Fatal("edit ignored injected failure")
	}
	got, err := s.GetMemory(ctx, scope, r.ID)
	if err != nil || got.Revision != 1 || got.Content != fact.Content {
		t.Fatalf("failed edit modified record: %+v %v", got, err)
	}
	var revision int64
	if err = s.db.QueryRow(`SELECT revision FROM curated_memory WHERE id=?`, r.ID).Scan(&revision); err != nil || revision != 1 {
		t.Fatal("failed edit modified revision pointer", revision, err)
	}
	history, err := s.MemoryHistory(ctx, scope, r.ID)
	if err != nil || len(history) != 1 {
		t.Fatalf("failed edit modified history: %+v %v", history, err)
	}
	exec(`DROP TRIGGER reject_memory_version`)
	r, err = s.EditMemory(ctx, scope, r.ID, 1, changed)
	if err != nil {
		t.Fatal(err)
	}
	exec(`CREATE TRIGGER reject_memory_delete BEFORE DELETE ON curated_memory_versions BEGIN SELECT RAISE(ABORT, 'injected cascade failure'); END`)
	if err = s.DeleteMemory(ctx, scope, r.ID, 2); err == nil {
		t.Fatal("delete ignored injected cascade failure")
	}
	history, err = s.MemoryHistory(ctx, scope, r.ID)
	if err != nil || len(history) != 2 || history[1].Content != changed.Content {
		t.Fatalf("failed delete modified history: %+v %v", history, err)
	}
	got, err = s.GetMemory(ctx, scope, r.ID)
	if err != nil || got.Revision != 2 {
		t.Fatalf("failed delete removed index: %+v %v", got, err)
	}
	exec(`DROP TRIGGER reject_memory_delete`)
	if err = s.DeleteMemory(ctx, scope, r.ID, 2); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM curated_memory_versions`).Scan(&count); err != nil || count != 0 {
		t.Fatal("successful delete left versions", count, err)
	}
}

func TestMemoryConcurrentEditDelete(t *testing.T) {
	s, scope, fact := memoryFixture(t)
	ctx := context.Background()
	second, err := Open(s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	for i := 0; i < 20; i++ {
		r, err := s.CreateMemory(ctx, scope, fact)
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		editResult := make(chan error, 1)
		deleteResult := make(chan error, 1)
		go func() {
			<-start
			changed := fact
			changed.Content = "edited preference"
			changed.ApprovalRef = "approval:edit"
			_, err := s.EditMemory(ctx, scope, r.ID, 1, changed)
			editResult <- err
		}()
		go func() { <-start; deleteResult <- second.DeleteMemory(ctx, scope, r.ID, 1) }()
		close(start)
		editErr, deleteErr := <-editResult, <-deleteResult
		if (editErr == nil) == (deleteErr == nil) {
			t.Fatalf("expected one successful operation, edit=%v delete=%v", editErr, deleteErr)
		}
		history, err := s.MemoryHistory(ctx, scope, r.ID)
		if editErr == nil {
			if err != nil || len(history) != 2 || history[1].Revision != 2 || history[1].Content != "edited preference" {
				t.Fatalf("edit winner lost data: %+v %v", history, err)
			}
			if err = s.DeleteMemory(ctx, scope, r.ID, 2); err != nil {
				t.Fatal(err)
			}
		} else if !errors.Is(err, ErrMemoryNotFound) {
			t.Fatalf("delete winner left retrievable memory: %+v %v", history, err)
		}
		var count int
		if err = s.db.QueryRow(`SELECT COUNT(*) FROM curated_memory_versions WHERE memory_id=?`, r.ID).Scan(&count); err != nil || count != 0 {
			t.Fatal("orphaned versions", count, err)
		}
	}
}

func TestMemoryIDsAndAtomicSelection(t *testing.T) {
	s, scope, fact := memoryFixture(t)
	ctx := context.Background()
	r, err := s.CreateMemory(ctx, scope, fact)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", strings.Repeat("a", 33), strings.Repeat("z", 32), strings.Repeat("a", 10000)} {
		if _, err = s.GetMemory(ctx, scope, id); err == nil {
			t.Fatal("get accepted invalid ID")
		}
		if _, err = s.EditMemory(ctx, scope, id, 1, fact); err == nil {
			t.Fatal("edit accepted invalid ID")
		}
		if _, err = s.MemoryHistory(ctx, scope, id); err == nil {
			t.Fatal("history accepted invalid ID")
		}
		if err = s.DeleteMemory(ctx, scope, id, 1); err == nil {
			t.Fatal("delete accepted invalid ID")
		}
		if got, err := s.RetrieveMemory(ctx, scope, []string{r.ID, id}); err == nil || got != nil {
			t.Fatalf("invalid ID returned partial result: %+v %v", got, err)
		}
	}
	if got, err := s.RetrieveMemory(ctx, scope, []string{r.ID, strings.Repeat("0", 32)}); !errors.Is(err, ErrMemoryNotFound) || got != nil {
		t.Fatalf("missing ID returned partial result: %+v %v", got, err)
	}
	expired := fact
	past := time.Now().Add(-time.Hour)
	expired.ExpiresAt = &past
	old, err := s.CreateMemory(ctx, scope, expired)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.RetrieveMemory(ctx, scope, []string{r.ID, old.ID}); !errors.Is(err, ErrMemoryNotFound) || got != nil {
		t.Fatalf("expired ID returned partial result: %+v %v", got, err)
	}
}

func TestMemoryIgnoredMutationRejected(t *testing.T) {
	s, scope, fact := memoryFixture(t)
	ctx := context.Background()
	r, err := s.CreateMemory(ctx, scope, fact)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`CREATE TRIGGER ignore_memory_update BEFORE UPDATE ON curated_memory BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	fact.ApprovalRef = "approval:replacement"
	if _, err = s.EditMemory(ctx, scope, r.ID, 1, fact); !errors.Is(err, ErrMemoryConflict) {
		t.Fatal("ignored update reported success", err)
	}
	history, err := s.MemoryHistory(ctx, scope, r.ID)
	if err != nil || len(history) != 1 {
		t.Fatal("ignored update left version", history, err)
	}
	if _, err = s.db.Exec(`CREATE TRIGGER ignore_memory_delete BEFORE DELETE ON curated_memory BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteMemory(ctx, scope, r.ID, 1); !errors.Is(err, ErrMemoryConflict) {
		t.Fatal("ignored delete reported success", err)
	}
	if _, err = s.GetMemory(ctx, scope, r.ID); err != nil {
		t.Fatal(err)
	}
}
