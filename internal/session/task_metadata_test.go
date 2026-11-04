package session

import (
	"context"
	"encoding/json"
	"gopi/internal/testutil"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskMetadataDoesNotReadConversationPayloads(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(testutil.PrivateDir(t), "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	idle, err := s.Create(ctx, "Idle")
	if err != nil {
		t.Fatal(err)
	}
	busy, err := s.Create(ctx, "Busy")
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.CreateRun(ctx, busy.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Insert a large event history and deliberately undecodable conversation/card
	// payloads. Metadata must neither materialize nor decode any of these rows.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2500; i++ {
		if _, err := tx.ExecContext(ctx, `INSERT INTO events(session_id,run_id,type,payload,created_at) VALUES(?,?,?,?,?)`, busy.ID, run.ID, "card", `{"revision":"not an integer","padding":"`+strings.Repeat("x", 4096)+`"}`, run.StartedAt.Format(timestampFormat)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO messages(session_id,position,payload) VALUES(?,?,?)`, busy.ID, 0, "not JSON"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Messages(ctx, busy.ID); err == nil {
		t.Fatal("fixture must contain undecodable messages")
	}
	items, err := s.ListTaskMetadata(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != busy.ID || items[0].Status != "running" || items[1].ID != idle.ID || items[1].Status != "idle" {
		t.Fatalf("%+v", items)
	}
	if err := s.FinishRun(ctx, run.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	items, err = s.ListTaskMetadata(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Status != "completed" || !items[0].UpdatedAt.After(run.StartedAt) {
		t.Fatal(items)
	}
	next, err := s.CreateRun(ctx, busy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendEvent(ctx, busy.ID, next.ID, "test", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	items, err = s.ListTaskMetadata(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Status != "running" {
		t.Fatal(items)
	}
	// Guard the query shape as well as its result: the sidebar must never scan
	// conversations/events, and its latest-run subquery must remain indexed.
	rows, err := s.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+taskMetadataQuery)
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
		lower := strings.ToLower(detail)
		if strings.Contains(lower, "messages") || strings.Contains(lower, "events") {
			t.Fatal(detail)
		}
		if strings.Contains(detail, "latest_run_by_session") {
			indexed = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !indexed {
		t.Fatal("latest-run lookup lost its index")
	}
}
