package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotProjectsOnlyLatestOpenStream(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "projection.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	task, _ := s.Create(ctx, "task")
	other, _ := s.Create(ctx, "other")
	old, _ := s.CreateRun(ctx, task.ID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO events(session_id,run_id,type,payload,created_at) VALUES(?,?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	// Invalid timestamps make the test fail if a historical preview is ever read.
	// Many requests in one historical run exercise indexed request boundaries.
	for request := 0; request < 100; request++ {
		if _, err := stmt.ExecContext(ctx, task.ID, old.ID, "model_start", `{}`, "unreadable old timestamp"); err != nil {
			t.Fatal(err)
		}
		for delta := 0; delta < 256; delta++ {
			if _, err := stmt.ExecContext(ctx, task.ID, old.ID, "model_delta", `{"delta":"`+strings.Repeat("old", 40)+`"}`, "unreadable old timestamp"); err != nil {
				t.Fatal(err)
			}
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(ctx, old.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	run, _ := s.CreateRun(ctx, task.ID)
	foreign, _ := s.CreateRun(ctx, other.ID)
	appendEvent := func(task, run, kind, payload string) Event {
		t.Helper()
		event, err := s.AppendEvent(ctx, task, run, kind, json.RawMessage(payload))
		if err != nil {
			t.Fatal(err)
		}
		return event
	}
	appendEvent(task.ID, run.ID, "model_start", `{}`)
	appendEvent(task.ID, run.ID, "model_delta", `{"delta":"obsolete request"}`)
	appendEvent(task.ID, run.ID, "message", `{}`)
	card := appendEvent(task.ID, run.ID, "card", `{"id":"card"}`)
	notice := appendEvent(task.ID, run.ID, "context_trim", `{"dropped_messages":2}`)
	start := appendEvent(task.ID, run.ID, "model_start", `{}`)
	first := appendEvent(task.ID, run.ID, "model_delta", `{"delta":"hello "}`)
	appendEvent(other.ID, foreign.ID, "model_start", `{}`)
	appendEvent(other.ID, foreign.ID, "model_delta", `{"delta":"private foreign task"}`)
	second := appendEvent(task.ID, run.ID, "model_delta", `{"delta":"world"}`)
	check := func(want []int64) {
		t.Helper()
		snapshot, err := s.LoadTaskSnapshot(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Events) != len(want) {
			t.Fatalf("loaded %d events, want %d", len(snapshot.Events), len(want))
		}
		for i, event := range snapshot.Events {
			if event.ID != want[i] || event.SessionID != task.ID {
				t.Fatalf("wrong event %+v", event)
			}
		}
	}
	check([]int64{card.ID, notice.ID, start.ID, first.ID, second.ID})
	// Closing the request clears provisional content before run completion.
	appendEvent(task.ID, run.ID, "message", `{}`)
	check([]int64{card.ID, notice.ID})
	for _, closeType := range []string{"error", "run_end"} {
		appendEvent(task.ID, run.ID, "model_start", `{}`)
		appendEvent(task.ID, run.ID, "model_delta", `{"delta":"cleared"}`)
		appendEvent(task.ID, run.ID, closeType, `{}`)
		check([]int64{card.ID, notice.ID})
	}
	appendEvent(task.ID, run.ID, "model_start", `{}`)
	for i := 0; i < 257; i++ {
		appendEvent(task.ID, run.ID, "model_delta", `{"delta":"overflow"}`)
	}
	check([]int64{card.ID, notice.ID}) // fail closed beyond the producer's budget
	appendEvent(task.ID, run.ID, "model_start", `{}`)
	appendEvent(task.ID, run.ID, "model_delta", `{"delta":"last"}`)
	if err := s.FinishRun(ctx, run.ID, "interrupted", ""); err != nil {
		t.Fatal(err)
	}
	check([]int64{card.ID, notice.ID})
	rows, err := s.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+snapshotEventsQuery, sql.Named("task", task.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	indexed := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(detail, "events_by_session_type_run") {
			indexed = true
		}
		if strings.Contains(strings.ToUpper(detail), "SCAN EVENTS") {
			t.Fatalf("unbounded event scan: %s", detail)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !indexed {
		t.Fatal("projection lost stream index")
	}
}
