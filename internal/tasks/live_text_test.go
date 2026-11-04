package tasks

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func liveEvent(id int64, run, kind, delta string) Event {
	b, _ := json.Marshal(map[string]string{"type": kind, "delta": delta})
	return Event{ID: id, RunID: run, Type: kind, Payload: b}
}
func TestLiveTextSnapshotResetReplayAndCompletion(t *testing.T) {
	runs := []Run{{ID: "one", Status: "running"}, {ID: "old", Status: "completed"}}
	events := []Event{liveEvent(1, "old", "model_start", ""), liveEvent(2, "old", "model_delta", "old secret"), liveEvent(3, "one", "model_start", ""), liveEvent(4, "one", "model_delta", "Hello "), liveEvent(5, "one", "model_delta", "world")}
	for i := 0; i < 2; i++ {
		got := projectLiveText("task-one", events, runs)
		if got == nil || got.TaskID != "task-one" || got.RunID != "one" || got.RequestID != 3 || got.Revision != 5 || got.Text != "Hello world" {
			t.Fatalf("snapshot %#v", got)
		}
	}
	events = append(events, liveEvent(6, "one", "message", ""))
	if got := projectLiveText("task-one", events, runs); got != nil {
		t.Fatalf("final message duplicated: %#v", got)
	}
	events = append(events, liveEvent(7, "one", "model_start", ""), liveEvent(8, "one", "model_delta", "next request"))
	got := projectLiveText("task-one", events, runs)
	if got == nil || got.RequestID != 7 || got.Text != "next request" {
		t.Fatalf("new request %#v", got)
	}
	for _, kind := range []string{"error", "run_end", "message"} {
		terminal := append(append([]Event{}, events...), liveEvent(9, "one", kind, ""))
		if got := projectLiveText("task-one", terminal, runs); got != nil {
			t.Fatal(kind, got)
		}
	}
	for _, state := range []string{"completed", "cancelled", "interrupted", "failed", "queued"} {
		if got := projectLiveText("task-one", events, []Run{{ID: "one", Status: state}}); got != nil {
			t.Fatal(state, got)
		}
	}
}
func TestLiveTextBoundedUTF8AndNoPartialToolData(t *testing.T) {
	runs := []Run{{ID: "one", Status: "running"}}
	events := []Event{liveEvent(1, "one", "model_start", ""), liveEvent(2, "one", "model_delta", strings.Repeat("界", 30000))}
	got := projectLiveText("task", events, runs)
	if got == nil || !got.Truncated || len(got.Text) > MaxLiveTextBytes || !utf8.ValidString(got.Text) {
		t.Fatalf("invalid bounded snapshot %#v", got)
	}
	tools := []Event{{ID: 1, RunID: "one", Type: "tool_start", Payload: json.RawMessage(`{"tool_call":{"arguments":"PRIVATE"}}`)}, liveEvent(2, "one", "model_delta", "orphan")}
	if got := projectLiveText("task", tools, runs); got != nil {
		t.Fatal("partial tool or orphan delta exposed", got)
	}
	events = append(events, Event{ID: 3, RunID: "one", Type: "model_delta", Payload: json.RawMessage(`broken`)})
	if got := projectLiveText("task", events, runs); got != nil {
		t.Fatal("corrupt snapshot shown", got)
	}
}
