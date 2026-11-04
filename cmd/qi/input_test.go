package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gopi/internal/agent"
	"gopi/internal/tasks"
)

type observedPipe struct {
	reader  io.ReadCloser
	closed  atomic.Int32
	entered chan struct{}
	once    sync.Once
}

func (p *observedPipe) Read(b []byte) (int, error) {
	p.once.Do(func() { close(p.entered) })
	return p.reader.Read(b)
}
func (p *observedPipe) Close() error { p.closed.Add(1); return p.reader.Close() }
func testPipe(t *testing.T) *observedPipe {
	t.Helper()
	r, w := io.Pipe()
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	return &observedPipe{reader: r, entered: make(chan struct{})}
}
func awaitBlockedRead(t *testing.T, p *observedPipe) {
	t.Helper()
	select {
	case <-p.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("input read did not begin")
	}
}
func awaitCancelled(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("input cancellation did not return")
	}
}

func TestStandaloneIdleInputCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := testPipe(t)
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"-model", "fixture"}, p, io.Discard, io.Discard, func(string) string { return "" })
	}()
	awaitBlockedRead(t, p)
	cancel()
	awaitCancelled(t, done)
}
func TestAttachedIdleInputCancelsWithoutCancellingTask(t *testing.T) {
	var taskCancels atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/cancel") {
			taskCancels.Add(1)
		}
		_ = json.NewEncoder(w).Encode(tasks.Task{ID: "fixture", Title: "Fixture", Status: "idle"})
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := testPipe(t)
	done := make(chan error, 1)
	go func() {
		done <- runAttach(ctx, []string{"-url", server.URL, "-task", "fixture"}, p, io.Discard, io.Discard, func(string) string { return testToken })
	}()
	awaitBlockedRead(t, p)
	cancel()
	awaitCancelled(t, done)
	if taskCancels.Load() != 0 {
		t.Fatal("disconnect cancelled daemon task")
	}
}
func TestApprovalInputCancelsWithoutExecutingTool(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := testPipe(t)
	input := newInputSession(ctx, p)
	defer input.close()
	var called atomic.Bool
	tool := approvedTool{Tool: localTool{agent.ToolDefinition{Name: "fixture", Parameters: json.RawMessage(`{}`)}, func(context.Context, json.RawMessage) (string, error) { called.Store(true); return "", nil }}, in: input, out: io.Discard}
	done := make(chan error, 1)
	go func() { _, err := tool.Execute(ctx, json.RawMessage(`{}`)); done <- err }()
	awaitBlockedRead(t, p)
	cancel()
	awaitCancelled(t, done)
	input.close()
	if called.Load() {
		t.Fatal("tool executed after cancelled approval")
	}
	select {
	case <-input.done:
	default:
		t.Fatal("input watcher did not exit")
	}
}
func TestInputWatcherIsJoinedOnNormalExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := testPipe(t)
	input := newInputSession(ctx, p)
	input.close()
	input.close()
	select {
	case <-input.done:
	default:
		t.Fatal("watcher remains after normal exit")
	}
	if p.closed.Load() != 0 {
		t.Fatal("normal lifecycle exit closed caller input")
	}
	cancel()
	if p.closed.Load() != 0 {
		t.Fatal("stopped watcher reacted to later cancellation")
	}
}

func TestOSStdinPipeCancels(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	p := &observedPipe{reader: r, entered: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"-model", "fixture"}, p, io.Discard, io.Discard, func(string) string { return "" })
	}()
	awaitBlockedRead(t, p)
	cancel()
	awaitCancelled(t, done)
	if p.closed.Load() != 1 {
		t.Fatalf("close count=%d", p.closed.Load())
	}
}
