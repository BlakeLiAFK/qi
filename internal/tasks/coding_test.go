package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"gopi/internal/agent"
	"gopi/internal/session"
)

func codingFixture(t *testing.T, body string, ttl time.Duration) (*Engine, *session.Store, Task, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake CLI")
	}
	workspace := t.TempDir()
	executable := filepath.Join(t.TempDir(), "fake-codex")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := session.Open(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := New(context.Background(), store, Config{Coding: &CodingConfig{Workspace: workspace, CodexPath: executable, ApprovalTTL: ttl}, Factory: func(string) (*agent.Agent, error) {
		return &agent.Agent{Model: modelFunc(func(_ context.Context, m []agent.Message, defs []agent.ToolDefinition) (agent.Message, error) {
			if len(defs) != 1 || defs[0].Name != "coding_run" {
				return agent.Message{}, errors.New("missing scoped coding tool")
			}
			if m[len(m)-1].Role == "tool" {
				return agent.Message{Content: "finished"}, nil
			}
			return agent.Message{ToolCalls: []agent.ToolCall{{ID: "call-1", Name: "coding_run", Arguments: json.RawMessage(`{"agent":"codex","prompt":"make the requested change"}`)}}}, nil
		})}, nil
	}})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Close(); store.Close() })
	task, err := engine.Create(context.Background(), "coding")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = engine.Start(context.Background(), task.ID, "please code"); err != nil {
		t.Fatal(err)
	}
	return engine, store, task, workspace
}
func awaitApproval(t *testing.T, e *Engine, taskID string) Approval {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		list, err := e.Approvals(context.Background(), taskID)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) > 0 {
			return list[0]
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("approval did not appear")
	return Approval{}
}
func TestCodingApprovalRunsOnce(t *testing.T) {
	e, s, task, wd := codingFixture(t, "printf invoked >> marker\nprintf '%s\\n' \"$@\"\n", time.Minute)
	approval := awaitApproval(t, e, task.ID)
	marker := filepath.Join(wd, "marker")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("executed before approval")
	}
	if !strings.Contains(string(approval.Action.Payload), wd) || !strings.Contains(string(approval.Action.Payload), "make the requested change") {
		t.Fatal("approval missing consequential inputs")
	}
	other, _ := e.Create(context.Background(), "other")
	decision := ApprovalDecision{ID: approval.ID, Revision: approval.Revision, Digest: approval.Digest, Allow: true}
	if _, err := e.DecideApproval(context.Background(), other.ID, decision); !errors.Is(err, session.ErrApprovalUnavailable) {
		t.Fatalf("cross-task approval accepted: %v", err)
	}
	if _, err := e.DecideApproval(context.Background(), task.ID, decision); err != nil {
		t.Fatal(err)
	}
	wait(t, e, task.ID)
	content, err := os.ReadFile(marker)
	if err != nil || string(content) != "invoked" {
		t.Fatalf("bad invocation %q %v", content, err)
	}
	final, _ := s.GetApproval(context.Background(), task.ID, approval.ID)
	if final.Status != "succeeded" {
		t.Fatalf("%+v", final)
	}
	if _, err := e.DecideApproval(context.Background(), task.ID, decision); !errors.Is(err, session.ErrApprovalUnavailable) {
		t.Fatal("replay accepted")
	}
	if _, err := s.ClaimApproval(context.Background(), task.ID, approval.ID, final.Revision, approval.Action); !errors.Is(err, session.ErrApprovalUnavailable) {
		t.Fatal("second claim accepted")
	}
	got, _ := e.Get(context.Background(), task.ID)
	if got.Status != "completed" {
		t.Fatalf("%+v", got)
	}
	if !strings.Contains(got.Messages[len(got.Messages)-2].Content, "--sandbox") {
		t.Fatal("worker output not persisted")
	}
}
func TestCodingDenyAndExpiryNeverDispatch(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "deny", true: "expiry"}[expired], func(t *testing.T) {
			ttl := time.Minute
			if expired {
				ttl = 100 * time.Millisecond
			}
			e, _, task, wd := codingFixture(t, "touch marker\n", ttl)
			a := awaitApproval(t, e, task.ID)
			if !expired {
				if _, err := e.DecideApproval(context.Background(), task.ID, ApprovalDecision{ID: a.ID, Revision: a.Revision, Digest: a.Digest, Allow: false}); err != nil {
					t.Fatal(err)
				}
			}
			wait(t, e, task.ID)
			if _, err := os.Stat(filepath.Join(wd, "marker")); !os.IsNotExist(err) {
				t.Fatal("denied/expired operation executed")
			}
		})
	}
}
func TestCodingFailedProcessIsUnknown(t *testing.T) {
	e, s, task, wd := codingFixture(t, "touch marker\nexit 7\n", time.Minute)
	a := awaitApproval(t, e, task.ID)
	if _, err := e.DecideApproval(context.Background(), task.ID, ApprovalDecision{ID: a.ID, Revision: a.Revision, Digest: a.Digest, Allow: true}); err != nil {
		t.Fatal(err)
	}
	wait(t, e, task.ID)
	if _, err := os.Stat(filepath.Join(wd, "marker")); err != nil {
		t.Fatal(err)
	}
	a, _ = s.GetApproval(context.Background(), task.ID, a.ID)
	if a.Status != "unknown" {
		t.Fatalf("partial effect reported as %s", a.Status)
	}
	got, _ := e.Get(context.Background(), task.ID)
	if got.Status != "failed" {
		t.Fatalf("run status %s", got.Status)
	}
}
func TestCodingWorkspaceReplacementBlocked(t *testing.T) {
	e, _, task, wd := codingFixture(t, "touch marker\n", time.Minute)
	a := awaitApproval(t, e, task.ID)
	if err := os.Rename(wd, wd+"-old"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(wd + "-old") })
	if err := os.Mkdir(wd, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := e.DecideApproval(context.Background(), task.ID, ApprovalDecision{ID: a.ID, Revision: a.Revision, Digest: a.Digest, Allow: true}); err != nil {
		t.Fatal(err)
	}
	wait(t, e, task.ID)
	if _, err := os.Stat(filepath.Join(wd, "marker")); !os.IsNotExist(err) {
		t.Fatal("replaced workspace accepted")
	}
}

