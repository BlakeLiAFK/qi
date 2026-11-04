package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gopi/internal/agent"
	"gopi/internal/tasks"
)

func TestContextBudgetEnvironment(t *testing.T) {
	for _, raw := range []string{"-1", "0", "one", "9999999999999999999999999"} {
		if _, err := contextInputBudget(func(string) string { return raw }); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	for _, tc := range []struct {
		raw  string
		want int
	}{{"", 262144}, {"4096", 4096}} {
		got, err := contextInputBudget(func(string) string { return tc.raw })
		if err != nil || got != tc.want {
			t.Fatalf("got=%d err=%v", got, err)
		}
	}
}
func TestCLIContextBudgetBlocksProvider(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer provider.Close()
	env := func(k string) string {
		if k == "QI_MAX_CONTEXT_BYTES" {
			return "1"
		}
		return ""
	}
	var b bytes.Buffer
	err := run(context.Background(), []string{"-model", "fixture", "-base-url", provider.URL, "-p", "hello"}, strings.NewReader(""), &b, &b, env)
	if err == nil || !strings.Contains(err.Error(), "context exceeds") {
		t.Fatalf("expected budget error, got %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("provider received over-budget request")
	}
}
func TestDaemonContextBudgetPreservesStoredPrompt(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer provider.Close()
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()
	h, closeAll, err := openDaemon(context.Background(), daemonConfig{database: filepath.Join(t.TempDir(), "tasks.db"), model: "fixture", baseURL: provider.URL, origin: origin, token: testToken, contextBytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	server.Config.Handler = h
	server.Start()
	defer server.Close()
	c, err := newDaemonClient(origin, testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer c.http.CloseIdleConnections()
	var task tasks.Task
	if err = c.request(context.Background(), "POST", "/api/tasks", map[string]string{"title": "budget test"}, &task); err != nil {
		t.Fatal(err)
	}
	if err = c.request(context.Background(), "POST", "/api/tasks/"+task.ID+"/messages", map[string]string{"prompt": "keep this full prompt"}, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		current, err := c.get(context.Background(), task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status == "failed" {
			if len(current.Messages) != 1 || current.Messages[0].Content != "keep this full prompt" {
				t.Fatal("durable history was changed")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected failed run, got %s", current.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if calls.Load() != 0 {
		t.Fatal("provider received over-budget request")
	}
}

func TestCLIReportsContextTrimming(t *testing.T) {
	calls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []agent.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		calls++
		if len(req.Messages) != 2 {
			t.Errorf("request %d got %d messages", calls, len(req.Messages))
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer provider.Close()
	var out, diag bytes.Buffer
	env := func(k string) string {
		if k == "QI_MAX_CONTEXT_BYTES" {
			return "650"
		}
		return ""
	}
	input := strings.Repeat("a", 200) + "\n" + strings.Repeat("b", 200) + "\n/quit\n"
	if err := run(context.Background(), []string{"-model", "fixture", "-base-url", provider.URL}, strings.NewReader(input), &out, &diag, env); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
	if !strings.Contains(diag.String(), "omitted 2 earlier messages") || !strings.Contains(diag.String(), "user instructions") || !strings.Contains(diag.String(), "history is unchanged") {
		t.Fatal(diag.String())
	}
	if strings.Contains(out.String(), "Context notice") {
		t.Fatal("notice should be diagnostic output")
	}
}

func TestAttachReportsSavedTrimNoticeOnce(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(tasks.Task{ID: "fixture", Status: "completed", ContextNotice: &tasks.ContextNotice{RunID: "run1", DroppedMessages: 2, InputBytes: 512}})
	}))
	defer server.Close()
	var out, diag bytes.Buffer
	err := runAttach(context.Background(), []string{"-url", server.URL, "-task", "fixture"}, strings.NewReader("/wait\n/wait\n/quit\n"), &out, &diag, func(string) string { return testToken })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(diag.String(), "Saved context notice") != 1 || !strings.Contains(diag.String(), "user instructions") {
		t.Fatal(diag.String())
	}
}
