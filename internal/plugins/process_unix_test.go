//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestStartupTimeoutKillsDescendants(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	_, err := StartScoped(context.Background(), Config{Command: "/bin/sh", Args: []string{"-c", `sleep 30 & echo $! > "$1"; wait`, "sh", pidFile}, Timeout: 100 * time.Millisecond})
	if err == nil {
		t.Fatal("non-MCP program initialized")
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return
		}
		// On Linux a killed grandchild can remain a zombie until PID 1 reaps it.
		if stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil && strings.Contains(string(stat), ") Z ") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("descendant survived MCP startup timeout")
}

func TestNormalExitKillsDescendantsHoldingPipes(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	start := time.Now()
	_, err := StartScoped(context.Background(), Config{Command: "/bin/sh", Args: []string{"-c", `sleep 30 & echo $! > "$1"; exit 0`, "sh", pidFile}, Timeout: 2 * time.Second})
	if err == nil {
		t.Fatal("non-MCP program initialized")
	}
	if time.Since(start) > time.Second {
		t.Fatal("normal exit blocked on descendant pipes")
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return
		}
		if stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil && strings.Contains(string(stat), ") Z ") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("descendant survived normal MCP exit")
}
