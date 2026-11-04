package webui

import (
	"context"
	"gopi/internal/session"
	"gopi/internal/tasks"
	"testing"
)

type memorySubmissionBackend struct {
	submissionBackend
	selection []session.MemorySelection
	disclosed bool
}

func (b *memorySubmissionBackend) SubmitWithMemory(_ context.Context, id, key, prompt string, names []string, selection []session.MemorySelection, disclosed bool, destination string) (tasks.Run, error) {
	b.calls++
	b.selection = selection
	b.disclosed = disclosed
	return tasks.Run{ID: "memory-run", SessionID: id, Status: "queued"}, nil
}
func TestHTTPMemorySelectionRequiresExplicitDisclosureAndKey(t *testing.T) {
	b := &memorySubmissionBackend{}
	h, err := New(Config{Origin: "http://127.0.0.1:8080", Token: token, Backend: b})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"prompt":"hello","memory":[{"id":"abc","revision":1}],"idempotency_key":"key"}`,
		`{"prompt":"hello","memory":[{"id":"abc","revision":1}],"memory_disclosed":true}`,
		`{"prompt":"hello","memory":[{"id":"abc","revision":1,"task_id":"other"}],"memory_disclosed":true,"idempotency_key":"key"}`,
	} {
		w := request(h, "POST", "/api/tasks/one/messages", body, true, "http://127.0.0.1:8080")
		if w.Code != 400 || b.calls != 0 {
			t.Fatal(w.Code, w.Body, b)
		}
	}
	body := `{"prompt":"hello","memory":[{"id":"abc","revision":1,"global":true}],"memory_disclosed":true,"idempotency_key":"key"}`
	w := request(h, "POST", "/api/tasks/one/messages", body, true, "http://127.0.0.1:8080")
	if w.Code != 200 || b.calls != 1 || !b.disclosed || !b.selection[0].Global {
		t.Fatal(w.Code, w.Body, b)
	}
	for _, auth := range []bool{false, true} {
		origin := "http://evil.example"
		if !auth {
			origin = "http://127.0.0.1:8080"
		}
		w = request(h, "POST", "/api/tasks/one/messages", body, auth, origin)
		if w.Code < 400 || b.calls != 1 {
			t.Fatal(w.Code, b.calls)
		}
	}
}
