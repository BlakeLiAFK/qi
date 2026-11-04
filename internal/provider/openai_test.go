package provider

import (
	"context"
	"encoding/json"
	"gopi/internal/agent"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIProtocol(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("request %s headers %v", r.URL.Path, r.Header)
		}
		var body struct {
			Model    string
			Messages []message
			Tools    []tool
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "test-model" || len(body.Tools) != 1 || body.Messages[1].ToolCalls[0].Function.Arguments != `{"x":1}` {
			t.Errorf("body %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"call2","type":"function","function":{"name":"echo","arguments":"{}"}}]}}]}`))
	}))
	defer s.Close()
	p := OpenAI{BaseURL: s.URL + "/v1/", APIKey: "test-key", Model: "test-model"}
	got, err := p.Complete(context.Background(), []agent.Message{{Role: "user", Content: "hi"}, {Role: "assistant", ToolCalls: []agent.ToolCall{{ID: "call1", Name: "echo", Arguments: json.RawMessage(`{"x":1}`)}}}}, []agent.ToolDefinition{{Name: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}})
	if err != nil || len(got.ToolCalls) != 1 || got.ToolCalls[0].ID != "call2" {
		t.Fatalf("%+v %v", got, err)
	}
}
func TestProviderFailures(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"unauthorized", 401, `secret prompt`, "HTTP 401"},
		{"malformed", 200, `{`, "decode"},
		{"empty", 200, `{"choices":[]}`, "no choices"},
		{"truncated", 200, `{"choices":[{"finish_reason":"length","message":{"content":"partial"}}]}`, "truncated"},
		{"filtered", 200, `{"choices":[{"finish_reason":"content_filter","message":{}}]}`, "blocked"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); w.Write([]byte(tc.body)) }))
			defer s.Close()
			p := OpenAI{BaseURL: s.URL, Model: "x"}
			_, err := p.Complete(context.Background(), nil, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "secret prompt") {
				t.Fatal(err)
			}
		})
	}
}
func TestProviderCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := OpenAI{BaseURL: "http://127.0.0.1:1", Model: "x"}
	if _, err := p.Complete(ctx, nil, nil); err == nil {
		t.Fatal("expected canceled request")
	}
}
