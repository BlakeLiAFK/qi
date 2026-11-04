package session

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"gopi/internal/testutil"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"gopi/internal/agent"
)

func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(testutil.PrivateDir(t), "history.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}
func TestDurableToolHistory(t *testing.T) {
	s, path := openTest(t)
	ctx := context.Background()
	item, err := s.Create(ctx, "中文 session ' ?")
	if err != nil {
		t.Fatal(err)
	}
	history := []agent.Message{{Role: "system", Content: "help"}, {Role: "user", Content: "你好\x00世界"}, {Role: "assistant", ToolCalls: []agent.ToolCall{{ID: "call1", Name: "tool", Arguments: json.RawMessage(`{"path":"a"}`)}}}, {Role: "tool", ToolCallID: "call1", Content: "result"}, {Role: "assistant", Content: "done"}}
	if err = s.SaveRevision(ctx, item.ID, 0, history); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	signature, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(signature, []byte("SQLite format 3\x00")) {
		t.Fatal("not a SQLite database")
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, messages, err := reopened.Load(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 1 || got.Title != item.Title || !got.CreatedAt.Equal(item.CreatedAt) {
		t.Fatalf("bad session: %+v", got)
	}
	if !reflect.DeepEqual(messages, history) {
		t.Fatalf("history mismatch: %#v", messages)
	}
	listed, err := reopened.List(ctx)
	if err != nil || len(listed) != 1 {
		t.Fatalf("list: %v %v", listed, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Fatalf("database too broadly readable: %o", info.Mode().Perm())
	}
}
func TestInvalidAndMissing(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	if _, err := s.Get(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.Messages(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.Save(ctx, "missing", nil); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	item, err := s.Create(ctx, "keep")
	if err != nil {
		t.Fatal(err)
	}
	history := []agent.Message{{Role: "user", Content: "keep"}}
	if err := s.Save(ctx, item.ID, history); err != nil {
		t.Fatal(err)
	}
	bad := []agent.Message{{Role: "assistant", ToolCalls: []agent.ToolCall{{Arguments: json.RawMessage(`{bad`)}}}}
	if err := s.Save(ctx, item.ID, bad); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	got, err := s.Messages(ctx, item.ID)
	if err != nil || !reflect.DeepEqual(got, history) {
		t.Fatalf("history changed: %v %v", got, err)
	}
	if err := s.SaveRevision(ctx, item.ID, 0, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	if err := s.SaveRevision(ctx, item.ID, -1, nil); err == nil {
		t.Fatal("negative revision accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.Save(cancelled, item.ID, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if err := s.Save(ctx, item.ID, nil); err != nil {
		t.Fatal(err)
	}
	got, err = s.Messages(ctx, item.ID)
	if err != nil || len(got) != 0 {
		t.Fatalf("clear: %v %v", got, err)
	}
}
func TestTransactionalRollback(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	item, err := s.Create(ctx, "rollback")
	if err != nil {
		t.Fatal(err)
	}
	original := []agent.Message{{Role: "user", Content: "original"}}
	if err = s.Save(ctx, item.ID, original); err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`CREATE TRIGGER fail_second BEFORE INSERT ON messages WHEN NEW.position = 1 BEGIN SELECT RAISE(ABORT,'injected failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Save(ctx, item.ID, []agent.Message{{Role: "user", Content: "changed"}, {Role: "assistant", Content: "fail"}}); err == nil {
		t.Fatal("injected failure did not fire")
	}
	got, messages, err := s.Load(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 1 || !reflect.DeepEqual(messages, original) {
		t.Fatalf("partial commit: %+v %#v", got, messages)
	}
}
func TestConcurrentRevisionAcrossConnections(t *testing.T) {
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
		go func(store *Store) {
			defer wg.Done()
			<-start
			results <- store.SaveRevision(ctx, item.ID, 0, []agent.Message{{Role: "user", Content: "winner"}})
		}(store)
	}
	close(start)
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for err := range results {
		if err == nil {
			wins++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
	}
}
func TestSessionIsolation(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	a, err := s.Create(ctx, "A")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Create(ctx, "B")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID {
		t.Fatal("duplicate IDs")
	}
	if err = s.Save(ctx, a.ID, []agent.Message{{Role: "user", Content: "private A"}}); err != nil {
		t.Fatal(err)
	}
	messages, err := s.Messages(ctx, b.ID)
	if err != nil || len(messages) != 0 {
		t.Fatalf("cross-session leakage: %v %v", messages, err)
	}
}
func TestOpenInvalid(t *testing.T) {
	for _, path := range []string{"", filepath.Join(testutil.PrivateDir(t), "missing", "db"), testutil.PrivateDir(t)} {
		if s, err := Open(path); err == nil {
			s.Close()
			t.Fatalf("opened invalid %q", path)
		}
	}
}

func TestLegacySchemaUpgrade(t *testing.T) {
	path := filepath.Join(testutil.PrivateDir(t), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE sessions (id TEXT PRIMARY KEY,title TEXT NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO sessions VALUES ('legacy','old','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	item, err := s.Get(context.Background(), "legacy")
	if err != nil || item.Revision != 0 {
		t.Fatalf("upgrade: %+v %v", item, err)
	}
	if err = s.SaveRevision(context.Background(), item.ID, 0, nil); err != nil {
		t.Fatal(err)
	}
}
func TestConnectionPragmasSurviveReopen(t *testing.T) {
	s, _ := openTest(t)
	// Force a fresh pooled connection. Initialization must apply per connection,
	// not only to the connection used when creating the schema.
	s.db.SetMaxIdleConns(0)
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for pragma, want := range map[string]int{"foreign_keys": 1, "busy_timeout": 5000, "synchronous": 2} {
		var got int
		if err = conn.QueryRowContext(context.Background(), "PRAGMA "+pragma).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s=%d, want %d", pragma, got, want)
		}
	}
}
