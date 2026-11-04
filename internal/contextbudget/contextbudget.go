// Package contextbudget projects a durable transcript into a bounded model input.
// Projection never edits the original history. Budgets count serialized context
// bytes, not tokens: provider token limits still need provider-specific handling.
package contextbudget

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"gopi/internal/agent"
)

const DefaultMaxInputBytes = 256 * 1024

var (
	ErrTooLarge       = errors.New("required context exceeds input budget")
	ErrInvalidHistory = errors.New("invalid tool-call history")
	ErrInvalidBudget  = errors.New("context budget must not be negative")
)

// Config bounds the serialized messages and tool definitions. Zero selects the
// default; negative values are invalid. Model/request metadata is not counted.
type Config struct{ MaxInputBytes int }

// BudgetError means pinned instructions and the most recent entire turn cannot
// fit. No content was truncated and no model request should be dispatched.
type BudgetError struct{ RequiredBytes, MaxInputBytes int }

func (e *BudgetError) Error() string {
	return fmt.Sprintf("%v: need %d bytes, budget %d", ErrTooLarge, e.RequiredBytes, e.MaxInputBytes)
}
func (e *BudgetError) Unwrap() error { return ErrTooLarge }

type Projection struct {
	Messages        []agent.Message
	InputBytes      int
	DroppedMessages int
}

// Model wraps a model without changing the agent's full, persistable history.
// Project is performed before every request, including tool-loop continuations.
type Model struct {
	Next   agent.Model
	Config Config
}

func (m *Model) Complete(ctx context.Context, messages []agent.Message, definitions []agent.ToolDefinition) (agent.Message, error) {
	if err := ctx.Err(); err != nil {
		return agent.Message{}, err
	}
	if m.Next == nil {
		return agent.Message{}, errors.New("context budget requires a model")
	}
	projection, err := Project(messages, definitions, m.Config)
	if err != nil {
		return agent.Message{}, err
	}
	return m.Next.Complete(ctx, projection.Messages, definitions)
}

// Project pins every system/developer message and keeps the entire latest user
// turn. Older turns are evicted oldest-first as complete units, including the
// user prompt and every assistant/tool message through the next user prompt.
// Instructions embedded in evicted user turns are not retained; lasting
// instructions belong in system/developer messages. Content is never abbreviated.
// Malformed or unfinished tool batches are rejected even if otherwise evictable.
// Returned messages own their slices, so callers cannot modify source history.
func Project(messages []agent.Message, definitions []agent.ToolDefinition, config Config) (Projection, error) {
	var result Projection
	budget := config.MaxInputBytes
	if budget < 0 {
		return result, ErrInvalidBudget
	}
	if budget == 0 {
		budget = DefaultMaxInputBytes
	}
	if err := validate(messages); err != nil {
		return result, err
	}
	base, err := contextBaseSize(definitions)
	if err != nil {
		return result, fmt.Errorf("size tool definitions: %w", err)
	}
	sizes := make([]int, len(messages))
	keep := make([]bool, len(messages))
	groups := make([][]int, 0)
	total, count := base, len(messages)
	for i, msg := range messages {
		size, err := messageSize(msg)
		if err != nil {
			return result, fmt.Errorf("size message %d: %w", i, err)
		}
		sizes[i], keep[i] = size, true
		total += size
		if msg.Role == "system" || msg.Role == "developer" {
			continue
		}
		if msg.Role == "user" || len(groups) == 0 {
			groups = append(groups, nil)
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], i)
	}
	if count > 0 {
		total += count - 1
	}
	// Never remove the last turn, even if it is the only one.
	for g := 0; total > budget && g+1 < len(groups); g++ {
		for _, i := range groups[g] {
			keep[i] = false
			total -= sizes[i]
			if count > 1 {
				total--
			}
			count--
			result.DroppedMessages++
		}
	}
	if total > budget {
		return Projection{}, &BudgetError{RequiredBytes: total, MaxInputBytes: budget}
	}
	result.Messages = make([]agent.Message, 0, count)
	for i, msg := range messages {
		if keep[i] {
			result.Messages = append(result.Messages, clone(msg))
		}
	}
	result.InputBytes = total
	return result, nil
}

func clone(m agent.Message) agent.Message {
	if m.ToolCalls != nil {
		m.ToolCalls = append([]agent.ToolCall{}, m.ToolCalls...)
		for i := range m.ToolCalls {
			m.ToolCalls[i].Arguments = append(json.RawMessage(nil), m.ToolCalls[i].Arguments...)
		}
	}
	return m
}

func validate(messages []agent.Message) error {
	pending := map[string]bool{}
	for i, m := range messages {
		invalid := func(reason string) error { return fmt.Errorf("%w at message %d: %s", ErrInvalidHistory, i, reason) }
		if len(pending) > 0 && m.Role != "tool" {
			return invalid("tool batch is missing results")
		}
		if m.Role != "assistant" && len(m.ToolCalls) > 0 {
			return invalid("tool calls require assistant role")
		}
		if m.Role != "tool" && m.ToolCallID != "" {
			return invalid("tool result ID requires tool role")
		}
		switch m.Role {
		case "system", "developer", "user":
		case "assistant":
			for _, call := range m.ToolCalls {
				if call.ID == "" || pending[call.ID] {
					return invalid("missing or duplicate tool call ID")
				}
				pending[call.ID] = true
			}
		case "tool":
			if m.ToolCallID == "" || !pending[m.ToolCallID] {
				return invalid("orphan or duplicate tool result")
			}
			delete(pending, m.ToolCallID)
		default:
			return invalid("unsupported message role")
		}
	}
	if len(pending) > 0 {
		return fmt.Errorf("%w: unfinished tool batch", ErrInvalidHistory)
	}
	return nil
}

// Match the Chat Completions context representation, including escaping tool
// arguments as JSON strings and wrapping schemas. No heuristic token conversion.
type wireCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}
type wireMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []wireCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}
type wireTool struct {
	Type     string               `json:"type"`
	Function agent.ToolDefinition `json:"function"`
}

func messageSize(m agent.Message) (int, error) {
	w := wireMessage{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
	for _, c := range m.ToolCalls {
		wc := wireCall{ID: c.ID, Type: "function"}
		wc.Function.Name, wc.Function.Arguments = c.Name, string(c.Arguments)
		w.ToolCalls = append(w.ToolCalls, wc)
	}
	raw, err := json.Marshal(w)
	return len(raw), err
}
func contextBaseSize(definitions []agent.ToolDefinition) (int, error) {
	payload := struct {
		Messages []wireMessage `json:"messages"`
		Tools    []wireTool    `json:"tools,omitempty"`
	}{Messages: []wireMessage{}}
	for _, d := range definitions {
		payload.Tools = append(payload.Tools, wireTool{Type: "function", Function: d})
	}
	raw, err := json.Marshal(payload)
	return len(raw), err
}
