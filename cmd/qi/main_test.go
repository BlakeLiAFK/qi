package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopi/internal/agent"
	"gopi/internal/workers"
)

func TestOneShotAndInteractive(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		t.Run(map[bool]string{false: "one-shot", true: "interactive"}[interactive], func(t *testing.T) {
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/chat/completions" {
					t.Errorf("path = %s", r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer test-key" {
					t.Error("missing authorization")
				}
				var req struct {
					Model    string          `json:"model"`
					Messages []agent.Message `json:"messages"`
					Tools    []any           `json:"tools"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				if req.Model != "test-model" || len(req.Tools) != 0 {
					t.Errorf("unexpected request: %+v", req)
				}
				want := 2 + calls*2
				if len(req.Messages) != want {
					t.Errorf("messages=%d want=%d", len(req.Messages), want)
				}
				calls++
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`))
			}))
			defer s.Close()
			env := func(k string) string {
				return map[string]string{"OPENAI_MODEL": "test-model", "OPENAI_BASE_URL": s.URL + "/v1", "OPENAI_API_KEY": "test-key"}[k]
			}
			args := []string{"-p", "hi"}
			input := ""
			want := "hello\n"
			if interactive {
				args = nil
				input = "hi\nagain\n/quit\n"
				want += "hello\n"
			}
			var out, diag bytes.Buffer
			if err := run(context.Background(), args, strings.NewReader(input), &out, &diag, env); err != nil {
				t.Fatal(err)
			}
			if out.String() != want {
				t.Fatalf("output %q", out.String())
			}
		})
	}
}
func TestApprovalBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		yes, allow  bool
	}{{"default", "", false, false}, {"no", "n\n", false, false}, {"explicit", "y\n", false, true}, {"flag", "", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			tool := localTool{agent.ToolDefinition{Name: "test", Parameters: json.RawMessage(`{}`)}, func(context.Context, json.RawMessage) (string, error) { called = true; return "ok", nil }}
			var diag bytes.Buffer
			a := approvedTool{Tool: tool, yes: tc.yes, in: bufio.NewScanner(strings.NewReader(tc.input)), out: &diag}
			_, err := a.Execute(context.Background(), json.RawMessage(`{"text":"untrusted"}`))
			if called != tc.allow || (err == nil) != tc.allow {
				t.Fatalf("called=%v err=%v", called, err)
			}
		})
	}
}
func TestPluginValidation(t *testing.T) {
	for _, data := range []string{`[{"name":"x","command":"test","typo":1}]`, `[{"name":"x","command":"test"},{"name":"x","command":"test"}]`, `[{"name":"../x","command":"test"}]`, `[] []`} {
		path := filepath.Join(t.TempDir(), "plugins.json")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readPlugins(path); err == nil {
			t.Fatalf("accepted %s", data)
		}
	}
}
func TestWorkerCannotChooseDirectory(t *testing.T) {
	m := workers.New(workers.Config{})
	defer m.Close()
	ts := workerTools(m, t.TempDir())
	_, err := ts[0].Execute(context.Background(), json.RawMessage(`{"agent":"codex","prompt":"test","work_dir":"/tmp"}`))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unexpected error %v", err)
	}
}
func TestInvalidFlags(t *testing.T) {
	for _, args := range [][]string{{}, {"-model", "test", "-max-steps", "0"}, {"-model", "test", "unexpected"}} {
		var b bytes.Buffer
		if err := run(context.Background(), args, strings.NewReader(""), &b, &b, func(string) string { return "" }); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestWorkersRequireFlag(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Tools []struct {
					Function agent.ToolDefinition `json:"function"`
				} `json:"tools"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			want := 0
			if enabled {
				want = 3
			}
			if len(req.Tools) != want {
				t.Errorf("tools=%d want=%d", len(req.Tools), want)
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
		}))
		args := []string{"-model", "test", "-base-url", s.URL, "-p", "hello"}
		if enabled {
			args = append(args, "-enable-workers", "-work-dir", t.TempDir())
		}
		var out bytes.Buffer
		if err := run(context.Background(), args, strings.NewReader(""), &out, &out, func(string) string { return "" }); err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
}
