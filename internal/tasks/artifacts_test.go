package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopi/internal/agent"
	"gopi/internal/artifacts"
	"gopi/internal/session"
)

func artifactFixture(t *testing.T, request string, setup ...func(string)) (*Engine, *session.Store, Task, *artifacts.Service, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	s, err := session.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	svc, err := artifacts.New(root, s)
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	e, err := New(context.Background(), s, Config{Artifacts: svc, Factory: func(string) (*agent.Agent, error) {
		return &agent.Agent{Model: modelFunc(func(_ context.Context, m []agent.Message, defs []agent.ToolDefinition) (agent.Message, error) {
			if len(defs) != 1 || defs[0].Name != "artifact_publish" {
				return agent.Message{}, errors.New("missing scoped artifact tool")
			}
			if m[len(m)-1].Role == "tool" {
				return agent.Message{Content: "done"}, nil
			}
			return agent.Message{ToolCalls: []agent.ToolCall{{ID: "publish", Name: "artifact_publish", Arguments: json.RawMessage(request)}}}, nil
		})}, nil
	}})
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close(); s.Close() })
	task, err := e.Create(context.Background(), "deliverable")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, task.ID)
	if err = os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "report.html")
	if err = os.WriteFile(file, []byte("<script>never execute</script>"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.List(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	for _, f := range setup {
		f(dbPath)
	}
	if _, err = e.Submit(context.Background(), task.ID, "artifact-owner", "artifact-request", "prepare report", nil); err != nil {
		t.Fatal(err)
	}
	return e, s, task, svc, file
}
func TestArtifactPublishExactApproval(t *testing.T) {
	for _, mode := range []string{"approve", "deny", "changed", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			e, s, task, svc, file := artifactFixture(t, `{"path":"report.html"}`)
			a := awaitApproval(t, e, task.ID)
			items, err := svc.List(context.Background(), task.ID)
			if err != nil || len(items) != 0 {
				t.Fatal("registered before approval", items, err)
			}
			if strings.Contains(string(a.Action.Payload), filepath.Dir(file)) || !strings.Contains(string(a.Action.Payload), "sha256") {
				t.Fatal("unsafe or incomplete approval", a)
			}
			other, err := e.Create(context.Background(), "other")
			if err != nil {
				t.Fatal(err)
			}
			decision := ApprovalDecision{ID: a.ID, Revision: a.Revision, Digest: a.Digest, Allow: mode != "deny"}
			if _, err = e.DecideApproval(context.Background(), other.ID, decision); !errors.Is(err, session.ErrApprovalUnavailable) {
				t.Fatal("cross-task decision", err)
			}
			if mode == "changed" {
				if err = os.WriteFile(file, []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "cancel" {
				if err = e.Cancel(context.Background(), task.ID); err != nil {
					t.Fatal(err)
				}
			} else if _, err = e.DecideApproval(context.Background(), task.ID, decision); err != nil {
				t.Fatal(err)
			}
			wait(t, e, task.ID)
			items, err = svc.List(context.Background(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "approve" {
				if len(items) != 0 {
					t.Fatal("unexpected registration", items)
				}
				return
			}
			if len(items) != 1 {
				t.Fatal(items)
			}
			_, content, err := svc.Download(context.Background(), task.ID, items[0].ID)
			if err != nil || string(content) != "<script>never execute</script>" {
				t.Fatal(string(content), err)
			}
			if _, _, err = svc.Download(context.Background(), other.ID, items[0].ID); err == nil {
				t.Fatal("cross-task download")
			}
			final, err := s.GetApproval(context.Background(), task.ID, a.ID)
			if err != nil || final.Status != "succeeded" {
				t.Fatal(final, err)
			}
			got, err := e.Get(context.Background(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			result := got.Messages[len(got.Messages)-2].Content
			if !strings.Contains(result, `"status":"registered"`) || !strings.Contains(result, "Not executed, downloaded, or shared externally.") || strings.Contains(result, filepath.Dir(file)) {
				t.Fatal(result)
			}
			if _, err = e.DecideApproval(context.Background(), task.ID, decision); !errors.Is(err, session.ErrApprovalUnavailable) {
				t.Fatal("replayed", err)
			}
		})
	}
}
func TestArtifactPublishRejectsPathsAndForeignFields(t *testing.T) {
	for _, raw := range []string{`{"path":"../report.html"}`, `{"path":"/etc/passwd"}`, `{"path":"report.html","task_id":"other"}`, `{"path":"report.html"} {}`, `{"path":".hidden"}`} {
		t.Run(raw, func(t *testing.T) {
			e, _, task, svc, _ := artifactFixture(t, raw)
			wait(t, e, task.ID)
			items, err := svc.List(context.Background(), task.ID)
			if err != nil || len(items) != 0 {
				t.Fatal(items, err)
			}
			approvals, err := e.Approvals(context.Background(), task.ID)
			if err != nil || len(approvals) != 0 {
				t.Fatal(approvals, err)
			}
		})
	}
}

func TestArtifactPublishFailuresAndExpiry(t *testing.T) {
	for _, mode := range []string{"expiry", "claim-write", "registration-write", "finish-write"} {
		t.Run(mode, func(t *testing.T) {
			var db *sql.DB
			e, _, task, svc, _ := artifactFixture(t, `{"path":"report.html"}`, func(dbPath string) {
				var err error
				db, err = sql.Open("sqlite", dbPath)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Close() })
				var stmt string
				switch mode {
				case "claim-write":
					stmt = `CREATE TRIGGER fail_claim BEFORE UPDATE OF status ON approvals WHEN NEW.status='executing' BEGIN SELECT RAISE(ABORT,'claim failure'); END`
				case "registration-write":
					stmt = `CREATE TRIGGER fail_registration BEFORE INSERT ON artifacts BEGIN SELECT RAISE(ABORT,'registration failure'); END`
				case "finish-write":
					stmt = `CREATE TRIGGER fail_finish BEFORE UPDATE OF status ON approvals WHEN NEW.status='succeeded' BEGIN SELECT RAISE(ABORT,'finish failure'); END`
				}
				if stmt != "" {
					if _, err = db.Exec(stmt); err != nil {
						t.Fatal(err)
					}
				}
			})
			a := awaitApproval(t, e, task.ID)
			if mode == "expiry" {
				if _, err := db.Exec("UPDATE approvals SET expires_at=? WHERE id=?", time.Now().Add(-time.Second).UnixNano(), a.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := e.DecideApproval(context.Background(), task.ID, ApprovalDecision{ID: a.ID, Revision: a.Revision, Digest: a.Digest, Allow: true}); err != nil {
					t.Fatal(err)
				}
			}
			wait(t, e, task.ID)
			items, err := svc.List(context.Background(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if mode == "finish-write" {
				want = 1
			}
			if len(items) != want {
				t.Fatalf("mode %s: got %d registrations, want %d", mode, len(items), want)
			}
			got, err := e.Get(context.Background(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "expiry" {
				if got.Status != "failed" || !strings.Contains(got.Runs[0].Error, "outcome unknown") {
					t.Fatalf("uncertain write presented as success: %+v", got.Runs)
				}
			}
			// Replay of the original user decision never repeats dispatch, even when
			// the registration committed but recording its completion failed.
			if _, err = e.DecideApproval(context.Background(), task.ID, ApprovalDecision{ID: a.ID, Revision: a.Revision, Digest: a.Digest, Allow: true}); err == nil {
				t.Fatal("replayed decision")
			}
		})
	}
}

func TestArtifactPublishSubmissionRetryDoesNotReplay(t *testing.T) {
	ctx := context.Background()
	e, _, task, svc, _ := artifactFixture(t, `{"path":"report.html"}`)
	a := awaitApproval(t, e, task.ID)
	first, err := e.Submit(ctx, task.ID, "artifact-owner", "artifact-request", "prepare report", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.DecideApproval(ctx, task.ID, ApprovalDecision{ID: a.ID, Revision: a.Revision, Digest: a.Digest, Allow: true}); err != nil {
		t.Fatal(err)
	}
	wait(t, e, task.ID)
	retry, err := e.Submit(ctx, task.ID, "artifact-owner", "artifact-request", "prepare report", nil)
	if err != nil || retry.ID != first.ID {
		t.Fatal(retry, err)
	}
	approvals, err := e.Approvals(ctx, task.ID)
	if err != nil || len(approvals) != 1 {
		t.Fatal("retry repeated approval", approvals, err)
	}
	items, err := svc.List(ctx, task.ID)
	if err != nil || len(items) != 1 {
		t.Fatal("retry repeated publication", items, err)
	}
	got, err := e.Get(ctx, task.ID)
	if err != nil || len(got.Runs) != 1 {
		t.Fatal("retry repeated run", got.Runs, err)
	}
}
