package webui

import (
	"context"
	"errors"
	"gopi/internal/session"
	"net/http/httptest"
	"strings"
	"testing"
)

type archiveBackend struct {
	fakeBackend
	archived bool
	id       string
	err      error
}

func (b *archiveBackend) ListArchived(_ context.Context, archived bool) ([]Task, error) {
	return []Task{{ID: "hidden", Archived: archived}}, nil
}
func (b *archiveBackend) SetArchived(_ context.Context, id string, archived bool) error {
	b.calls++
	b.id = id
	b.archived = archived
	return b.err
}
func TestArchiveHTTPAuthorizationValidationAndFilters(t *testing.T) {
	b := &archiveBackend{}
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
		{`{"archived":true}`, false, "", 401},
		{`{"archived":true}`, true, "", 403},
		{`{"archived":true}`, true, "https://evil.example", 403},
		{`{}`, true, "http://127.0.0.1:8080", 400},
		{`{"archived":null}`, true, "http://127.0.0.1:8080", 400},
		{`{"archived":"true"}`, true, "http://127.0.0.1:8080", 400},
		{`{"archived":true,"id":"other"}`, true, "http://127.0.0.1:8080", 400},
	} {
		w := request(h, "POST", "/api/tasks/one/archive", tc.body, tc.auth, tc.origin)
		if w.Code != tc.code || b.calls != 0 {
			t.Fatal(tc, w.Code, b.calls)
		}
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:8080/api/tasks/one/archive", strings.NewReader(`{"archived":true}`))
	r.SetBasicAuth("qi", token)
	r.Header.Set("Origin", "http://127.0.0.1:8080")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 || b.calls != 0 {
		t.Fatal(w.Code, b.calls)
	}
	for _, archived := range []string{"true", "false"} {
		w := request(h, "POST", "/api/tasks/one/archive", `{"archived":`+archived+`}`, true, "http://127.0.0.1:8080")
		if w.Code != 200 || b.id != "one" || b.archived != (archived == "true") {
			t.Fatal(w.Code, b)
		}
	}
	for _, failure := range []error{session.ErrRunActive, errors.New("failed persistence")} {
		b.err = failure
		w := request(h, "POST", "/api/tasks/one/archive", `{"archived":true}`, true, "http://127.0.0.1:8080")
		if w.Code != 409 {
			t.Fatal(w.Code)
		}
	}
	b.err = session.ErrNotFound
	w = request(h, "POST", "/api/tasks/missing/archive", `{"archived":true}`, true, "http://127.0.0.1:8080")
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
	w = request(h, "GET", "/api/tasks?archived=true", "", true, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"hidden"`) {
		t.Fatal(w.Code, w.Body)
	}
	w = request(h, "GET", "/api/tasks", "", true, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), `"id":"hidden"`) {
		t.Fatal(w.Code, w.Body)
	}
	w = request(h, "GET", "/api/tasks?archived=invalid", "", true, "")
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}
