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

func TestArgumentsEnforceExplicitSafetyModes(t *testing.T) {
	for _, name := range []string{"codex", "claude"} {
		args, err := Arguments(name, "--dangerously-skip-permissions; $(touch /tmp/nope)")
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(args[:len(args)-1], " ")
		if strings.Contains(joined, "dangerously") || strings.Contains(joined, "bypassPermissions") || strings.Contains(joined, "full-auto") {
			t.Fatalf("unsafe flags: %v", args)
		}
		if args[len(args)-2] != "--" {
			t.Fatalf("prompt lacks option separator: %v", args)
		}
		if name == "codex" && !strings.Contains(joined, "--ask-for-approval never exec --sandbox workspace-write") {
			t.Fatalf("missing sandbox policy: %v", args)
		}
		if name == "claude" && !strings.Contains(joined, "--permission-mode default") {
			t.Fatalf("missing permission policy: %v", args)
		}
	}
}

func TestWorkerEnvironmentDoesNotInheritSecrets(t *testing.T) {
	t.Setenv("QI_WEB_TOKEN", "qi-parent-secret")
	t.Setenv("OPENAI_API_KEY", "model-parent-secret")
	t.Setenv("ANTHROPIC_API_KEY", "parent-anthropic-secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "cloud-parent-secret")
	t.Setenv("LD_PRELOAD", "should-not-be-inherited")
	parentHome := t.TempDir()
	t.Setenv("HOME", parentHome)
	p := fake(t, "printf '%s\\n' \"$HOME\"; env\n")
	m := New(Config{Enabled: true, CodexPath: p, Credentials: map[string]string{"CODEX_API_KEY": "explicit-worker-key"}})
	v, err := m.Start(context.Background(), Request{"codex", "x", t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	v = await(t, m, v.ID)
	m.Close()
	for _, secret := range []string{"qi-parent-secret", "model-parent-secret", "parent-anthropic-secret", "cloud-parent-secret", "should-not-be-inherited", parentHome} {
		if strings.Contains(v.Output, secret) {
			t.Fatalf("inherited protected value: %s", secret)
		}
	}
	if !strings.Contains(v.Output, "CODEX_API_KEY=explicit-worker-key") {
		t.Fatal("explicit credential missing")
	}
	home := strings.Split(v.Output, "\n")[0]
	if home == "" || home == parentHome {
		t.Fatal("private worker home missing")
	}
	if _, err = os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("temporary worker home not removed: %v", err)
	}
}

func TestWorkerCredentialsRejectDaemonTokens(t *testing.T) {
	for _, key := range []string{"QI_WEB_TOKEN", "OPENAI_API_KEY", "HOME", "LD_PRELOAD", "PATH"} {
		m := New(Config{Enabled: true, CodexPath: fake(t, "exit 0\n"), Credentials: map[string]string{key: "blocked"}})
		if _, err := m.Start(context.Background(), Request{"codex", "x", t.TempDir()}); err == nil {
			t.Fatalf("forbidden credential accepted: %s", key)
		}
		m.Close()
	}
}
