package main

import (
	"bytes"
	"context"
	"encoding/json"
	"gopi/internal/testutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"gopi/internal/agent"
	"gopi/internal/tasks"
)

func TestAttachSharesDaemonHistory(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []agent.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": "reply " + req.Messages[len(req.Messages)-1].Content}}}})
	}))
	defer provider.Close()
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()
	handler, closeAll, err := openDaemon(context.Background(), daemonConfig{database: filepath.Join(testutil.PrivateDir(t), "tasks.db"), model: "fixture", baseURL: provider.URL, origin: origin, token: testToken, maxSteps: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	server.Config.Handler = handler
	server.Start()
	defer server.Close()
	env := func(k string) string {
		if k == "QI_WEB_TOKEN" {
			return testToken
		}
		return ""
	}
	var out, diag bytes.Buffer
	if err = runAttach(context.Background(), []string{"-url", origin, "-title", "Shared task", "-p", "first"}, strings.NewReader(""), &out, &diag, env); err != nil {
		t.Fatal(err)
	}
	if out.String() != "reply first\n" {
		t.Fatal(out.String())
	}
	client, err := newDaemonClient(origin, testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer client.http.CloseIdleConnections()
	var items []tasks.Task
	if err = client.request(context.Background(), "GET", "/api/tasks", nil, &items); err != nil || len(items) != 1 {
		t.Fatalf("list: %+v %v", items, err)
	}
	id := items[0].ID
	out.Reset()
	diag.Reset()
	if err = run(context.Background(), []string{"attach", "-url", origin, "-task", id}, strings.NewReader("second\n/history\n/quit\n"), &out, &diag, env); err != nil {
		t.Fatal(err)
	}
	task, err := client.get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(task.Messages) != 4 || task.Messages[0].Content != "first" || task.Messages[2].Content != "second" {
		t.Fatalf("shared history = %+v", task.Messages)
	}
	if !strings.Contains(out.String(), "You: first") || !strings.Contains(out.String(), "reply second") {
		t.Fatal(out.String())
	}
	out.Reset()
	diag.Reset()
	if err = runAttach(context.Background(), []string{"-url", origin, "-list"}, strings.NewReader(""), &out, &diag, env); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), id) || !strings.Contains(out.String(), "Shared task") {
		t.Fatal(out.String())
	}
	if strings.Contains(out.String()+diag.String(), testToken) {
		t.Fatal("token leaked")
	}
}
func TestDaemonClientRejectsUnsafeOriginsAndRedirects(t *testing.T) {
	for _, origin := range []string{"http://example.com", "http://localhost:8686", "http://user:password@127.0.0.1:8686", "http://127.0.0.1:8686/", "http://127.0.0.1:8686?token=bad", "file:///tmp/test"} {
		if _, err := newDaemonClient(origin, testToken); err == nil {
			t.Errorf("accepted %s", origin)
		}
	}
	visited := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { visited = true }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer redirect.Close()
	c, err := newDaemonClient(redirect.URL, testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer c.http.CloseIdleConnections()
	err = c.request(context.Background(), "GET", "/api/tasks", nil, nil)
	if err == nil || visited {
		t.Fatalf("redirect followed: visited=%v err=%v", visited, err)
	}
}
func TestAttachAuthAndCancellation(t *testing.T) {
	cancelled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, token, ok := r.BasicAuth()
		if !ok || user != "qi" || token != testToken {
			t.Error("missing auth")
		}
		if r.Method == "POST" {
			if r.Header.Get("Origin") != "http://"+r.Host || r.Header.Get("X-Qi-Request") != "1" {
				t.Error("missing CSRF headers")
			}
			cancelled = true
			_, _ = w.Write([]byte(`{"accepted":true}`))
			return
		}
		_ = json.NewEncoder(w).Encode(tasks.Task{ID: "example", Title: "Example", Status: "running"})
	}))
	defer server.Close()
	var b bytes.Buffer
	err := runAttach(context.Background(), []string{"-url", server.URL, "-task", "example"}, strings.NewReader("/cancel\n/quit\n"), &b, &b, func(string) string { return testToken })
	if err != nil || !cancelled {
		t.Fatalf("cancelled=%v err=%v", cancelled, err)
	}
}

