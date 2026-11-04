package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gopi/internal/agent"
)

// Anthropic implements buffered native Messages API requests, not Claude Code.
// BaseURL is an API root ending in /v1. Remote endpoints require HTTPS; only
// literal loopback IPs permit HTTP. Credentials and prompts never follow redirects.
// This text/client-tool adapter deliberately rejects unsupported content instead
// of silently losing blocks that would be needed on a subsequent tool turn.
// Protocol: https://platform.claude.com/docs/en/api/messages/create
// Tools: https://platform.claude.com/docs/en/agents-and-tools/tool-use/handle-tool-calls
// Version is pinned to the stable 2023-06-01 contract, not beta features.
type Anthropic struct {
	BaseURL   string
	APIKey    string
	Model     string
	MaxTokens int
	Client    *http.Client
}

type anthropicBlock struct {
	Type        string          `json:"type"`
	Text        string          `json:"text,omitempty"`
	ID          string          `json:"id,omitempty"`
	Name        string          `json:"name,omitempty"`
	Input       json.RawMessage `json:"input,omitempty"`
	ToolsetName string          `json:"toolset_name,omitempty"`
	ToolUseID   string          `json:"tool_use_id,omitempty"`
	Content     *string         `json:"content,omitempty"`
}
type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}
type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}
type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    []anthropicBlock   `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
}

func anthropicInput(messages []agent.Message, definitions []agent.ToolDefinition) (anthropicRequest, error) {
	out := anthropicRequest{Messages: []anthropicMessage{}}
	pending := map[string]bool{}
	used := map[string]bool{}
	for _, m := range messages {
		if m.Role != "assistant" && len(m.ToolCalls) != 0 || m.Role != "tool" && m.ToolCallID != "" {
			return out, errors.New("invalid Anthropic message tool fields")
		}
		if len(pending) > 0 && m.Role != "tool" {
			return out, errors.New("Anthropic tool results must immediately follow tool calls")
		}
		switch m.Role {
		case "system":
			if len(out.Messages) != 0 {
				return out, errors.New("Anthropic system messages must precede conversation")
			}
			if m.Content != "" {
				out.System = append(out.System, anthropicBlock{Type: "text", Text: m.Content})
			}
		case "user", "assistant":
			blocks := []anthropicBlock{}
			if m.Content != "" {
				blocks = append(blocks, anthropicBlock{Type: "text", Text: m.Content})
			}
			if len(m.ToolCalls) > agent.MaxToolCalls {
				return out, errors.New("too many Anthropic tool calls")
			}
			for _, c := range m.ToolCalls {
				if c.ID == "" || used[c.ID] || c.Name == "" || !agent.IsJSONObject(c.Arguments) {
					return out, errors.New("invalid Anthropic tool call")
				}
				used[c.ID], pending[c.ID] = true, true
				blocks = append(blocks, anthropicBlock{Type: "tool_use", ID: c.ID, Name: c.Name, Input: c.Arguments})
			}
			if len(blocks) == 0 {
				return out, errors.New("empty Anthropic message")
			}
			out.Messages = append(out.Messages, anthropicMessage{Role: m.Role, Content: blocks})
		case "tool":
			if !pending[m.ToolCallID] {
				return out, errors.New("unmatched Anthropic tool result")
			}
			delete(pending, m.ToolCallID)
			b := anthropicBlock{Type: "tool_result", ToolUseID: m.ToolCallID, Content: &m.Content}
			n := len(out.Messages)
			if n > 0 && out.Messages[n-1].Role == "user" {
				out.Messages[n-1].Content = append(out.Messages[n-1].Content, b)
			} else {
				out.Messages = append(out.Messages, anthropicMessage{Role: "user", Content: []anthropicBlock{b}})
			}
		default:
			return out, errors.New("unsupported Anthropic message role")
		}
	}
	if len(pending) > 0 {
		return out, errors.New("missing Anthropic tool results")
	}
	if len(definitions) > agent.MaxTools {
		return out, errors.New("too many Anthropic tools")
	}
	for _, d := range definitions {
		if d.Name == "" || !agent.IsJSONObject(d.Parameters) {
			return out, errors.New("invalid Anthropic tool definition")
		}
		out.Tools = append(out.Tools, anthropicTool{Name: d.Name, Description: d.Description, InputSchema: d.Parameters})
	}
	return out, nil
}

func (p *Anthropic) Complete(ctx context.Context, messages []agent.Message, definitions []agent.ToolDefinition) (agent.Message, error) {
	zero := agent.Message{}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if strings.TrimSpace(p.Model) == "" {
		return zero, errors.New("model name is required")
	}
	if p.MaxTokens < 0 {
		return zero, errors.New("Anthropic max tokens must be positive")
	}
	base := strings.TrimRight(p.BaseURL, "/")
	if base == "" {
		base = "https://api.anthropic.com/v1"
	}
	if err := validateEndpoint(base); err != nil {
		return zero, err
	}
	wire, err := anthropicInput(messages, definitions)
	if err != nil {
		return zero, err
	}
	wire.Model, wire.MaxTokens = p.Model, p.MaxTokens
	if wire.MaxTokens == 0 {
		wire.MaxTokens = 4096
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return zero, errors.New("encode Anthropic request failed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/messages", bytes.NewReader(body))
	if err != nil {
		return zero, errors.New("construct Anthropic request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	if p.APIKey != "" {
		req.Header.Set("x-api-key", p.APIKey)
	}
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 120 * time.Second}
	}
	safe := *client
	safe.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := safe.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		return zero, errors.New("Anthropic request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return zero, fmt.Errorf("Anthropic model HTTP %d (%s)", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	const maxBody = 8 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	if err != nil {
		return zero, errors.New("read Anthropic response failed")
	}
	if len(raw) > maxBody {
		return zero, errors.New("Anthropic response exceeds 8 MiB")
	}
	var result struct {
		Type       string           `json:"type"`
		Role       string           `json:"role"`
		Content    []anthropicBlock `json:"content"`
		StopReason string           `json:"stop_reason"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return zero, errors.New("decode Anthropic response failed")
	}
	if result.Type != "message" || result.Role != "assistant" {
		return zero, errors.New("invalid Anthropic response envelope")
	}
	switch result.StopReason {
	case "end_turn", "stop_sequence", "tool_use":
	case "max_tokens":
		return zero, errors.New("Anthropic response was truncated by token limit")
	case "refusal":
		return zero, errors.New("Anthropic response was refused")
	default:
		return zero, errors.New("unsupported Anthropic stop reason")
	}
	out := agent.Message{Role: "assistant"}
	ids := map[string]bool{}
	for _, b := range result.Content {
		switch b.Type {
		case "text":
			if len(out.ToolCalls) > 0 {
				return zero, errors.New("unsupported Anthropic text after tool use")
			}
			out.Content += b.Text
		case "tool_use":
			if b.ToolsetName != "" || len(out.ToolCalls) >= agent.MaxToolCalls || b.ID == "" || len(b.ID) > 4096 || ids[b.ID] || b.Name == "" || len(b.Name) > 4096 || !agent.IsJSONObject(b.Input) {
				return zero, errors.New("invalid Anthropic response tool call")
			}
			ids[b.ID] = true
			out.ToolCalls = append(out.ToolCalls, agent.ToolCall{ID: b.ID, Name: b.Name, Arguments: b.Input})
		default:
			return zero, errors.New("unsupported Anthropic response content block")
		}
	}
	if (len(out.ToolCalls) > 0) != (result.StopReason == "tool_use") {
		return zero, errors.New("Anthropic stop reason does not match tool calls")
	}
	if len(result.Content) == 0 {
		return zero, errors.New("empty Anthropic response")
	}
	return out, nil
}
