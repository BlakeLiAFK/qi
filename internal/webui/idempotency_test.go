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
