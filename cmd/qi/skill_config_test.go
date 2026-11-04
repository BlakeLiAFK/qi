package main

import (
	"bytes"
	"context"
	"encoding/json"
	"gopi/internal/testutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopi/internal/tasks"
)

func TestExplicitSkillRoots(t *testing.T) {
	catalog, err := discoverSkills(context.Background(), nil)
	if err != nil || catalog != nil {
		t.Fatal("skills must be disabled by default")
	}
	if _, err = discoverSkills(context.Background(), []string{filepath.Join(testutil.PrivateDir(t), "missing")}); err == nil {
		t.Fatal("missing root accepted")
	}
	var roots skillRootFlags
	if err = roots.Set(""); err == nil {
		t.Fatal("empty root accepted")
	}
}
func TestAttachSkillsApplyToOneTurnOnly(t *testing.T) {
	root := testutil.PrivateDir(t)
	body := "Use fixture terminology; do not execute tools."
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("---\nname: fixture\ndescription: Fixture reference\n---\n"+body), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := discoverSkills(context.Background(), []string{root})
	if err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tools []any `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if len(req.Tools) != 0 {
			t.Error("skills must not grant tool access")
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer provider.Close()
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()
	handler, closeAll, err := openDaemon(context.Background(), daemonConfig{database: filepath.Join(testutil.PrivateDir(t), "tasks.db"), model: "fixture", baseURL: provider.URL, origin: origin, token: testToken, skills: catalog})
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	server.Config.Handler = handler
	server.Start()
	defer server.Close()
	var out, diag bytes.Buffer
	env := func(k string) string {
		if k == "QI_WEB_TOKEN" {
			return testToken
		}
		return ""
	}
	if err = runAttach(context.Background(), []string{"-url", origin}, strings.NewReader("/skills\n/use-skills fixture\nfirst\nsecond\n/quit\n"), &out, &diag, env); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Fixture reference") || !strings.Contains(diag.String(), "next message only") {
		t.Fatalf("out=%s diag=%s", out.String(), diag.String())
	}
	client, err := newDaemonClient(origin, testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer client.http.CloseIdleConnections()
	var list []tasks.Task
	if err = client.request(context.Background(), "GET", "/api/tasks", nil, &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatal("expected one task")
	}
	task, err := client.get(context.Background(), list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(task.Messages) != 4 {
		t.Fatalf("messages=%+v", task.Messages)
	}
	if !strings.Contains(task.Messages[0].Content, body) || !strings.Contains(task.Messages[0].Content, "untrusted local content") {
		t.Fatal("selected reference not recorded")
	}
	if task.Messages[2].Content != "second" {
		t.Fatal("skill selection leaked into next turn")
	}
	var b bytes.Buffer
	if err = runAttach(context.Background(), []string{"-url", origin, "-skills", "fixture"}, strings.NewReader(""), &b, &b, env); err == nil {
		t.Fatal("persistent -skills without -p accepted")
	}
}
