package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"gopi/internal/agent"
	"gopi/internal/tasks"
)

func TestAttachWaitsThroughQueuedState(t *testing.T) {
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			_, _ = w.Write([]byte(`{"accepted":true}`))
			return
		}
		n := reads.Add(1)
		status := "queued"
		content := "old answer"
		if n >= 3 {
			status = "completed"
			content = "new answer"
		}
		_ = json.NewEncoder(w).Encode(tasks.Task{ID: "example", Title: "Queued", Status: status, Messages: []agent.Message{{Role: "assistant", Content: content}}})
	}))
	defer server.Close()
	var out, diag bytes.Buffer
	err := runAttach(context.Background(), []string{"-url", server.URL, "-task", "example", "-p", "do work"}, strings.NewReader(""), &out, &diag, func(string) string { return testToken })
	if err != nil {
		t.Fatal(err)
	}
	if reads.Load() < 3 || !strings.Contains(out.String(), "new answer") || strings.Contains(out.String(), "old answer") {
		t.Fatalf("premature queued completion: reads=%d output=%q", reads.Load(), out.String())
	}
}
