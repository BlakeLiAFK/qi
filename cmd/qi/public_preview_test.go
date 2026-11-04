package main

import (
	"context"
	"gopi/internal/tasks"
	"gopi/internal/testutil"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicPreviewDaemonWiring(t *testing.T) {
	path := filepath.Join(testutil.PrivateDir(t), "tasks.db")
	cfg := daemonConfig{database: path, origin: "http://127.0.0.1:8686", token: testToken, publicPreview: true}
	if _, _, err := openDaemon(context.Background(), cfg); err == nil {
		t.Fatal("cleartext preview accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("invalid auth config created database", err)
	}
	cfg.origin = "https://qi.example"
	h, closeAll, err := openDaemon(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	r := httptest.NewRequest("GET", cfg.origin+"/api/tasks", nil)
	r.SetBasicAuth("qi", testToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("preview accepts Basic", w.Code)
	}
	r = httptest.NewRequest("POST", cfg.origin+"/api/login", strings.NewReader(`{"token":"`+testToken+`"}`))
	r.Header.Set("Origin", cfg.origin)
	r.Header.Set("X-Qi-Request", "1")
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	r = httptest.NewRequest("GET", cfg.origin+"/api/tasks", nil)
	r.AddCookie(w.Result().Cookies()[0])
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
}

func TestPublicPreviewRejectsProcessToolsBeforeSideEffects(t *testing.T) {
	for _, args := range [][]string{
		{"-public-preview", "-enable-workers"},
		{"-public-preview", "-mcp-tools", "/must-not-read-missing-config.json"},
		{"-public-preview", "-enable-workers", "-mcp-tools", "/must-not-read-missing-config.json"},
	} {
		var out strings.Builder
		err := runServer(context.Background(), args, &out, func(string) string { return "" })
		if err == nil || !strings.Contains(err.Error(), "OS isolation") {
			t.Fatalf("unsafe flags not rejected first: %v", err)
		}
	}
	for _, kind := range []string{"coding", "mcp"} {
		path := filepath.Join(testutil.PrivateDir(t), "must-not-create.db")
		cfg := daemonConfig{database: path, origin: "https://qi.example", token: testToken, publicPreview: true}
		if kind == "coding" {
			cfg.coding = &tasks.CodingConfig{Workspace: "/must-not-inspect"}
		} else {
			cfg.mcp = []tasks.MCPServer{{Name: "fixture", Command: "/must-not-run"}}
		}
		if _, closeAll, err := openDaemon(context.Background(), cfg); err == nil {
			closeAll()
			t.Fatalf("unsafe %s config accepted", kind)
		} else if !strings.Contains(err.Error(), "OS isolation") {
			t.Fatalf("wrong rejection: %v", err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("unsafe config touched database: %v", err)
		}
	}
}
func TestPublicPreviewAllowsAuthenticatedArtifactsAndQuestions(t *testing.T) {
	root, err := filepath.EvalSymlinks(testutil.PrivateDir(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg := daemonConfig{database: filepath.Join(testutil.PrivateDir(t), "tasks.db"), origin: "https://qi.example", token: testToken, publicPreview: true, artifactDir: root, questions: true}
	h, closeAll, err := openDaemon(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", cfg.origin+"/api/tasks", nil))
	if w.Code != 401 {
		t.Fatalf("artifact/question opt-ins weakened auth: %d", w.Code)
	}
}
