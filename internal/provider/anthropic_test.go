package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gopi/internal/agent"
)

const anthropicOK = `{"type":"message","role":"assistant","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}`

func TestAnthropicProtocol(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "test-key" || r.Header.Get("anthropic-version") != "2023-06-01" || r.Header.Get("Authorization") != "" {
			t.Error("invalid Anthropic request transport")
		}
		var b anthropicRequest
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			t.Fatal(err)
		}
		if b.Model != "test-model" || b.MaxTokens != 123 || len(b.System) != 2 || b.System[1].Text != "second" || len(b.Messages) != 4 || len(b.Tools) != 1 || string(b.Tools[0].InputSchema) != `{"type":"object"}` {
			t.Errorf("unexpected conversion: %+v", b)
		}
		if b.Messages[1].Content[1].Type != "tool_use" || string(b.Messages[1].Content[1].Input) != `{"x":1}` || len(b.Messages[2].Content) != 2 || b.Messages[2].Content[0].ToolUseID != "one" || *b.Messages[2].Content[1].Content != "" {
			t.Error("tool history conversion")
		}
		w.Write([]byte(`{"type":"message","role":"assistant","stop_reason":"tool_use","content":[{"type":"text","text":"checking"},{"type":"tool_use","id":"three","name":"echo","input":{"x":3}},{"type":"tool_use","id":"four","name":"echo","input":{}}]}`))
	}))
	defer s.Close()
	p := Anthropic{BaseURL: s.URL + "/v1/", APIKey: "test-key", Model: "test-model", MaxTokens: 123}
	msgs := []agent.Message{{Role: "system", Content: "first"}, {Role: "system", Content: "second"}, {Role: "user", Content: "hi"}, {Role: "assistant", Content: "checking", ToolCalls: []agent.ToolCall{{ID: "one", Name: "echo", Arguments: json.RawMessage(`{"x":1}`)}, {ID: "two", Name: "echo", Arguments: json.RawMessage(`{}`)}}}, {Role: "tool", ToolCallID: "one", Content: "output"}, {Role: "tool", ToolCallID: "two", Content: ""}, {Role: "user", Content: "continue"}}
	got, err := p.Complete(context.Background(), msgs, []agent.ToolDefinition{{Name: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}})
	if err != nil || got.Content != "checking" || len(got.ToolCalls) != 2 || got.ToolCalls[0].ID != "three" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestAnthropicFailures(t *testing.T) {
	cases := []struct {
		name, body, want string
		status           int
	}{
		{"error", "secret-key private-prompt", "HTTP 401", 401},
		{"malformed", `{"secret-key":`, "decode", 200},
		{"truncated", `{"type":"message","role":"assistant","stop_reason":"max_tokens","content":[{"type":"tool_use","id":"x","name":"echo","input":{}}]}`, "truncated", 200},
		{"unsupported", `{"type":"message","role":"assistant","stop_reason":"end_turn","content":[{"type":"thinking"}]}`, "unsupported", 200},
		{"missing_stop", `{"type":"message","role":"assistant","content":[{"type":"text","text":"partial"}]}`, "stop reason", 200},
		{"bad_input", `{"type":"message","role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"x","name":"echo","input":[]}]}`, "invalid", 200},
		{"duplicate_id", `{"type":"message","role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"x","name":"echo","input":{}},{"type":"tool_use","id":"x","name":"echo","input":{}}]}`, "invalid", 200},
		{"mismatch", `{"type":"message","role":"assistant","stop_reason":"tool_use","content":[{"type":"text","text":"hi"}]}`, "does not match", 200},
		{"interleaved", `{"type":"message","role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"x","name":"echo","input":{}},{"type":"text","text":"after"}]}`, "text after", 200},
		{"oversize", strings.Repeat("x", (8<<20)+1), "exceeds", 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); w.Write([]byte(tc.body)) }))
			defer s.Close()
			p := Anthropic{BaseURL: s.URL, Model: "x"}
			got, err := p.Complete(context.Background(), []agent.Message{{Role: "user", Content: "hi"}}, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "secret-key") || len(got.ToolCalls) > 0 || got.Content != "" {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}

func TestAnthropicCancellation(t *testing.T) {
	started := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		p := Anthropic{BaseURL: s.URL, Model: "x"}
		_, err := p.Complete(ctx, []agent.Message{{Role: "user", Content: "hi"}}, nil)
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation blocked")
	}
}

func TestAnthropicTransportBoundaries(t *testing.T) {
	p := Anthropic{BaseURL: "http://api.example.com/v1", Model: "x", APIKey: "secret"}
	if _, err := p.Complete(context.Background(), nil, nil); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatal(err)
	}
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("redirect forwarded private request") }))
	defer destination.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	client := source.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { t.Error("custom redirect policy invoked"); return nil }
	p = Anthropic{BaseURL: source.URL, Model: "x", APIKey: "secret", Client: client}
	if _, err := p.Complete(context.Background(), []agent.Message{{Role: "user", Content: "private"}}, nil); err == nil || !strings.Contains(err.Error(), "307") {
		t.Fatal(err)
	}
}

func TestAnthropicRejectsBrokenHistory(t *testing.T) {
	call := agent.Message{Role: "assistant", ToolCalls: []agent.ToolCall{{ID: "one", Name: "echo", Arguments: json.RawMessage(`{}`)}}}
	for _, messages := range [][]agent.Message{
		{{Role: "tool", ToolCallID: "missing", Content: "out"}},
		{call},
		{call, {Role: "user", Content: "interrupted"}},
		{{Role: "user", Content: "hi"}, {Role: "system", Content: "late"}},
		{{Role: "developer", Content: "unsupported"}},
	} {
		if _, err := anthropicInput(messages, nil); err == nil {
			t.Fatalf("accepted %+v", messages)
		}
	}
}

func TestAnthropicDefaultTokensAndText(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b anthropicRequest
		json.NewDecoder(r.Body).Decode(&b)
		if b.MaxTokens != 4096 {
			t.Error(b.MaxTokens)
		}
		w.Write([]byte(anthropicOK))
	}))
	defer s.Close()
	p := Anthropic{BaseURL: s.URL, Model: "x"}
	got, err := p.Complete(context.Background(), []agent.Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil || got.Content != "done" {
		t.Fatalf("%+v %v", got, err)
	}
}

type anthropicEcho struct{}

func (anthropicEcho) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{Name: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (anthropicEcho) Execute(context.Context, json.RawMessage) (string, error) { return "echoed", nil }

func TestAnthropicAgentToolRoundTrip(t *testing.T) {
	requests := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body anthropicRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if requests == 1 {
			w.Write([]byte(`{"type":"message","role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"tool-one","name":"echo","input":{}}]}`))
			return
		}
		if len(body.Messages) != 3 || body.Messages[2].Role != "user" || body.Messages[2].Content[0].ToolUseID != "tool-one" || *body.Messages[2].Content[0].Content != "echoed" {
			t.Error("incorrect tool result continuation")
		}
		w.Write([]byte(anthropicOK))
	}))
	defer s.Close()
	a := agent.Agent{Model: &Anthropic{BaseURL: s.URL, Model: "test"}, Tools: []agent.Tool{anthropicEcho{}}}
	history, err := a.Run(context.Background(), []agent.Message{{Role: "user", Content: "echo"}})
	if err != nil || len(history) != 4 || history[3].Content != "done" || requests != 2 {
		t.Fatalf("requests=%d history=%+v err=%v", requests, history, err)
	}
}
