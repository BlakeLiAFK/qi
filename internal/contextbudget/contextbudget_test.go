package contextbudget

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"gopi/internal/agent"
)

func callMessage() agent.Message {
	return agent.Message{Role: "assistant", ToolCalls: []agent.ToolCall{{ID: "a", Name: "lookup", Arguments: json.RawMessage(`{"q":"a\\b"}`)}, {ID: "b", Name: "lookup", Arguments: json.RawMessage(`{}`)}}}
}
func example() []agent.Message {
	return []agent.Message{
		{Role: "system", Content: "Permanent instructions"},
		{Role: "user", Content: "old prompt"}, callMessage(),
		{Role: "tool", ToolCallID: "b", Content: "second"},
		{Role: "tool", ToolCallID: "a", Content: "first"},
		{Role: "assistant", Content: "old answer"},
		{Role: "developer", Content: "Also permanent"},
		{Role: "user", Content: "latest prompt"},
		{Role: "assistant", Content: "latest answer"},
	}
}
func TestProjectEvictsWholeTurnsAndPinsInstructions(t *testing.T) {
	history := example()
	wanted := []agent.Message{history[0], history[6], history[7], history[8]}
	minimum, err := Project(wanted, nil, Config{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Project(history, nil, Config{MaxInputBytes: minimum.InputBytes})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Messages, wanted) {
		t.Fatalf("messages = %#v", got.Messages)
	}
	if got.DroppedMessages != 5 || got.InputBytes != minimum.InputBytes {
		t.Fatalf("projection = %#v", got)
	}
	if err := validate(got.Messages); err != nil {
		t.Fatal(err)
	}
}
func TestProjectKeepsNewestContiguousTurns(t *testing.T) {
	history := []agent.Message{{Role: "user", Content: "first"}, {Role: "assistant", Content: "one"}, {Role: "user", Content: "second"}, {Role: "assistant", Content: "two"}, {Role: "user", Content: "third"}}
	wanted, _ := Project(history[2:], nil, Config{})
	got, err := Project(history, nil, Config{MaxInputBytes: wanted.InputBytes})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Messages, history[2:]) {
		t.Fatalf("messages = %#v", got.Messages)
	}
}
func TestLatestToolTurnIsIndivisible(t *testing.T) {
	history := []agent.Message{{Role: "user", Content: "current"}, callMessage(), {Role: "tool", ToolCallID: "a", Content: strings.Repeat("large", 100)}, {Role: "tool", ToolCallID: "b", Content: "result"}}
	full, err := Project(history, nil, Config{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Project(history, nil, Config{MaxInputBytes: full.InputBytes - 1})
	var budgetErr *BudgetError
	if !errors.Is(err, ErrTooLarge) || !errors.As(err, &budgetErr) {
		t.Fatalf("error = %v", err)
	}
	if budgetErr.RequiredBytes != full.InputBytes || got.Messages != nil {
		t.Fatalf("partial projection or wrong required size: %#v %v", got, err)
	}
}
func TestProjectOwnsCopiesAndDoesNotMutateHistory(t *testing.T) {
	history := example()
	before, _ := json.Marshal(history)
	got, err := Project(history, nil, Config{})
	if err != nil {
		t.Fatal(err)
	}
	got.Messages[0].Content = "changed"
	got.Messages[2].ToolCalls[0].Name = "changed"
	got.Messages[2].ToolCalls[0].Arguments[0] = '!'
	after, _ := json.Marshal(history)
	if string(before) != string(after) {
		t.Fatal("projection mutated source")
	}
}
func TestSizingIncludesSchemasArgumentsEscapesAndUnicode(t *testing.T) {
	messages := []agent.Message{{Role: "user", Content: "你好 <>&\n"}, {Role: "assistant", ToolCalls: []agent.ToolCall{{ID: "a", Name: "lookup", Arguments: json.RawMessage(`{"q":"x"}`)}}}, {Role: "tool", ToolCallID: "a", Content: "ok"}}
	defs := []agent.ToolDefinition{{Name: "lookup", Description: "desc", Parameters: json.RawMessage(`{"type":"object"}`)}}
	// This representation is independent of the sizing helper and matches the
	// provider's wire envelope with model metadata excluded.
	raw, err := json.Marshal(map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "你好 <>&\n"},
			map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"id": "a", "type": "function", "function": map[string]any{"name": "lookup", "arguments": `{"q":"x"}`}}}},
			map[string]any{"role": "tool", "content": "ok", "tool_call_id": "a"},
		},
		"tools": []any{map[string]any{"type": "function", "function": defs[0]}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		got, err := Project(messages, defs, Config{MaxInputBytes: len(raw)})
		if err != nil || got.InputBytes != len(raw) {
			t.Fatalf("size = %d, want %d, err %v", got.InputBytes, len(raw), err)
		}
	}
	if _, err := Project(messages, defs, Config{MaxInputBytes: len(raw) - 1}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("boundary error = %v", err)
	}
}
func TestInvalidHistories(t *testing.T) {
	tests := map[string][]agent.Message{
		"orphan":             {{Role: "tool", ToolCallID: "x"}},
		"unfinished":         {callMessage()},
		"missing one result": {callMessage(), {Role: "tool", ToolCallID: "a"}},
		"interleaved":        {callMessage(), {Role: "user", Content: "new"}},
		"duplicate result":   {callMessage(), {Role: "tool", ToolCallID: "a"}, {Role: "tool", ToolCallID: "a"}},
		"duplicate ID":       {{Role: "assistant", ToolCalls: []agent.ToolCall{{ID: "x"}, {ID: "x"}}}},
		"empty ID":           {{Role: "assistant", ToolCalls: []agent.ToolCall{{ID: ""}}}},
		"wrong role":         {{Role: "user", ToolCalls: []agent.ToolCall{{ID: "x"}}}},
		"unknown role":       {{Role: "unknown"}},
		"wrong result role":  {{Role: "assistant", ToolCallID: "x"}},
	}
	for name, history := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Project(history, nil, Config{MaxInputBytes: 1}); !errors.Is(err, ErrInvalidHistory) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
func TestEmptyAndPinnedOnly(t *testing.T) {
	got, err := Project(nil, nil, Config{})
	if err != nil || got.InputBytes != len(`{"messages":[]}`) {
		t.Fatalf("got %#v, %v", got, err)
	}
	if _, err := Project([]agent.Message{{Role: "system", Content: "pinned"}}, nil, Config{MaxInputBytes: 1}); !errors.Is(err, ErrTooLarge) {
		t.Fatal(err)
	}
	if _, err := Project(nil, nil, Config{MaxInputBytes: -1}); !errors.Is(err, ErrInvalidBudget) {
		t.Fatal(err)
	}
	if _, err := Project(nil, []agent.ToolDefinition{{Parameters: json.RawMessage(`broken`)}}, Config{}); err == nil {
		t.Fatal("accepted malformed schema")
	}
}

type recordingModel struct {
	calls    int
	received []agent.Message
}

func (m *recordingModel) Complete(_ context.Context, messages []agent.Message, _ []agent.ToolDefinition) (agent.Message, error) {
	m.calls++
	m.received = messages
	return agent.Message{Role: "assistant", Content: "new answer"}, nil
}
func TestModelWrapperPreservesAgentTranscript(t *testing.T) {
	history := example()
	minimal, _ := Project([]agent.Message{history[0], history[6], history[7], history[8]}, nil, Config{})
	next := &recordingModel{}
	a := agent.Agent{Model: &Model{Next: next, Config: Config{MaxInputBytes: minimal.InputBytes}}}
	transcript, err := a.Run(context.Background(), history)
	if err != nil {
		t.Fatal(err)
	}
	if next.calls != 1 || len(next.received) != 4 {
		t.Fatalf("model calls=%d, messages=%d", next.calls, len(next.received))
	}
	if len(transcript) != len(history)+1 || !reflect.DeepEqual(transcript[:len(history)], history) {
		t.Fatal("agent lost durable history")
	}
}
func TestWrapperDoesNotDispatchOnFailureOrCancellation(t *testing.T) {
	next := &recordingModel{}
	model := &Model{Next: next, Config: Config{MaxInputBytes: 1}}
	if _, err := model.Complete(context.Background(), nil, nil); !errors.Is(err, ErrTooLarge) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := model.Complete(ctx, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if next.calls != 0 {
		t.Fatal("dispatched failed projection")
	}
	if _, err := (&Model{}).Complete(context.Background(), nil, nil); err == nil {
		t.Fatal("accepted nil model")
	}
}
