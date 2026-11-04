package plugins

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"gopi/internal/agent"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMCPHelper(t *testing.T) {
	if os.Getenv("GOPI_MCP_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var req struct {
			ID     uint64 `json:"id"`
			Method string `json:"method"`
		}
		_ = json.Unmarshal(scanner.Bytes(), &req)
		if req.ID == 0 {
			continue
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05"}
		case "tools/list":
			result = map[string]any{"tools": []Tool{{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}}}
		case "tools/call":
			if os.Getenv("GOPI_MCP_HANG_CALL") == "1" {
				continue
			}
			result = map[string]any{"content": []map[string]string{{"type": "text", "text": "hello"}}}
		default:
			continue
		}
		_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
	os.Exit(0)
}
func TestLifecycle(t *testing.T) {
	c, err := Start(context.Background(), Config{Command: os.Args[0], Args: []string{"-test.run=TestMCPHelper"}, Env: []string{"GOPI_MCP_HELPER=1"}, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ts, err := c.Tools(context.Background())
	if err != nil || len(ts) != 1 || ts[0].Name != "echo" {
		t.Fatalf("tools: %v %v", ts, err)
	}
	result, err := c.Call(context.Background(), "echo", json.RawMessage(`{}`))
	if err != nil || !json.Valid(result) {
		t.Fatalf("call: %s %v", result, err)
	}
	if _, err = c.Call(context.Background(), "echo", json.RawMessage(`{`)); err == nil {
		t.Fatal("accepted malformed JSON")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err = c.request(ctx, "hang", nil); err == nil {
		t.Fatal("missing timeout")
	}
}
func TestMissingCommand(t *testing.T) {
	if _, err := Start(context.Background(), Config{}); err == nil {
		t.Fatal("missing command accepted")
	}
}

func TestCallTimeoutIsUnknown(t *testing.T) {
	c, err := Start(context.Background(), Config{Command: os.Args[0], Args: []string{"-test.run=TestMCPHelper"}, Env: []string{"GOPI_MCP_HELPER=1", "GOPI_MCP_HANG_CALL=1"}, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = c.Call(ctx, "echo", json.RawMessage(`{}`))
	if !errors.Is(err, agent.ErrOutcomeUnknown) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("missing unknown outcome: %v", err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	_, err = c.Call(ctx2, "echo", json.RawMessage(`{}`))
	if !errors.Is(err, context.Canceled) || errors.Is(err, agent.ErrOutcomeUnknown) {
		t.Fatalf("pre-dispatch cancellation should be known: %v", err)
	}
}

func TestPluginEnvironmentExcludesImplicitSecrets(t *testing.T) {
	t.Setenv("GOPI_TEST_SECRET", "hidden")
	t.Setenv("PATH", "/test/bin")
	env := pluginEnvironment([]string{"EXPLICIT_TOKEN=allowed"})
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "GOPI_TEST_SECRET=") || !strings.Contains(joined, "PATH=/test/bin") || !strings.Contains(joined, "EXPLICIT_TOKEN=allowed") {
		t.Fatal("bad environment filtering")
	}
}
