package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopi/internal/plugins"
)

func readyServer(t *testing.T) *server {
	t.Helper()
	s := &server{}
	r := s.handle([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`))
	if r == nil || r.Error != nil {
		t.Fatalf("initialize: %+v", r)
	}
	if r := s.handle([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)); r != nil {
		t.Fatal("notification produced output")
	}
	return s
}
func TestProtocol(t *testing.T) {
	s := readyServer(t)
	for _, tt := range []struct {
		name, input string
		code        int
	}{
		{"parse", `{`, -32700},
		{"batch", `[]`, -32600},
		{"version", `{"jsonrpc":"1.0","id":1,"method":"ping"}`, -32600},
		{"bad id", `{"jsonrpc":"2.0","id":{},"method":"ping"}`, -32600},
		{"unknown method", `{"jsonrpc":"2.0","id":1,"method":"shell"}`, -32601},
		{"unknown tool", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"shell","arguments":{}}}`, -32602},
		{"missing text", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"text_stats","arguments":{}}}`, -32602},
		{"null", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"text_stats","arguments":{"text":null}}}`, -32602},
		{"wrong type", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"text_stats","arguments":{"text":3}}}`, -32602},
		{"extra argument", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"text_stats","arguments":{"text":"hi","path":"/etc/passwd"}}}`, -32602},
		{"cursor", `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"cursor":"x"}}`, -32602},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := s.handle([]byte(tt.input))
			if r == nil || r.Error == nil || r.Error.Code != tt.code {
				t.Fatalf("got %+v, want %d", r, tt.code)
			}
		})
	}
	if r := new(server).handle([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)); r.Error == nil {
		t.Fatal("list before initialize")
	}
	if r := s.handle([]byte(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"text_stats","arguments":{"text":"hi"}}}`)); r != nil {
		t.Fatal("notification received response")
	}
	for _, size := range []int{4096, 4097} {
		input, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "unicode", "method": "tools/call", "params": map[string]any{"name": "text_stats", "arguments": map[string]string{"text": strings.Repeat("猫", size)}}})
		r := s.handle(input)
		if (r.Error != nil) != (size > maxText) {
			t.Fatalf("size %d: %+v", size, r)
		}
		if string(r.ID) != `"unicode"` {
			t.Fatal("lost string ID")
		}
	}
}
func TestFraming(t *testing.T) {
	var out bytes.Buffer
	if err := serve(strings.NewReader("{\n"+`{"jsonrpc":"2.0","id":2,"method":"ping"}`+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("unexpected output: %q", out.String())
	}
	for _, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatal("non-JSON stdout")
		}
	}
	out.Reset()
	if err := serve(strings.NewReader(strings.Repeat("x", maxFrame+1)), &out); err == nil {
		t.Fatal("oversized frame accepted")
	}
	if out.Len() != 0 {
		t.Fatal("oversized frame generated protocol output")
	}
}

// Build the actual executable, then use qi's real newline-delimited transport.
// No shell fixture, API key, network request, model, or external server is needed.
func TestQiProcessInteroperability(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "textstats")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	startModes := map[string]func(context.Context, plugins.Config) (*plugins.Client, error){"standalone": plugins.Start}
	if plugins.ScopedSupported() {
		startModes["daemon_scoped"] = plugins.StartScoped
	}
	for name, start := range startModes {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			client, err := start(ctx, plugins.Config{Command: binary, Dir: t.TempDir(), Timeout: 5 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			// The daemon calls directly without discovery; verify that lifecycle.
			raw, err := client.Call(ctx, "text_stats", json.RawMessage(`{"text":"你好 Go 🌍"}`))
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				Content []struct{ Type, Text string }
				IsError bool
			}
			if err = json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
			if result.IsError || len(result.Content) != 1 || result.Content[0].Text != "characters=7 words=3" {
				t.Fatalf("unexpected result: %s", raw)
			}
			tools, err := client.Tools(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(tools) != 1 || tools[0].Name != "text_stats" {
				t.Fatalf("unexpected tools: %+v", tools)
			}
			if _, err = client.Call(ctx, "exec", json.RawMessage(`{}`)); err == nil {
				t.Fatal("unknown tool allowed")
			}
			// An invalid request must not kill the session.
			if _, err = client.Call(ctx, "text_stats", json.RawMessage(`{"text":""}`)); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestManifestMatchesDiscovery(t *testing.T) {
	raw, err := os.ReadFile("mcp-tools.json")
	if err != nil {
		t.Fatal(err)
	}
	var config []struct {
		Name, Command, Dir string
		Tools              []plugins.Tool
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err = d.Decode(&config); err != nil {
		t.Fatal(err)
	}
	s := readyServer(t)
	listed := s.handle([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`))
	encoded, _ := json.Marshal(listed.Result)
	var found struct{ Tools []plugins.Tool }
	if err = json.Unmarshal(encoded, &found); err != nil {
		t.Fatal(err)
	}
	if len(config) != 1 || config[0].Name != "textstats" || len(config[0].Tools) != 1 {
		t.Fatal("unexpected manifest")
	}
	a, b := config[0].Tools[0], found.Tools[0]
	var x, y any
	if err = json.Unmarshal(a.InputSchema, &x); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b.InputSchema, &y); err != nil {
		t.Fatal(err)
	}
	if a.Name != b.Name || a.Description != b.Description || !reflect.DeepEqual(x, y) {
		t.Fatal("manifest and tools/list differ")
	}
	raw, err = os.ReadFile("plugins.json")
	if err != nil {
		t.Fatal(err)
	}
	var standalone []struct{ Name, Command, Dir string }
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err = d.Decode(&standalone); err != nil {
		t.Fatal(err)
	}
	if len(standalone) != 1 || standalone[0].Name != config[0].Name || standalone[0].Command != config[0].Command || standalone[0].Dir != config[0].Dir {
		t.Fatal("config paths differ")
	}
}
