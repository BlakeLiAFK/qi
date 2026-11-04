package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gopi/internal/agent"
	"gopi/internal/session"
	"gopi/internal/tasks"
	"gopi/internal/testutil"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func memoryServer(t *testing.T) (http.Handler, *session.Store, string) {
	t.Helper()
	store, err := session.Open(filepath.Join(testutil.PrivateDir(t), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	engine, err := tasks.New(context.Background(), store, tasks.Config{Factory: func(string) (*agent.Agent, error) {
		t.Error("memory management invoked model factory")
		return nil, errors.New("forbidden")
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Close() })
	task, err := engine.Create(context.Background(), "Memory fixture")
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Config{Origin: "http://127.0.0.1:8080", Token: token, Backend: EngineBackend{engine}})
	if err != nil {
		t.Fatal(err)
	}
	return h, store, task.ID
}

const memoryCreate = `{"action":"create","confirm":true,"non_sensitive":true,"fact":{"content":"Use examples","source_ref":"Typed by owner","kind":"preference","confidence":1,"confidentiality":"private"}}`

func TestMemoryHTTPAuthorization(t *testing.T) {
	h, store, id := memoryServer(t)
	path := "/api/memory?task_id=" + id
	for _, tc := range []struct {
		path, body, origin string
		auth               bool
		status             int
	}{
		{path, memoryCreate, "", false, 401}, {path, memoryCreate, "", true, 403}, {path, memoryCreate, "https://evil.example", true, 403},
		{"/api/memory", memoryCreate, "http://127.0.0.1:8080", true, 400},
		{path + "&global=true", memoryCreate, "http://127.0.0.1:8080", true, 400},
		{path + "&task_id=other", memoryCreate, "http://127.0.0.1:8080", true, 400},
		{path, strings.Replace(memoryCreate, `"confirm":true`, `"confirm":false`, 1), "http://127.0.0.1:8080", true, 400},
		{path, strings.Replace(memoryCreate, `"non_sensitive":true`, `"non_sensitive":false`, 1), "http://127.0.0.1:8080", true, 400},
		{path, strings.Replace(memoryCreate, `"source_ref":`, `"approval_ref":"forged","source_ref":`, 1), "http://127.0.0.1:8080", true, 400},
		{"/api/memory?task_id=missing", memoryCreate, "http://127.0.0.1:8080", true, 404},
	} {
		w := request(h, "POST", tc.path, tc.body, tc.auth, tc.origin)
		if w.Code != tc.status {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body)
		}
	}
	records, err := store.ListMemory(context.Background(), session.MemoryScope{TaskID: id})
	if err != nil || len(records) != 0 {
		t.Fatal(records, err)
	}
	for _, asset := range []string{"/memory", "/memory.js", "/api/memory?global=true"} {
		if w := request(h, "GET", asset, "", false, ""); w.Code != 401 {
			t.Fatal(asset, w.Code)
		}
	}
}
func TestMemoryHTTPCRUDScopesAndRevisions(t *testing.T) {
	h, store, id := memoryServer(t)
	path := "/api/memory?task_id=" + id
	origin := "http://127.0.0.1:8080"
	w := request(h, "POST", path, memoryCreate, true, origin)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var record session.MemoryRecord
	if err := json.Unmarshal(w.Body.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(record.ApprovalRef, "web-owner:") || record.Revision != 1 || record.Scope.TaskID != id {
		t.Fatal(record)
	}
	global := request(h, "GET", "/api/memory?global=true", "", true, "")
	if global.Code != 200 || strings.TrimSpace(global.Body.String()) != "[]" {
		t.Fatal(global.Code, global.Body)
	}
	edit := strings.Replace(memoryCreate, `"action":"create"`, fmt.Sprintf(`"action":"edit","id":%q,"revision":1`, record.ID), 1)
	w = request(h, "POST", path, edit, true, origin)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var edited session.MemoryRecord
	json.Unmarshal(w.Body.Bytes(), &edited)
	if edited.Revision != 2 || edited.ApprovalRef == record.ApprovalRef {
		t.Fatal(edited)
	}
	if w = request(h, "POST", path, edit, true, origin); w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
	del := fmt.Sprintf(`{"action":"delete","id":%q,"revision":2,"confirm":true}`, record.ID)
	if w = request(h, "POST", "/api/memory?global=true", del, true, origin); w.Code != 404 {
		t.Fatal(w.Code, w.Body)
	}
	history := request(h, "GET", path+"&id="+record.ID, "", true, "")
	var versions []session.MemoryRecord
	json.Unmarshal(history.Body.Bytes(), &versions)
	if history.Code != 200 || len(versions) != 2 {
		t.Fatal(history.Code, history.Body)
	}
	if w = request(h, "POST", path, strings.Replace(del, `"revision":2`, `"revision":1`, 1), true, origin); w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
	if w = request(h, "POST", path, del, true, origin); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if _, err := store.MemoryHistory(context.Background(), session.MemoryScope{TaskID: id}, record.ID); !errors.Is(err, session.ErrMemoryNotFound) {
		t.Fatal(err)
	}
	if w = request(h, "GET", path, "", true, ""); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal(w.Code, w.Body)
	}
	// No model selection field is accepted by ordinary submissions.
	if w = request(h, "POST", "/api/tasks/"+id+"/messages", `{"prompt":"hello","memory_ids":["anything"]}`, true, origin); w.Code != 400 {
		t.Fatal(w.Code, w.Body)
	}
}
