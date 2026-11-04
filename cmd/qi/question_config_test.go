package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"gopi/internal/agent"
	"gopi/internal/tasks"
)

func TestDaemonQuestionsExplicitOptIn(t *testing.T) {
	observed := make(chan []string, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tools []struct {
				Function agent.ToolDefinition `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		names := []string{}
		for _, tool := range req.Tools {
			names = append(names, tool.Function.Name)
		}
		observed <- names
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer provider.Close()
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()
	handler, closeAll, err := openDaemon(context.Background(), daemonConfig{database: filepath.Join(t.TempDir(), "tasks.db"), model: "fixture", baseURL: provider.URL, origin: origin, token: testToken, questions: true})
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	server.Config.Handler = handler
	server.Start()
	defer server.Close()
	c, err := newDaemonClient(origin, testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer c.http.CloseIdleConnections()
	var task tasks.Task
	if err = c.request(context.Background(), "POST", "/api/tasks", map[string]string{"title": "questions fixture"}, &task); err != nil {
		t.Fatal(err)
	}
	if err = c.request(context.Background(), "POST", "/api/tasks/"+task.ID+"/messages", map[string]string{"prompt": "hello"}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case names := <-observed:
		if len(names) != 2 {
			t.Fatalf("tools=%v", names)
		}
		found := map[string]bool{}
		for _, name := range names {
			found[name] = true
		}
		if !found["ask_user_question"] || !found["get_question_answers"] {
			t.Fatalf("tools=%v", names)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("model request missing")
	}
}
