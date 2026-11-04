package session

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"gopi/internal/agent"
)

// completeBetweenReads forces the exact old failure window: history has already
// been read, then a second connection commits the final response and run status,
// before the reader asks for runs. There are no sleeps or scheduler assumptions.
type completeBetweenReads struct {
	snapshotReader
	complete func() error
}

func (q *completeBetweenReads) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if strings.Contains(query, "FROM runs") && q.complete != nil {
		f := q.complete
		q.complete = nil
		if err := f(); err != nil {
			return nil, err
		}
	}
	return q.snapshotReader.QueryContext(ctx, query, args...)
}

func TestTaskSnapshotCompletionBetweenHistoryAndStatus(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "snapshot.db")
	reader, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	writer, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	for _, transactional := range []bool{false, true} {
		name := "old_separate_reads_reproduces_race"
		if transactional {
			name = "read_transaction_keeps_snapshot"
		}
		t.Run(name, func(t *testing.T) {
			task, err := reader.Create(ctx, name)
			if err != nil {
				t.Fatal(err)
			}
			history := []agent.Message{{Role: "user", Content: "hello"}}
			if err := reader.Save(ctx, task.ID, history); err != nil {
				t.Fatal(err)
			}
			run, err := reader.CreateRun(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			var source snapshotReader = reader.db
			var tx *sql.Tx
			if transactional {
				tx, err = reader.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				source = tx
			}
			intercepted := &completeBetweenReads{snapshotReader: source, complete: func() error {
				final := append(history, agent.Message{Role: "assistant", Content: "final answer"})
				if err := writer.Save(ctx, task.ID, final); err != nil {
					return err
				}
				if _, err := writer.AppendEvent(ctx, task.ID, run.ID, "message", []byte(`{"message":{"role":"assistant","content":"final answer"}}`)); err != nil {
					return err
				}
				return writer.FinishRun(ctx, run.ID, "completed", "")
			}}
			snapshot, err := readTaskSnapshot(ctx, intercepted, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Messages) != 1 || len(snapshot.Runs) != 1 {
				t.Fatalf("bad fixture: %+v", snapshot)
			}
			if transactional {
				if snapshot.Runs[0].Status != "running" || len(snapshot.Events) != 0 {
					t.Fatalf("mixed snapshot: %+v", snapshot)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			} else if snapshot.Runs[0].Status != "completed" || len(snapshot.Events) != 1 {
				t.Fatalf("old race not reproduced: %+v", snapshot)
			}
			fresh, err := reader.LoadTaskSnapshot(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(fresh.Messages) != 2 || fresh.Messages[1].Content != "final answer" || fresh.Runs[0].Status != "completed" || len(fresh.Events) != 1 {
				t.Fatalf("new snapshot lost final response: %+v", fresh)
			}
		})
	}
}

func TestTaskSnapshotMissingAndCanceled(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "missing.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.LoadTaskSnapshot(context.Background(), "missing"); err != ErrNotFound {
		t.Fatalf("got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.LoadTaskSnapshot(ctx, "missing"); err == nil {
		t.Fatal("canceled read succeeded")
	}
}
