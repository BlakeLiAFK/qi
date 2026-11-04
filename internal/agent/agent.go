// Package agent provides the small, provider-independent model/tool loop.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type Tool interface {
	Definition() ToolDefinition
	Execute(context.Context, json.RawMessage) (string, error)
}

type Model interface {
	Complete(context.Context, []Message, []ToolDefinition) (Message, error)
}

// EventModel optionally reports model-input diagnostics and provisional text progress through the same ordered
// event stream as the agent. Implementations must not include prompt contents in
// diagnostic events or retain the callback after CompleteWithEvents returns.
type EventModel interface {
	Model
	CompleteWithEvents(context.Context, []Message, []ToolDefinition, func(Event)) (Message, error)
}

// Event callbacks are synchronous and ordered. A callback must not modify the
// pointed-to values. Only message events represent whole messages. model_delta text is provisional
// and must never be added to history or interpreted as tool arguments.
type Event struct {
	Type            string    `json:"type"`
	Delta           string    `json:"delta,omitempty"`
	Message         *Message  `json:"message,omitempty"`
	ToolCall        *ToolCall `json:"tool_call,omitempty"`
	Error           string    `json:"error,omitempty"`
	DroppedMessages int       `json:"dropped_messages,omitempty"`
	InputBytes      int       `json:"input_bytes,omitempty"`
}

var ErrMaxSteps = errors.New("agent exceeded maximum model steps")

// ErrOutcomeUnknown means a dispatched tool may have performed its side effect.
// Callers must reconcile the actual outcome before retrying; it is not a
// recoverable model error. All tools are treated as potentially side-effectful.
var ErrOutcomeUnknown = errors.New("tool outcome unknown; verify external state before retrying")

const (
	MaxToolCalls = 128
	MaxTools     = 1024
	MaxToolBytes = 1024 * 1024
)

// IsJSONObject checks the required top-level shape, not full JSON Schema.
func IsJSONObject(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	return len(raw) <= MaxToolBytes && json.Unmarshal(raw, &object) == nil && object != nil
}

type Agent struct {
	Model    Model
	Tools    []Tool
	MaxSteps int
	OnEvent  func(Event)
}

func (a *Agent) emit(e Event) {
	if a.OnEvent != nil {
		a.OnEvent(e)
	}
}

// Run returns the full transcript even on failure, so callers can persist partial
// progress. Tools run serially in model order. Tool failures are returned to the
// model for recovery; cancellation and model failures stop the loop.
func (a *Agent) Run(ctx context.Context, history []Message) ([]Message, error) {
	messages := append([]Message(nil), history...)
	if a.Model == nil {
		return messages, errors.New("model is required")
	}
	limit := a.MaxSteps
	if limit <= 0 {
		limit = 20
	}
	if len(a.Tools) > MaxTools {
		return messages, errors.New("too many tools")
	}
	registry := make(map[string]Tool, len(a.Tools))
	definitions := make([]ToolDefinition, 0, len(a.Tools))
	for _, tool := range a.Tools {
		if tool == nil {
			return messages, errors.New("nil tool")
		}
		d := tool.Definition()
		if d.Name == "" || !IsJSONObject(d.Parameters) {
			return messages, fmt.Errorf("invalid tool definition %q", d.Name)
		}
		if _, exists := registry[d.Name]; exists {
			return messages, fmt.Errorf("duplicate tool %q", d.Name)
		}
		registry[d.Name] = tool
		definitions = append(definitions, d)
	}
	fail := func(err error) ([]Message, error) {
		a.emit(Event{Type: "error", Error: err.Error()})
		return messages, err
	}
	a.emit(Event{Type: "run_start"})
	for step := 0; step < limit; step++ {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		var response Message
		var err error
		if observable, ok := a.Model.(EventModel); ok {
			response, err = observable.CompleteWithEvents(ctx, messages, definitions, a.emit)
		} else {
			response, err = a.Model.Complete(ctx, messages, definitions)
		}
		if err != nil {
			return fail(fmt.Errorf("model: %w", err))
		}
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		response.Role = "assistant"
		messages = append(messages, response)
		a.emit(Event{Type: "message", Message: &response})
		if len(response.ToolCalls) == 0 {
			a.emit(Event{Type: "run_end"})
			return messages, nil
		}
		// Validate the complete batch before any side effects.
		if len(response.ToolCalls) > MaxToolCalls {
			return fail(errors.New("too many tool calls in one batch"))
		}
		ids := make(map[string]bool)
		for _, call := range response.ToolCalls {
			if call.ID == "" || ids[call.ID] {
				return fail(errors.New("model returned missing or duplicate tool call ID"))
			}
			ids[call.ID] = true
		}
		for _, call := range response.ToolCalls {
			if err := ctx.Err(); err != nil {
				return fail(err)
			}
			a.emit(Event{Type: "tool_start", ToolCall: &call})
			if err := ctx.Err(); err != nil {
				return fail(err)
			}
			var output string
			var toolErr error
			dispatched := false
			tool, found := registry[call.Name]
			switch {
			case !found:
				toolErr = fmt.Errorf("unknown tool %q", call.Name)
			case !IsJSONObject(call.Arguments):
				toolErr = errors.New("tool arguments must be a JSON object within size limit")
			default:
				dispatched = true
				output, toolErr = tool.Execute(ctx, call.Arguments)
			}
			// Preserve an explicit result even when cancellation races completion.
			// A timeout does not prove that a remote side effect was rolled back.
			if dispatched && (ctx.Err() != nil || errors.Is(toolErr, context.Canceled) || errors.Is(toolErr, context.DeadlineExceeded)) {
				toolErr = fmt.Errorf("%w: %w", ErrOutcomeUnknown, errors.Join(toolErr, ctx.Err()))
			}
			if len(output) > MaxToolBytes {
				output = ""
				toolErr = errors.Join(toolErr, errors.New("tool output exceeded size limit"))
			}
			event := Event{Type: "tool_end", ToolCall: &call}
			if toolErr != nil {
				event.Error = toolErr.Error()
				payload, _ := json.Marshal(map[string]string{"error": toolErr.Error(), "output": output})
				output = string(payload)
			}
			result := Message{Role: "tool", Content: output, ToolCallID: call.ID}
			messages = append(messages, result)
			event.Message = &result
			a.emit(event)
			if errors.Is(toolErr, ErrOutcomeUnknown) {
				return fail(toolErr)
			}
			if err := ctx.Err(); err != nil {
				return fail(err)
			}
		}
	}
	return fail(ErrMaxSteps)
}
