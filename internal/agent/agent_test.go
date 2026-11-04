package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type modelFunc func(context.Context, []Message, []ToolDefinition) (Message, error)

func (f modelFunc) Complete(c context.Context, m []Message, d []ToolDefinition) (Message, error) {
	return f(c, m, d)
}

type testTool struct {
	calls int
	err   error
}

func (t *testTool) Definition() ToolDefinition {
	return ToolDefinition{Name: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (t *testTool) Execute(context.Context, json.RawMessage) (string, error) {
	t.calls++
	return "result", t.err
}
func TestLoop(t *testing.T) {
	tool := &testTool{}
	var events []string
	n := 0
	a := Agent{Tools: []Tool{tool}, OnEvent: func(e Event) { events = append(events, e.Type) }, Model: modelFunc(func(_ context.Context, m []Message, d []ToolDefinition) (Message, error) {
		n++
		if n == 1 {
			if len(d) != 1 {
				t.Fatal(d)
			}
			return Message{ToolCalls: []ToolCall{{ID: "c1", Name: "echo", Arguments: json.RawMessage(`{}`)}}}, nil
		}
		if len(m) != 3 || m[2].Role != "tool" || m[2].Content != "result" {
			t.Fatal(m)
		}
		return Message{Content: "done"}, nil
	})}
	history := []Message{{Role: "user", Content: "hello"}}
	got, err := a.Run(context.Background(), history)
	if err != nil || len(got) != 4 || tool.calls != 1 {
		t.Fatalf("%v %v %d", got, err, tool.calls)
	}
	if len(history) != 1 {
		t.Fatal("mutated input")
	}
	want := []string{"run_start", "message", "tool_start", "tool_end", "message", "run_end"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events %v", events)
	}
}
func TestToolFailureIsRecoverable(t *testing.T) {
	for _, name := range []string{"echo", "missing"} {
		t.Run(name, func(t *testing.T) {
			n := 0
			a := Agent{Tools: []Tool{&testTool{err: errors.New("failure")}}, Model: modelFunc(func(_ context.Context, m []Message, _ []ToolDefinition) (Message, error) {
				n++
				if n == 1 {
					return Message{ToolCalls: []ToolCall{{ID: "x", Name: name, Arguments: json.RawMessage(`{}`)}}}, nil
				}
				var payload map[string]string
				if json.Unmarshal([]byte(m[len(m)-1].Content), &payload) != nil || payload["error"] == "" {
					t.Fatal(m)
				}
				return Message{Content: "recovered"}, nil
			})}
			if _, err := a.Run(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestMaxSteps(t *testing.T) {
	tool := &testTool{}
	a := Agent{MaxSteps: 2, Tools: []Tool{tool}, Model: modelFunc(func(context.Context, []Message, []ToolDefinition) (Message, error) {
		return Message{ToolCalls: []ToolCall{{ID: "x", Name: "echo", Arguments: json.RawMessage(`{}`)}}}, nil
	})}
	_, err := a.Run(context.Background(), nil)
	if !errors.Is(err, ErrMaxSteps) || tool.calls != 2 {
		t.Fatalf("%v %d", err, tool.calls)
	}
}
func TestCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := Agent{Model: modelFunc(func(context.Context, []Message, []ToolDefinition) (Message, error) {
		t.Fatal("model called")
		return Message{}, nil
	})}
	if _, err := a.Run(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestDuplicateCallsPreventExecution(t *testing.T) {
	tool := &testTool{}
	a := Agent{Tools: []Tool{tool}, Model: modelFunc(func(context.Context, []Message, []ToolDefinition) (Message, error) {
		c := ToolCall{ID: "x", Name: "echo", Arguments: json.RawMessage(`{}`)}
		return Message{ToolCalls: []ToolCall{c, c}}, nil
	})}
	if _, err := a.Run(context.Background(), nil); err == nil || tool.calls != 0 {
		t.Fatalf("%v %d", err, tool.calls)
	}
}
func TestDuplicateTools(t *testing.T) {
	tool := &testTool{}
	a := Agent{Tools: []Tool{tool, tool}, Model: modelFunc(func(context.Context, []Message, []ToolDefinition) (Message, error) {
		t.Fatal("model called")
		return Message{}, nil
	})}
	if _, err := a.Run(context.Background(), nil); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestInvalidArgumentsDoNotExecute(t *testing.T) {
	tool := &testTool{}
	calls := 0
	a := Agent{Tools: []Tool{tool}, Model: modelFunc(func(_ context.Context, m []Message, _ []ToolDefinition) (Message, error) {
		calls++
		if calls == 1 {
			return Message{ToolCalls: []ToolCall{{ID: "bad", Name: "echo", Arguments: json.RawMessage(`not-json`)}}}, nil
		}
		if m[len(m)-1].Role != "tool" {
			t.Fatal("missing error result")
		}
		return Message{Content: "fixed"}, nil
	})}
	if _, err := a.Run(context.Background(), nil); err != nil || tool.calls != 0 {
		t.Fatalf("%v, calls=%d", err, tool.calls)
	}
}

func TestModelFailurePreservesHistory(t *testing.T) {
	expected := errors.New("provider unavailable")
	a := Agent{Model: modelFunc(func(context.Context, []Message, []ToolDefinition) (Message, error) { return Message{}, expected })}
	got, err := a.Run(context.Background(), []Message{{Role: "user", Content: "hello"}})
	if !errors.Is(err, expected) || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
}

func TestToolStartCancellationPreventsExecution(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tool := &testTool{}
	a := Agent{Tools: []Tool{tool}, OnEvent: func(e Event) {
		if e.Type == "tool_start" {
			cancel()
		}
	}, Model: modelFunc(func(context.Context, []Message, []ToolDefinition) (Message, error) {
		return Message{ToolCalls: []ToolCall{{ID: "c1", Name: "echo", Arguments: json.RawMessage(`{}`)}}}, nil
	})}
	if _, err := a.Run(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if tool.calls != 0 {
		t.Fatal("tool executed after persistence callback cancellation")
	}
}

func TestUnknownOutcomeStopsBeforeRetryAndRemainingBatch(t *testing.T) {
	for _, cause := range []error{context.DeadlineExceeded, context.Canceled, ErrOutcomeUnknown} {
		t.Run(cause.Error(), func(t *testing.T) {
			tool := &testTool{err: cause}
			models := 0
			events := []string{}
			a := Agent{Tools: []Tool{tool}, OnEvent: func(e Event) { events = append(events, e.Type) }, Model: modelFunc(func(context.Context, []Message, []ToolDefinition) (Message, error) {
				models++
				return Message{ToolCalls: []ToolCall{{ID: "1", Name: "echo", Arguments: json.RawMessage(`{}`)}, {ID: "2", Name: "echo", Arguments: json.RawMessage(`{}`)}}}, nil
			})}
			messages, err := a.Run(context.Background(), nil)
			if !errors.Is(err, ErrOutcomeUnknown) || tool.calls != 1 || models != 1 {
				t.Fatalf("err=%v calls=%d models=%d", err, tool.calls, models)
			}
			if len(messages) != 2 || messages[1].Role != "tool" || !strings.Contains(messages[1].Content, "outcome unknown") {
				t.Fatalf("unknown result not persisted: %v", messages)
			}
			if !reflect.DeepEqual(events, []string{"run_start", "message", "tool_start", "tool_end", "error"}) {
				t.Fatal(events)
			}
		})
	}
}

func TestObjectArgumentsAndBatchLimits(t *testing.T) {
	for _, args := range []string{`null`, `[]`, `1`, `"text"`, `{"v":"` + strings.Repeat("x", MaxToolBytes) + `"}`} {
		tool := &testTool{}
		a := Agent{Tools: []Tool{tool}, MaxSteps: 1, Model: modelFunc(func(context.Context, []Message, []ToolDefinition) (Message, error) {
			return Message{ToolCalls: []ToolCall{{ID: "1", Name: "echo", Arguments: json.RawMessage(args)}}}, nil
		})}
		_, _ = a.Run(context.Background(), nil)
		if tool.calls != 0 {
			t.Fatal("executed invalid arguments")
		}
	}
	tool := &testTool{}
	a := Agent{Tools: []Tool{tool}, Model: modelFunc(func(context.Context, []Message, []ToolDefinition) (Message, error) {
		return Message{ToolCalls: make([]ToolCall, MaxToolCalls+1)}, nil
	})}
	if _, err := a.Run(context.Background(), nil); err == nil || tool.calls != 0 {
		t.Fatal("batch limit not enforced")
	}
}
