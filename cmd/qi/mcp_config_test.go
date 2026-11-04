package main

import (
	"context"
	"encoding/json"
	"gopi/internal/testutil"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopi/internal/plugins"
	"gopi/internal/tasks"
)

func TestMCPToolsConfigStrictAndBounded(t *testing.T) {
	for _, raw := range []string{
		`null`, `[]`, `{}","`,
		`[{"name":"x","command":"test","dir":"/tmp","tools":[],"env":["SECRET=value"]}]`,
		`[{"name":"x","command":"test","dir":"/tmp","tools":[],"credentials":{}}]`,
		`[{"name":"x","command":"test","dir":"/tmp","tools":[],"timeout":5}]`,
		`[{"name":"x","command":"test","dir":"/tmp","tools":[{"name":"a","inputSchema":{},"env":[]}]}]`,
		`[{}] []`, strings.Repeat(" ", 1<<20) + `[{}]`,
	} {
		path := filepath.Join(testutil.PrivateDir(t), "mcp.json")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readMCPTools(path); err == nil {
			t.Errorf("accepted invalid config of length %d", len(raw))
		}
	}
}
func TestMCPToolsStartupValidationDoesNotExecute(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixture")
	}
	dir := testutil.PrivateDir(t)
	command := filepath.Join(dir, "fake-mcp")
	if err := os.WriteFile(command, []byte("#!/bin/sh\ntouch was-started\n"), 0700); err != nil {
		t.Fatal(err)
	}
	servers := []tasks.MCPServer{{Name: "fixture", Command: command, Dir: dir, Tools: []plugins.Tool{{Name: "echo", Description: "fixture tool", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)}}}}
	raw, err := json.Marshal(servers)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "tools.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := readMCPTools(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := daemonConfig{database: filepath.Join(dir, "tasks.db"), model: "fixture", origin: "http://127.0.0.1:8686", token: testToken, maxSteps: 3, mcp: loaded}
	_, closeAll, err := openDaemon(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	closeAll()
	if _, err = os.Stat(filepath.Join(dir, "was-started")); !os.IsNotExist(err) {
		t.Fatalf("MCP executed during startup: %v", err)
	}
	cfg.mcp[0].Command = filepath.Join(dir, "missing")
	if _, closeAll, err = openDaemon(context.Background(), cfg); err == nil {
		closeAll()
		t.Fatal("accepted missing executable")
	}
	cfg.mcp[0].Command = command
	cfg.mcp[0].Tools = nil
	if _, closeAll, err = openDaemon(context.Background(), cfg); err == nil {
		closeAll()
		t.Fatal("accepted empty static allowlist")
	}
}
