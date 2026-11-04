// Package provider contains model transports; the agent core has no HTTP dependency.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"gopi/internal/agent"
)

// OpenAI uses the OpenAI-compatible Chat Completions protocol. BaseURL is the
// API root (for example https://api.openai.com/v1), not the completion endpoint.
// Remote endpoints require HTTPS. HTTP is allowed only for literal loopback IPs.
// Redirects are never followed, to avoid forwarding prompts or credentials.
type OpenAI struct {
	BaseURL string
	APIKey  string
	Model   string
	Client  *http.Client
	// Stream opts into SSE transport. Text progress is provisional until Complete returns.
	Stream bool
}

type function struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
type call struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Function function `json:"function"`
}
type message struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	ToolCalls  []call `json:"tool_calls,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}
type tool struct {
	Type     string               `json:"type"`
	Function agent.ToolDefinition `json:"function"`
}

// validateEndpoint deliberately does not resolve hostnames: localhost aliases and
// DNS results cannot grant an exception to the HTTPS requirement.
func validateEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("model endpoint must be an absolute URL without credentials, query, or fragment")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" {
		ip, err := netip.ParseAddr(u.Hostname())
		if err == nil && ip.Zone() == "" && ip.IsLoopback() {
			return nil
		}
	}
	return errors.New("model endpoint requires HTTPS; HTTP is allowed only for a literal loopback IP")
}

func (p *OpenAI) Complete(ctx context.Context, messages []agent.Message, definitions []agent.ToolDefinition) (agent.Message, error) {
	return p.CompleteWithEvents(ctx, messages, definitions, nil)
}

// CompleteWithEvents reports ordered, text-only progress when streaming is enabled.
// The callback is request-local and is never retained or called concurrently.
// Preview is limited to 64 KiB and 256 events per model request; final output is
// unaffected by preview limits. Agent MaxSteps bounds the number of requests.
func (p *OpenAI) CompleteWithEvents(ctx context.Context, messages []agent.Message, definitions []agent.ToolDefinition, emit func(agent.Event)) (agent.Message, error) {
	var zero agent.Message
	if p.Model == "" {
		return zero, errors.New("model name is required")
	}
	wire := make([]message, 0, len(messages))
	for _, m := range messages {
		w := message{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
		for _, c := range m.ToolCalls {
			w.ToolCalls = append(w.ToolCalls, call{ID: c.ID, Type: "function", Function: function{Name: c.Name, Arguments: string(c.Arguments)}})
		}
		wire = append(wire, w)
	}
	ts := make([]tool, 0, len(definitions))
	for _, d := range definitions {
		ts = append(ts, tool{Type: "function", Function: d})
	}
	body, err := json.Marshal(struct {
		Model    string    `json:"model"`
		Messages []message `json:"messages"`
		Tools    []tool    `json:"tools,omitempty"`
		Stream   bool      `json:"stream,omitempty"`
	}{p.Model, wire, ts, p.Stream})
	if err != nil {
		return zero, err
	}
	base := strings.TrimRight(p.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	if err := validateEndpoint(base); err != nil {
		return zero, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return zero, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.Stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 120 * time.Second}
	}
	// Copy instead of mutating a caller-owned client. Even a caller-supplied
	// redirect policy cannot weaken the credential and prompt transport boundary.
	safeClient := *client
	safeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := safeClient.Do(req)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Never expose provider response bodies, which may contain private data.
		return zero, fmt.Errorf("model HTTP %d (%s)", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	if p.Stream {
		return readStream(ctx, resp, emit)
	}
	const maxBody = 8 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return zero, err
	}
	if len(raw) > maxBody {
		return zero, errors.New("model response exceeds 8 MiB")
	}
	var result struct {
		Choices []struct {
			Message      message `json:"message"`
			FinishReason string  `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return zero, fmt.Errorf("decode model response: %w", err)
	}
	if len(result.Choices) == 0 {
		return zero, errors.New("model returned no choices")
	}
	choice := result.Choices[0]
	if choice.FinishReason == "length" {
		return zero, errors.New("model response was truncated by token limit")
	}
	if choice.FinishReason == "content_filter" {
		return zero, errors.New("model response was blocked by content filter")
	}
	m := choice.Message
	out := agent.Message{Role: "assistant", Content: m.Content}
	for _, c := range m.ToolCalls {
		if c.Type != "function" {
			return zero, fmt.Errorf("unsupported tool call type %q", c.Type)
		}
		out.ToolCalls = append(out.ToolCalls, agent.ToolCall{ID: c.ID, Name: c.Function.Name, Arguments: json.RawMessage(c.Function.Arguments)})
	}
	return out, nil
}
