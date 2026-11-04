package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopi/internal/provider"
	"gopi/internal/tasks"
)

func resolveModelTest(t *testing.T, args []string, values map[string]string) (modelSettings, error) {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	f := registerModelFlags(fs)
	if err := fs.Parse(args); err != nil {
		return modelSettings{}, err
	}
	return f.resolve(fs, func(k string) string { return values[k] })
}
func TestProviderEnvironmentIsolation(t *testing.T) {
	values := map[string]string{"OPENAI_MODEL": "openai-fixture", "OPENAI_BASE_URL": "https://openai-fixture.invalid/v1", "OPENAI_API_KEY": "openai-key-fixture", "ANTHROPIC_MODEL": "anthropic-fixture", "ANTHROPIC_API_KEY": "anthropic-key-fixture"}
	c, err := resolveModelTest(t, []string{"-provider", "anthropic"}, values)
	if err != nil {
		t.Fatal(err)
	}
	if c.model != "anthropic-fixture" || c.base != "" || c.key != "anthropic-key-fixture" || c.maxTokens != 4096 {
		t.Fatal("Anthropic inherited wrong settings")
	}
	m, err := newModelProvider(c, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.(*provider.Anthropic); !ok {
		t.Fatal("not native Anthropic")
	}
	delete(values, "ANTHROPIC_API_KEY")
	c, err = resolveModelTest(t, []string{"-provider", "anthropic"}, values)
	if err != nil || c.key != "" {
		t.Fatal("OpenAI key used as fallback")
	}
	c, err = resolveModelTest(t, nil, values)
	if err != nil || c.model != "openai-fixture" || c.base != values["OPENAI_BASE_URL"] || c.key != values["OPENAI_API_KEY"] {
		t.Fatal("OpenAI environment compatibility changed")
	}
	values["ANTHROPIC_BASE_URL"] = "https://anthropic-fixture.invalid/v1"
	values["ANTHROPIC_MAX_TOKENS"] = "123"
	c, err = resolveModelTest(t, []string{"-provider", "anthropic", "-model", "override", "-base-url", "", "-max-output-tokens", "321"}, values)
	if err != nil || c.model != "override" || c.base != "" || c.maxTokens != 321 {
		t.Fatal("explicit overrides not honored")
	}
}
func TestProviderInvalidConfiguration(t *testing.T) {
	env := map[string]string{"OPENAI_MODEL": "fixture", "ANTHROPIC_MODEL": "fixture"}
	for _, args := range [][]string{{"-provider", "unknown"}, {"-max-output-tokens", "100"}, {"-provider", "anthropic", "-max-output-tokens", "0"}, {"-provider", "anthropic", "-max-output-tokens", "-1"}, {"-provider", "anthropic", "-model", ""}} {
		if _, err := resolveModelTest(t, args, env); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	env["ANTHROPIC_MAX_TOKENS"] = "bad"
	if _, err := resolveModelTest(t, []string{"-provider", "anthropic"}, env); err == nil {
		t.Fatal("accepted invalid token environment")
	}
	var b bytes.Buffer
	err := runServer(context.Background(), []string{"-provider", "anthropic", "-model", "fixture", "-model-stream"}, &b, func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "not supported by native Anthropic") {
		t.Fatalf("streaming not rejected early: %v", err)
	}
}
func TestNativeAnthropicCLIAndDaemon(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "anthropic-fixture-key" || r.Header.Get("Authorization") != "" {
			t.Error("wrong Anthropic request routing/auth")
		}
		var body struct {
			Model     string `json:"model"`
			MaxTokens int    `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "anthropic-fixture" || body.MaxTokens != 123 {
			t.Errorf("bad request: %+v", body)
		}
		_, _ = w.Write([]byte(`{"type":"message","role":"assistant","content":[{"type":"text","text":"native reply"}],"stop_reason":"end_turn"}`))
	}))
	defer mock.Close()
	values := map[string]string{"ANTHROPIC_MODEL": "anthropic-fixture", "ANTHROPIC_BASE_URL": mock.URL + "/v1", "ANTHROPIC_API_KEY": "anthropic-fixture-key", "ANTHROPIC_MAX_TOKENS": "123", "OPENAI_API_KEY": "must-not-leak"}
	var out, diag bytes.Buffer
	if err := run(context.Background(), []string{"-provider", "anthropic", "-p", "hello"}, strings.NewReader(""), &out, &diag, func(k string) string { return values[k] }); err != nil {
		t.Fatal(err)
	}
	if out.String() != "native reply\n" {
		t.Fatal(out.String())
	}
	c, err := resolveModelTest(t, []string{"-provider", "anthropic"}, values)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()
	h, closeAll, err := openDaemon(context.Background(), daemonConfig{database: filepath.Join(t.TempDir(), "tasks.db"), providerName: c.name, model: c.model, baseURL: c.base, apiKey: c.key, maxOutputTokens: c.maxTokens, origin: origin, token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	server.Config.Handler = h
	server.Start()
	defer server.Close()
	client, err := newDaemonClient(origin, testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer client.http.CloseIdleConnections()
	var task tasks.Task
	if err = client.request(context.Background(), "POST", "/api/tasks", map[string]string{"title": "native fixture"}, &task); err != nil {
		t.Fatal(err)
	}
	if err = client.request(context.Background(), "POST", "/api/tasks/"+task.ID+"/messages", map[string]string{"prompt": "hello"}, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := client.get(context.Background(), task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == "completed" {
			if len(got.Messages) != 2 || got.Messages[1].Content != "native reply" {
				t.Fatal("native reply not persisted")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not complete: %s", got.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
