package workers

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func fake(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires POSIX")
	}
	p := filepath.Join(t.TempDir(), "fake")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return p
}
func await(t *testing.T, m *Manager, id string) Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		v, e := m.Get(id)
		if e != nil {
			t.Fatal(e)
		}
		if v.Status != "running" {
			return v
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("task did not finish")
	return Task{}
}
func TestAdapters(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) {
			p := fake(t, "printf '%s\\n' \"$@\"\n")
			m := New(Config{Enabled: true, CodexPath: p, ClaudePath: p})
			defer m.Close()
			v, e := m.Start(context.Background(), Request{Agent: agent, Prompt: "hello; $(false)", WorkDir: t.TempDir()})
			if e != nil {
				t.Fatal(e)
			}
			v = await(t, m, v.ID)
			if v.Status != "completed" || !strings.Contains(v.Output, "hello; $(false)") {
				t.Fatalf("%+v", v)
			}
		})
	}
}
func TestBoundedOutput(t *testing.T) {
	p := fake(t, "printf 123456789; printf abcdef >&2\n")
	m := New(Config{Enabled: true, CodexPath: p, MaxOutputBytes: 5})
	defer m.Close()
	v, e := m.Start(context.Background(), Request{"codex", "x", t.TempDir()})
	if e != nil {
		t.Fatal(e)
	}
	v = await(t, m, v.ID)
	if len(v.Output) != 5 || !v.Truncated {
		t.Fatalf("%+v", v)
	}
}
func TestCancelAndTimeout(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		p := fake(t, "sleep 60\n")
		c := Config{Enabled: true, CodexPath: p}
		if timeout {
			c.Timeout = 30 * time.Millisecond
		}
		m := New(c)
		v, e := m.Start(context.Background(), Request{"codex", "x", t.TempDir()})
		if e != nil {
			t.Fatal(e)
		}
		if !timeout {
			if e = m.Cancel(v.ID); e != nil {
				t.Fatal(e)
			}
		}
		v = await(t, m, v.ID)
		m.Close()
		if v.Status != "cancelled" {
			t.Fatalf("%+v", v)
		}
	}
}
func TestFailureAndValidation(t *testing.T) {
	m := New(Config{})
	if _, e := m.Start(context.Background(), Request{}); e == nil {
		t.Fatal("disabled start allowed")
	}
	m = New(Config{Enabled: true, CodexPath: fake(t, "exit 7\n"), MaxTasks: 1})
	defer m.Close()
	if _, e := m.Start(context.Background(), Request{"other", "x", t.TempDir()}); e == nil {
		t.Fatal("unknown agent allowed")
	}
	v, e := m.Start(context.Background(), Request{"codex", "x", t.TempDir()})
	if e != nil {
		t.Fatal(e)
	}
	v = await(t, m, v.ID)
	if v.Status != "failed" {
		t.Fatalf("%+v", v)
	}
	if _, e = m.Start(context.Background(), Request{"codex", "x", t.TempDir()}); e == nil {
		t.Fatal("capacity not enforced")
	}
	if e = m.Forget(v.ID); e != nil {
		t.Fatal(e)
	}
	m.Close()
	if _, e = m.Start(context.Background(), Request{"codex", "x", t.TempDir()}); e == nil {
		t.Fatal("closed start allowed")
	}
}
