package webui

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"gopi/internal/session"
)

type resultReadBackend struct {
	fakeBackend
	task, run string
	err       error
}

func (b *resultReadBackend) MarkResultRead(_ context.Context, task, run string) error {
	b.calls++
	b.task = task
	b.run = run
	return b.err
}
func TestResultReadHTTPAuthScopeAndValidation(t *testing.T) {
	b := &resultReadBackend{}
	h, err := New(Config{Origin: "http://127.0.0.1:8080", Token: token, Backend: b})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		body   string
		auth   bool
		origin string
		code   int
	}{
		{`{"run_id":"run"}`, false, "", 401},
		{`{"run_id":"run"}`, true, "", 403},
		{`{"run_id":"run"}`, true, "https://evil.example", 403},
		{`{}`, true, "http://127.0.0.1:8080", 400},
		{`{"run_id":null}`, true, "http://127.0.0.1:8080", 400},
		{`{"run_id":" "}`, true, "http://127.0.0.1:8080", 400},
		{`{"run_id":"run","task_id":"other"}`, true, "http://127.0.0.1:8080", 400},
		{`{"run_id":"run","through_seq":99999}`, true, "http://127.0.0.1:8080", 400},
	} {
		w := request(h, "POST", "/api/tasks/one/read-result", tc.body, tc.auth, tc.origin)
		if w.Code != tc.code || b.calls != 0 {
			t.Fatal(tc, w.Code, b.calls)
		}
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:8080/api/tasks/one/read-result", strings.NewReader(`{"run_id":"run"}`))
	r.SetBasicAuth("qi", token)
	r.Header.Set("Origin", "http://127.0.0.1:8080")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 || b.calls != 0 {
		t.Fatal(w.Code, b.calls)
	}
	w = request(h, "GET", "/api/tasks/one/read-result", "", true, "")
	if w.Code != 405 || b.calls != 0 {
		t.Fatal(w.Code, b.calls)
	}
	for i := 0; i < 2; i++ {
		w = request(h, "POST", "/api/tasks/one/read-result", `{"run_id":"exact-run"}`, true, "http://127.0.0.1:8080")
		if w.Code != 200 || b.task != "one" || b.run != "exact-run" || !strings.Contains(w.Body.String(), `"accepted":true`) {
			t.Fatal(w.Code, b, w.Body.String())
		}
	}
	b.err = session.ErrResultUnavailable
	w = request(h, "POST", "/api/tasks/one/read-result", `{"run_id":"other-task-run"}`, true, "http://127.0.0.1:8080")
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
	b.err = errors.New("persistence failure")
	w = request(h, "POST", "/api/tasks/one/read-result", `{"run_id":"exact-run"}`, true, "http://127.0.0.1:8080")
	if w.Code != 500 {
		t.Fatal(w.Code)
	}
}
