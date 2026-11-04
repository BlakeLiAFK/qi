package tasks

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopi/internal/agent"
	"gopi/internal/plugins"
	"gopi/internal/session"
)

func TestMCPTaskHelper(t *testing.T) {
	marker := ""
	for i, arg := range os.Args {
		if arg == "qi-mcp-test" && i+1 < len(os.Args) {
			marker = os.Args[i+1]
		}
	}
	if marker == "" {
		return
	}
	f, err := os.OpenFile(marker, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(2)
	}
	_, _ = f.WriteString("start\n")
	_ = os.WriteFile(marker+".home", []byte(os.Getenv("HOME")), 0600)
	_ = f.Close()
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req struct {
			ID     uint64 `json:"id"`
			Method string `json:"method"`
			Params struct {
				Arguments struct {
					Hang bool `json:"hang"`
					Fail bool `json:"fail"`
				} `json:"arguments"`
			} `json:"params"`
		}
		_ = json.Unmarshal(scanner.Bytes(), &req)
		if req.ID == 0 {
			continue
		}
		result := map[string]any{"protocolVersion": "2024-11-05"}
		if req.Method == "tools/list" {
			os.Exit(3)
		} // discovery is forbidden
		if req.Method == "tools/call" {
			calls, err := os.OpenFile(marker+".calls", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				os.Exit(4)
			}
			if _, err = calls.WriteString("call\n"); err != nil {
				os.Exit(5)
			}
			if err = calls.Close(); err != nil {
				os.Exit(6)
			}
			if req.Params.Arguments.Hang {
				continue
			}
			result = map[string]any{"isError": req.Params.Arguments.Fail, "home": os.Getenv("HOME"), "env": os.Environ()}
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
	os.Exit(0)
}
func mcpFixture(t *testing.T, args string, ttl, timeLimit time.Duration) (*Engine, *session.Store, Task, string) {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "started")
	s, err := session.Open(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	config := MCPServer{Name: "local", Command: os.Args[0], Args: []string{"-test.run=TestMCPTaskHelper", "--", "qi-mcp-test", marker}, Dir: t.TempDir(), ApprovalTTL: ttl, Timeout: timeLimit, Tools: []plugins.Tool{{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}}}
	e, err := New(context.Background(), s, Config{MCP: []MCPServer{config}, Factory: func(string) (*agent.Agent, error) {
		return &agent.Agent{Model: modelFunc(func(_ context.Context, m []agent.Message, defs []agent.ToolDefinition) (agent.Message, error) {
			if len(defs) != 1 || defs[0].Name != "mcp_local__echo" {
				return agent.Message{}, errors.New("missing MCP capability")
			}
			if m[len(m)-1].Role == "tool" {
				return agent.Message{Content: "done"}, nil
			}
			return agent.Message{ToolCalls: []agent.ToolCall{{ID: "call", Name: "mcp_local__echo", Arguments: json.RawMessage(args)}}}, nil
		})}, nil
	}})
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close(); s.Close() })
	task, err := e.Create(context.Background(), "mcp")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Start(context.Background(), task.ID, "use tool"); err != nil {
		t.Fatal(err)
	}
	return e, s, task, marker
}
func TestMCPApprovalAndNoAmbientSecrets(t *testing.T) {
	t.Setenv("QI_WEB_TOKEN", "must-not-leak")
	t.Setenv("OPENAI_API_KEY", "must-not-leak")
	e, s, task, marker := mcpFixture(t, `{"value":"specific"}`, time.Minute, time.Second)
	a := awaitApproval(t, e, task.ID)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("server started before approval")
	}
	if a.Action.Kind != "mcp.call" || !strings.Contains(string(a.Action.Payload), "specific") || !strings.Contains(string(a.Action.Payload), os.Args[0]) {
		t.Fatal("incomplete approval")
	}
	d := ApprovalDecision{ID: a.ID, Revision: a.Revision, Digest: a.Digest, Allow: true}
	other, _ := e.Create(context.Background(), "other")
	if _, err := e.DecideApproval(context.Background(), other.ID, d); !errors.Is(err, session.ErrApprovalUnavailable) {
		t.Fatal("cross-task approval accepted")
	}
	if _, err := e.DecideApproval(context.Background(), task.ID, d); err != nil {
		t.Fatal(err)
	}
	wait(t, e, task.ID)
	got, _ := s.GetApproval(context.Background(), task.ID, a.ID)
	if got.Status != "succeeded" {
		t.Fatalf("status %s", got.Status)
	}
	content, err := os.ReadFile(marker)
	if err != nil || string(content) != "start\n" {
		t.Fatalf("startup %q %v", content, err)
	}
	snapshot, _ := e.Get(context.Background(), task.ID)
	var result struct {
		Home string   `json:"home"`
		Env  []string `json:"env"`
	}
	for _, m := range snapshot.Messages {
		if m.Role == "tool" {
			_ = json.Unmarshal([]byte(m.Content), &result)
		}
	}
	if result.Home == "" || result.Home == os.Getenv("HOME") {
		t.Fatal("missing private home")
	}
	if _, err := os.Stat(result.Home); !os.IsNotExist(err) {
		t.Fatal("private home not removed")
	}
	if strings.Contains(strings.Join(result.Env, "\n"), "must-not-leak") {
		t.Fatal("ambient credential leaked")
	}
	if _, err := e.DecideApproval(context.Background(), task.ID, d); !errors.Is(err, session.ErrApprovalUnavailable) {
		t.Fatal("replayed approval")
	}
}
func TestMCPDeniedExpiredCancelledDoNotStart(t *testing.T) {
	for _, mode := range []string{"deny", "expire", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ttl := time.Minute
			if mode == "expire" {
				ttl = 100 * time.Millisecond
			}
			e, _, task, marker := mcpFixture(t, `{}`, ttl, time.Second)
			a := awaitApproval(t, e, task.ID)
			if mode == "deny" {
				_, err := e.DecideApproval(context.Background(), task.ID, ApprovalDecision{ID: a.ID, Revision: a.Revision, Digest: a.Digest})
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "cancel" {
				if err := e.Cancel(context.Background(), task.ID); err != nil {
					t.Fatal(err)
				}
			}
			wait(t, e, task.ID)
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("unapproved startup")
			}
		})
	}
}
func TestMCPUnknownOutcomesNotReplayed(t *testing.T) {
	for _, args := range []string{`{"hang":true}`, `{"fail":true}`} {
		t.Run(args, func(t *testing.T) {
			e, s, task, marker := mcpFixture(t, args, 5*time.Minute, time.Minute)
			a := awaitApproval(t, e, task.ID)
			if _, err := e.DecideApproval(context.Background(), task.ID, ApprovalDecision{ID: a.ID, Revision: a.Revision, Digest: a.Digest, Allow: true}); err != nil {
				t.Fatal(err)
			}
			// Synchronize with dispatch, not a wall-clock startup guess. A
			// deadline spanning process startup may expire before the helper
			// runs at all under race instrumentation or a busy test host.
			deadline := time.Now().Add(30 * time.Second)
			var calls []byte
			for time.Now().Before(deadline) {
				calls, _ = os.ReadFile(marker + ".calls")
				if string(calls) == "call\n" {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if string(calls) != "call\n" {
				t.Fatalf("helper did not receive exactly one call: %q", calls)
			}
			if args == `{"hang":true}` {
				// Exercise cancellation after receipt deterministically.
				// plugins.TestCallTimeoutIsUnknown covers a request deadline
				// created after MCP initialization, independently of startup.
				if err := e.Cancel(context.Background(), task.ID); err != nil {
					t.Fatal(err)
				}
			}
			wait(t, e, task.ID)
			got, _ := s.GetApproval(context.Background(), task.ID, a.ID)
			if got.Status != "unknown" {
				t.Fatalf("status %s", got.Status)
			}
			content, _ := os.ReadFile(marker)
			if string(content) != "start\n" {
				t.Fatalf("unexpected startup/replay: %q", content)
			}
			calls, _ = os.ReadFile(marker + ".calls")
			if string(calls) != "call\n" {
				t.Fatalf("unexpected call replay: %q", calls)
			}
		})
	}
}
func TestMCPConfigIsCopiedAndCapabilitiesValidated(t *testing.T) {
	s := MCPServer{Name: "local", Command: os.Args[0], Dir: t.TempDir(), Args: []string{"first"}, Tools: []plugins.Tool{{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}}}
	got, err := prepareMCP([]MCPServer{s})
	if err != nil {
		t.Fatal(err)
	}
	s.Args[0] = "mutated"
	s.Tools[0].InputSchema[2] = 'X'
	if got[0].Args[0] != "first" || strings.Contains(string(got[0].Tools[0].InputSchema), "X") {
		t.Fatal("config mutable")
	}
	for _, bad := range []MCPServer{{Name: "bad-name"}, {Name: "x", Command: os.Args[0], Dir: t.TempDir()}, {Name: "x", Command: os.Args[0], Dir: t.TempDir(), Tools: []plugins.Tool{{Name: "x", InputSchema: json.RawMessage(`[]`)}}}} {
		if _, err := prepareMCP([]MCPServer{bad}); err == nil {
			t.Fatal("bad capability accepted")
		}
	}
}

func TestMCPCancelRunningCallCleansHomeAndDoesNotReplay(t *testing.T) {
	e, s, task, marker := mcpFixture(t, `{"hang":true}`, time.Minute, 10*time.Second)
	a := awaitApproval(t, e, task.ID)
	if _, err := e.DecideApproval(context.Background(), task.ID, ApprovalDecision{ID: a.ID, Revision: a.Revision, Digest: a.Digest, Allow: true}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	var home []byte
	for time.Now().Before(deadline) {
		home, _ = os.ReadFile(marker + ".home")
		if len(home) > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(home) == 0 {
		t.Fatal("server did not start")
	}
	if err := e.Cancel(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	wait(t, e, task.ID)
	if _, err := os.Stat(string(home)); !os.IsNotExist(err) {
		t.Fatal("cancelled process home not removed")
	}
	got, _ := s.GetApproval(context.Background(), task.ID, a.ID)
	if got.Status != "unknown" {
		t.Fatalf("status %s", got.Status)
	}
	content, _ := os.ReadFile(marker)
	if string(content) != "start\n" {
		t.Fatal("cancelled call replayed")
	}
}

func TestMCPAndCodingShareWorkspaceGate(t *testing.T) {
	dir := t.TempDir()
	coding, err := prepareCoding(&CodingConfig{Workspace: dir, CodexPath: os.Args[0]})
	if err != nil {
		t.Fatal(err)
	}
	servers, err := prepareMCP([]MCPServer{{Name: "local", Command: os.Args[0], Dir: dir, Tools: []plugins.Tool{{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}}}})
	if err != nil {
		t.Fatal(err)
	}
	if coding.workspaceGate != servers[0].workspaceGate {
		t.Fatal("MCP and coding do not share workspace serialization")
	}
}

func TestMCPExpiresWaitingForWorkspaceWithoutStarting(t *testing.T) {
	e, s, task, marker := mcpFixture(t, `{}`, 200*time.Millisecond, time.Second)
	a := awaitApproval(t, e, task.ID)
	gate := e.cfg.MCP[0].workspaceGate
	gate <- struct{}{}
	defer func() { <-gate }()
	if _, err := e.DecideApproval(context.Background(), task.ID, ApprovalDecision{ID: a.ID, Revision: a.Revision, Digest: a.Digest, Allow: true}); err != nil {
		t.Fatal(err)
	}
	wait(t, e, task.ID)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("started while workspace held")
	}
	got, _ := s.GetApproval(context.Background(), task.ID, a.ID)
	if got.Status == "executing" || got.Status == "succeeded" {
		t.Fatal("expired queued approval consumed")
	}
}

func TestMCPConcurrentCallsSerializeWorkspace(t *testing.T) {
	e, _, first, marker := mcpFixture(t, `{"hang":true}`, time.Minute, 10*time.Second)
	a := awaitApproval(t, e, first.ID)
	if _, err := e.DecideApproval(context.Background(), first.ID, ApprovalDecision{ID: a.ID, Revision: a.Revision, Digest: a.Digest, Allow: true}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	second, err := e.Create(context.Background(), "second")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Start(context.Background(), second.ID, "use tool"); err != nil {
		t.Fatal(err)
	}
	b := awaitApproval(t, e, second.ID)
	if _, err = e.DecideApproval(context.Background(), second.ID, ApprovalDecision{ID: b.ID, Revision: b.Revision, Digest: b.Digest, Allow: true}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	raw, _ := os.ReadFile(marker)
	if string(raw) != "start\n" {
		t.Fatalf("concurrent MCP startup: %q", raw)
	}
	if err = e.Cancel(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	wait(t, e, first.ID)
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		raw, _ = os.ReadFile(marker)
		if string(raw) == "start\nstart\n" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if string(raw) != "start\nstart\n" {
		t.Fatal("workspace gate not released after cancellation")
	}
	if err = e.Cancel(context.Background(), second.ID); err != nil {
		t.Fatal(err)
	}
	wait(t, e, second.ID)
}

func TestMCPExposedNameCollisionsRejected(t *testing.T) {
	dir := t.TempDir()
	_, err := prepareMCP([]MCPServer{
		{Name: "a", Command: os.Args[0], Dir: dir, Tools: []plugins.Tool{{Name: "b__c", InputSchema: json.RawMessage(`{"type":"object"}`)}}},
		{Name: "a__b", Command: os.Args[0], Dir: dir, Tools: []plugins.Tool{{Name: "c", InputSchema: json.RawMessage(`{"type":"object"}`)}}},
	})
	if err == nil {
		t.Fatal("colliding exposed capability names accepted")
	}
}