func TestAttachPendingApprovalDoesNotClaimCompletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			_, _ = w.Write([]byte(`{"accepted":true}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/approvals") {
			_ = json.NewEncoder(w).Encode([]tasks.Approval{{ID: "approval", Status: "pending", ExpiresAt: time.Now().Add(time.Minute), Action: tasks.ApprovalAction{Kind: "coding.run", Target: "/test/codex"}}})
			return
		}
		_ = json.NewEncoder(w).Encode(tasks.Task{ID: "example", Title: "Example", Status: "running"})
	}))
	defer server.Close()
	var out, diag bytes.Buffer
	err := runAttach(context.Background(), []string{"-url", server.URL, "-task", "example", "-p", "do work"}, strings.NewReader(""), &out, &diag, func(string) string { return testToken })
	if err != errApprovalPending {
		t.Fatalf("expected pending, got %v", err)
	}
	if !strings.Contains(diag.String(), "Review the full action in the WebUI") {
		t.Fatal(diag.String())
	}
}

func TestDaemonCodingApprovalHTTPBoundary(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "ambient-model-fixture-must-not-leak")
	t.Setenv("QI_WEB_TOKEN", "ambient-daemon-fixture-must-not-leak")
	t.Setenv("CODEX_API_KEY", "ambient-codex-fixture-must-not-leak")
	if runtime.GOOS == "windows" {
		t.Skip("Unix script fixture")
	}
	workspace := testutil.PrivateDir(t)
	executable := filepath.Join(workspace, "fake-codex")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n[ \"$CODEX_API_KEY\" = fixture-codex-key ] || exit 21\n[ -z \"$OPENAI_API_KEY\" ] || exit 22\n[ -z \"$QI_WEB_TOKEN\" ] || exit 23\nprintf done > dispatched\nprintf fixture-output\n"), 0700); err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []agent.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.Messages[len(req.Messages)-1].Role == "tool" {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"coding1","type":"function","function":{"name":"coding_run","arguments":"{\"agent\":\"codex\",\"prompt\":\"fixture only\"}"}}]}}]}`))
	}))
	defer provider.Close()
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()
	handler, closeAll, err := openDaemon(context.Background(), daemonConfig{database: filepath.Join(testutil.PrivateDir(t), "tasks.db"), model: "fixture", baseURL: provider.URL, origin: origin, token: testToken, maxSteps: 3, coding: &tasks.CodingConfig{Workspace: workspace, CodexPath: executable, Timeout: time.Second, Credentials: workerCredentials(func(key string) string {
		if key == "QI_CODEX_API_KEY" {
			return "fixture-codex-key"
		}
		return ""
	})}})
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
	if err = c.request(context.Background(), "POST", "/api/tasks", map[string]string{"title": "coding fixture"}, &task); err != nil {
		t.Fatal(err)
	}
	if err = c.request(context.Background(), "POST", "/api/tasks/"+task.ID+"/messages", map[string]string{"prompt": "test coding fixture"}, nil); err != nil {
		t.Fatal(err)
	}
	var approval tasks.Approval
	deadline := time.Now().Add(5 * time.Second)
	for {
		var list []tasks.Approval
		if err = c.request(context.Background(), "GET", "/api/tasks/"+task.ID+"/approvals", nil, &list); err != nil {
			t.Fatal(err)
		}
		if len(list) > 0 {
			approval = list[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("approval not created")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = os.Stat(filepath.Join(workspace, "dispatched")); !os.IsNotExist(err) {
		t.Fatalf("dispatched before approval: %v", err)
	}
	decision := tasks.ApprovalDecision{ID: approval.ID, Revision: approval.Revision, Digest: approval.Digest, Allow: true}
	if err = c.request(context.Background(), "POST", "/api/tasks/"+task.ID+"/approvals", decision, nil); err != nil {
		t.Fatal(err)
	}
	for {
		current, err := c.get(context.Background(), task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task did not complete: %+v", current)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = os.Stat(filepath.Join(workspace, "dispatched")); err != nil {
		t.Fatal(err)
	}
	if err = c.request(context.Background(), "POST", "/api/tasks/"+task.ID+"/approvals", decision, nil); err == nil {
		t.Fatal("replayed approval accepted")
	}
}
