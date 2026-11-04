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

func TestEndpointTransportPolicy(t *testing.T) {
	cases := []struct {
		raw     string
		allowed bool
	}{
		{"https://api.openai.com/v1", true},
		{"https://localhost:8443/v1", true},
		{"http://127.0.0.1:8080/v1", true},
		{"http://127.42.0.1/v1", true},
		{"http://[::1]:8080/v1", true},
		{"http://localhost:8080/v1", false},
		{"http://localhost.evil.example/v1", false},
		{"http://127.0.0.1.evil.example/v1", false},
		{"http://127.0.0.1@evil.example/v1", false},
		{"http://192.168.1.2/v1", false},
		{"http://api.openai.com/v1", false},
		{"http://2130706433/v1", false},
		{"http://[::1%25eth0]/v1", false},
		{"https://user:secret@example.com/v1", false},
		{"https://example.com/v1?key=secret", false},
		{"https://example.com/v1?", false},
		{"https://example.com/v1#fragment", false},
		{"//example.com/v1", false},
		{"file:///tmp/model", false},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			if err := validateEndpoint(tc.raw); (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v, error=%v", tc.allowed, err)
			}
		})
	}
}

func TestUnsafeEndpointRejectedBeforeRequest(t *testing.T) {
	p := OpenAI{BaseURL: "http://api.example.com/v1", APIKey: "never-transmit", Model: "test"}
	if _, err := p.Complete(context.Background(), nil, nil); err == nil || !strings.Contains(err.Error(), "requires HTTPS") {
		t.Fatalf("expected transport validation, got %v", err)
	}
}

func TestRedirectNeverForwardsPromptOrKey(t *testing.T) {
	forwarded := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded = true
		w.Write([]byte(`{"choices":[{"message":{"content":"unsafe"}}]}`))
	}))
	defer destination.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	client := source.Client()
	customPolicyCalled := false
	client.CheckRedirect = func(*http.Request, []*http.Request) error { customPolicyCalled = true; return nil }
	p := OpenAI{BaseURL: source.URL, APIKey: "secret", Model: "test", Client: client}
	_, err := p.Complete(context.Background(), []agent.Message{{Role: "user", Content: "private prompt"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 307") || forwarded || customPolicyCalled {
		t.Fatalf("error=%v forwarded=%v custom policy=%v", err, forwarded, customPolicyCalled)
	}
	if client.CheckRedirect == nil {
		t.Fatal("mutated caller-owned client")
	}
}
