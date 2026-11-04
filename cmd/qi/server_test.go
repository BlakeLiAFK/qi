package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"gopi/internal/testutil"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopi/internal/agent"
	"gopi/internal/tasks"
)

const testToken = "test-only-token-not-a-secret-1234567890"

func TestDaemonPersistsIsolatedTasks(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []agent.Message `json:"messages"`
			Tools    []any           `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if len(request.Tools) != 0 {
			t.Error("daemon must not expose tools")
		}
		first := request.Messages[0].Content
		for _, m := range request.Messages {
			if m.Role == "user" && m.Content != first {
				t.Error("cross-task history leaked")
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": "reply: " + first}}}})
	}))
	defer provider.Close()
	cfg := daemonConfig{database: filepath.Join(testutil.PrivateDir(t), "tasks.db"), model: "fixture", baseURL: provider.URL, origin: "http://127.0.0.1:8686", token: testToken, maxSteps: 3}
	handler, closeAll, err := openDaemon(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			closeAll()
		}
	}()
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, cfg.origin+path, strings.NewReader(body))
		req.SetBasicAuth("qi", testToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", cfg.origin)
		req.Header.Set("X-Qi-Request", "1")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code < 200 || w.Code >= 300 {
			t.Fatalf("%s %s returned %d: %s", method, path, w.Code, w.Body.String())
		}
		return w
	}
	ids := []string{}
	for _, prompt := range []string{"alpha", "beta"} {
		var task tasks.Task
		if err = json.Unmarshal(call("POST", "/api/tasks", `{"title":"`+prompt+`"}`).Body.Bytes(), &task); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, task.ID)
		call("POST", "/api/tasks/"+task.ID+"/messages", `{"prompt":"`+prompt+`"}`)
	}
	for _, id := range ids {
		deadline := time.Now().Add(5 * time.Second)
		for {
			var task tasks.Task
			_ = json.Unmarshal(call("GET", "/api/tasks/"+id, "").Body.Bytes(), &task)
			if task.Status == "completed" {
				if len(task.Messages) != 2 {
					t.Fatalf("messages: %+v", task.Messages)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("task did not complete: %+v", task)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	closeAll()
	closed = true
	handler, closeAll, err = openDaemon(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	closed = false
	for _, id := range ids {
		var task tasks.Task
		_ = json.Unmarshal(call("GET", "/api/tasks/"+id, "").Body.Bytes(), &task)
		if task.Status != "completed" || len(task.Messages) != 2 {
			t.Fatalf("restart lost task: %+v", task)
		}
	}
	req := httptest.NewRequest("GET", cfg.origin+"/api/tasks", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("unauthenticated status=%d", w.Code)
	}
	req = httptest.NewRequest("GET", cfg.origin+"/api/tasks", nil)
	req.SetBasicAuth("qi", testToken)
	req.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatalf("cross-origin status=%d", w.Code)
	}
}

func TestServerValidation(t *testing.T) {
	for _, address := range []string{"0.0.0.0:8686", ":8686", "localhost:8686", "192.168.1.2:8686"} {
		if loopbackAddress(address) == nil {
			t.Errorf("accepted %s", address)
		}
	}
	for _, address := range []string{"127.0.0.1:0", "[::1]:8686"} {
		if err := loopbackAddress(address); err != nil {
			t.Error(err)
		}
	}
	for _, args := range [][]string{{"serve", "-token", "never-allowed"}, {"serve", "-enable-workers"}, {"serve", "-model", "fixture"}} {
		var b bytes.Buffer
		if err := run(context.Background(), args, strings.NewReader(""), &b, &b, func(string) string { return "" }); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	var b bytes.Buffer
	if err := run(context.Background(), []string{"serve", "-h"}, strings.NewReader(""), &b, &b, func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "QI_WEB_TOKEN") {
		t.Fatal("help missing auth instructions")
	}
}

type readyWriter struct {
	io.Writer
	ready chan struct{}
}

func (w readyWriter) Write(b []byte) (int, error) {
	n, err := w.Writer.Write(b)
	select {
	case w.ready <- struct{}{}:
	default:
	}
	return n, err
}
func TestServeShutsDown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output bytes.Buffer
	ready := make(chan struct{}, 1)
	done := make(chan error, 1)
	env := func(k string) string {
		if k == "QI_WEB_TOKEN" {
			return testToken
		}
		return ""
	}
	go func() {
		done <- runServer(ctx, []string{"-listen", "127.0.0.1:0", "-db", filepath.Join(testutil.PrivateDir(t), "tasks.db"), "-model", "fixture"}, readyWriter{&output, ready}, env)
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("exited before ready: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("startup timed out")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timed out")
	}
	if strings.Contains(output.String(), testToken) {
		t.Fatal("token was printed")
	}
	if !strings.Contains(output.String(), "qi WebUI: http://127.0.0.1:") {
		t.Fatal(fmt.Sprint("missing listening URL: ", output.String()))
	}
}

func TestServerCodingRequiresExplicitWorkspace(t *testing.T) {
	for _, args := range [][]string{
		{"-enable-workers"},
		{"-work-dir", testutil.PrivateDir(t)},
		{"-enable-workers", "-work-dir", testutil.PrivateDir(t), "-worker-timeout", "2h"},
	} {
		var out bytes.Buffer
		args = append(args, "-model", "fixture")
		if err := runServer(context.Background(), args, &out, func(string) string { return testToken }); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
