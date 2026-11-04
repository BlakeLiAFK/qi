package main

import (
	"context"
	"encoding/json"
	"fmt"
	"gopi/internal/tasks"
	"gopi/internal/testutil"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDaemonModelStreamingOptInAndCanonicalCompletion(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Stream bool `json:"stream"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				if req.Stream != enabled {
					t.Errorf("stream=%v wanted %v", req.Stream, enabled)
				}
				if !req.Stream {
					fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"Hello world"}}]}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello \"}}]}\n\n")
				w.(http.Flusher).Flush()
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"world\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer model.Close()
			origin := "http://127.0.0.1:8686"
			handler, closeAll, err := openDaemon(context.Background(), daemonConfig{database: filepath.Join(testutil.PrivateDir(t), "tasks.db"), model: "fixture", baseURL: model.URL, origin: origin, token: testToken, maxSteps: 2, modelStream: enabled})
			if err != nil {
				t.Fatal(err)
			}
			defer closeAll()
			call := func(method, path, body string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, origin+path, strings.NewReader(body))
				r.SetBasicAuth("qi", testToken)
				r.Header.Set("Origin", origin)
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("X-Qi-Request", "1")
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code >= 400 {
					t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
				}
				return w
			}
			var created tasks.Task
			json.Unmarshal(call("POST", "/api/tasks", `{"title":"Stream test"}`).Body.Bytes(), &created)
			call("POST", "/api/tasks/"+created.ID+"/messages", `{"prompt":"Hello"}`)
			read := func() tasks.Task {
				var task tasks.Task
				json.Unmarshal(call("GET", "/api/tasks/"+created.ID, "").Body.Bytes(), &task)
				return task
			}
			if enabled {
				deadline := time.Now().Add(3 * time.Second)
				for {
					task := read()
					if task.LiveText != nil {
						if task.LiveText.Text != "Hello " || task.LiveText.TaskID != created.ID || len(task.Messages) != 1 {
							t.Fatalf("bad provisional task: %#v", task)
						}
						again := read()
						if again.LiveText == nil || again.LiveText.Text != "Hello " {
							t.Fatal("snapshot duplicated or lost")
						}
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("missing live snapshot")
					}
					time.Sleep(5 * time.Millisecond)
				}
			}
			unblock()
			deadline := time.Now().Add(3 * time.Second)
			for {
				task := read()
				if task.Status == "completed" {
					if task.LiveText != nil || len(task.Messages) != 2 || task.Messages[1].Content != "Hello world" {
						t.Fatalf("noncanonical completion: %#v", task)
					}
					break
				}
				if task.Status == "failed" || time.Now().After(deadline) {
					t.Fatalf("run did not complete: %#v", task)
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
}
