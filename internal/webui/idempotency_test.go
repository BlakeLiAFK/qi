package webui

import (
	"context"
	"errors"
	"gopi/internal/tasks"
	"net/http"
	"strings"
	"testing"
)

type submissionBackend struct {
	fakeBackend
	key, prompt string
	fail        bool
}

func (b *submissionBackend) Submit(_ context.Context, id, key, prompt string, names []string) (tasks.Run, error) {
	b.calls++
	b.key = key
	b.prompt = prompt
	if b.fail {
		return tasks.Run{}, errors.New("conflict")
	}
	return tasks.Run{ID: "accepted-run", SessionID: id, Status: "queued"}, nil
}
func TestHTTPIdempotentSubmission(t *testing.T) {
	b := &submissionBackend{}
	h, err := New(Config{Origin: "http://127.0.0.1:8080", Token: token, Backend: b})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"prompt":"hello","idempotency_key":"client-key"}`
	w := request(h, "POST", "/api/tasks/one/messages", body, true, "http://127.0.0.1:8080")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"id":"accepted-run"`) || b.key != "client-key" {
		t.Fatal(w.Code, w.Body, b)
	}
	b.fail = true
	w = request(h, "POST", "/api/tasks/one/messages", body, true, "http://127.0.0.1:8080")
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
	before := b.calls
	w = request(h, "POST", "/api/tasks/one/messages", `{"prompt":"hello","idempotency_key":"invalid key"}`, true, "http://127.0.0.1:8080")
	if w.Code != 400 || b.calls != before {
		t.Fatal(w.Code, b.calls)
	}
	w = request(h, "POST", "/api/tasks/one/messages", body, false, "")
	if w.Code != 401 || b.calls != before {
		t.Fatal(w.Code, b.calls)
	}
}

type creationBackend struct {
	fakeBackend
	key, title string
	fail       bool
}

func (b *creationBackend) CreateIdempotent(_ context.Context, key, title string) (Task, error) {
	b.calls++
	b.key = key
	b.title = title
	if b.fail {
		return Task{}, errors.New("conflict")
	}
	return Task{ID: "original-task", Title: title}, nil
}
func TestHTTPIdempotentTaskCreation(t *testing.T) {
	b := &creationBackend{}
	h, err := New(Config{Origin: "http://127.0.0.1:8080", Token: token, Backend: b})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"title":"hello","idempotency_key":"creation-key"}`
	w := request(h, "POST", "/api/tasks", body, true, "http://127.0.0.1:8080")
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"id":"original-task"`) || b.key != "creation-key" {
		t.Fatal(w.Code, w.Body, b)
	}
	b.fail = true
	w = request(h, "POST", "/api/tasks", body, true, "http://127.0.0.1:8080")
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
	before := b.calls
	w = request(h, "POST", "/api/tasks", `{"title":"hello","idempotency_key":"invalid key"}`, true, "http://127.0.0.1:8080")
	if w.Code != 400 || b.calls != before {
		t.Fatal(w.Code, b.calls)
	}
	w = request(h, "POST", "/api/tasks", body, false, "http://127.0.0.1:8080")
	if w.Code != 401 || b.calls != before {
		t.Fatal(w.Code, b.calls)
	}
	// Legacy request still reaches the original method.
	w = request(h, "POST", "/api/tasks", `{"title":"legacy"}`, true, "http://127.0.0.1:8080")
	if w.Code != 202 || b.calls != before+1 {
		t.Fatal(w.Code, b.calls)
	}
}