func TestCodingCancellationStopsProcessAndDoesNotReplay(t *testing.T) {
	e, s, task, wd := codingFixture(t, "touch marker\nsleep 60\ntouch too-late\n", time.Minute)
	a := awaitApproval(t, e, task.ID)
	if _, err := e.DecideApproval(context.Background(), task.ID, ApprovalDecision{ID: a.ID, Revision: a.Revision, Digest: a.Digest, Allow: true}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(wd, "marker")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("process did not start")
		}
		time.Sleep(time.Millisecond)
	}
	if err := e.Cancel(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	wait(t, e, task.ID)
	if _, err := os.Stat(filepath.Join(wd, "too-late")); !os.IsNotExist(err) {
		t.Fatal("cancelled process continued")
	}
	a, _ = s.GetApproval(context.Background(), task.ID, a.ID)
	if a.Status != "unknown" {
		t.Fatalf("cancelled effect status %s", a.Status)
	}
	got, _ := e.Get(context.Background(), task.ID)
	if got.Status != "cancelled" {
		t.Fatalf("run status %s", got.Status)
	}
	if _, err := s.ClaimApproval(context.Background(), task.ID, a.ID, a.Revision, a.Action); !errors.Is(err, session.ErrApprovalUnavailable) {
		t.Fatal("cancelled effect replayed")
	}
}

func TestCodingStrictArgumentsAndConfig(t *testing.T) {
	if _, err := prepareCoding(&CodingConfig{Workspace: t.TempDir(), Timeout: 2 * time.Hour}); err == nil {
		t.Fatal("unbounded runtime accepted")
	}
	if _, err := prepareCoding(&CodingConfig{}); err == nil {
		t.Fatal("empty workspace accepted")
	}
	e, _ := fixture(t, func(string) (*agent.Agent, error) { return &agent.Agent{}, nil })
	cfg, err := prepareCoding(&CodingConfig{Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	tool := codingTool{engine: e, config: cfg}
	for _, raw := range []string{`{"agent":"codex","prompt":"x","work_dir":"/"}`, `{"agent":"shell","prompt":"x"}`, `{"agent":"codex","prompt":""}`} {
		if _, err := tool.Execute(context.Background(), json.RawMessage(raw)); err == nil {
			t.Fatalf("unsafe input accepted: %s", raw)
		}
	}
}

func TestCodingCancelPendingRevokesApproval(t *testing.T) {
	e, s, task, wd := codingFixture(t, "touch marker\n", time.Minute)
	a := awaitApproval(t, e, task.ID)
	if err := e.Cancel(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	wait(t, e, task.ID)
	a, _ = s.GetApproval(context.Background(), task.ID, a.ID)
	if a.Status != "denied" {
		t.Fatalf("orphan approval status %s", a.Status)
	}
	if _, err := e.DecideApproval(context.Background(), task.ID, ApprovalDecision{ID: a.ID, Revision: a.Revision, Digest: a.Digest, Allow: true}); !errors.Is(err, session.ErrApprovalUnavailable) {
		t.Fatal("cancelled run approved")
	}
	if _, err := os.Stat(filepath.Join(wd, "marker")); !os.IsNotExist(err) {
		t.Fatal("cancelled pending request executed")
	}
}

func TestCodingSerializesApprovedWorkspaceJobs(t *testing.T) {
	e, _, first, wd := codingFixture(t, "echo start >> marker\nsleep 0.15\necho end >> marker\n", time.Minute)
	second, err := e.Create(context.Background(), "second coding task")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Start(context.Background(), second.ID, "code too"); err != nil {
		t.Fatal(err)
	}
	firstApproval := awaitApproval(t, e, first.ID)
	secondApproval := awaitApproval(t, e, second.ID)
	// Both requests reached approval: a pending decision does not monopolize the
	// workspace. Once approved, their effects must be serialized.
	for _, a := range []Approval{firstApproval, secondApproval} {
		if _, err = e.DecideApproval(context.Background(), a.TaskID, ApprovalDecision{ID: a.ID, Revision: a.Revision, Digest: a.Digest, Allow: true}); err != nil {
			t.Fatal(err)
		}
	}
	wait(t, e, first.ID)
	wait(t, e, second.ID)
	raw, err := os.ReadFile(filepath.Join(wd, "marker"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "start\nend\nstart\nend\n" {
		t.Fatalf("workspace jobs overlapped: %s", raw)
	}
}
