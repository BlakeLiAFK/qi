package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopi/internal/artifacts"
	"gopi/internal/tasks"
)

func TestDaemonArtifactsExplicitRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("secure Unix artifact fixture")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()
	cfg := daemonConfig{database: filepath.Join(t.TempDir(), "tasks.db"), model: "fixture", origin: origin, token: testToken, artifactDir: root}
	handler, closeAll, err := openDaemon(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	server.Config.Handler = handler
	server.Start()
	defer server.Close()
	client, err := newDaemonClient(origin, testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer client.http.CloseIdleConnections()
	var task tasks.Task
	if err = client.request(context.Background(), "POST", "/api/tasks", map[string]string{"title": "deliverable fixture"}, &task); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(root, task.ID), 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, task.ID, "report.html")
	content := "<html>fixture report</html>"
	if err = os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	var a artifacts.Artifact
	if err = client.request(context.Background(), "POST", "/api/tasks/"+task.ID+"/artifacts", map[string]string{"path": "report.html"}, &a); err != nil {
		t.Fatal(err)
	}
	url := origin + "/api/tasks/" + task.ID + "/artifacts/" + a.ID
	req, _ := http.NewRequest("GET", url, nil)
	req.SetBasicAuth("qi", testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || string(data) != content || resp.Header.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("unsafe/incorrect download: %d %v", resp.StatusCode, resp.Header)
	}
	if err = os.WriteFile(file, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = client.request(context.Background(), "GET", "/api/tasks/"+task.ID+"/artifacts/"+a.ID, nil, nil); err == nil {
		t.Fatal("modified artifact served")
	}
	if err = client.request(context.Background(), "POST", "/api/tasks/"+task.ID+"/artifacts", map[string]string{"path": "../outside"}, nil); err == nil {
		t.Fatal("traversal accepted")
	}
}
func TestDaemonArtifactRootValidationAndDefaultOff(t *testing.T) {
	cfg := daemonConfig{database: filepath.Join(t.TempDir(), "tasks.db"), model: "fixture", origin: "http://127.0.0.1:8686", token: testToken, artifactDir: filepath.Join(t.TempDir(), "missing")}
	if _, closeAll, err := openDaemon(context.Background(), cfg); err == nil {
		closeAll()
		t.Fatal("missing artifact root accepted")
	}
	cfg.artifactDir = ""
	handler, closeAll, err := openDaemon(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()
	create := httptest.NewRequest("POST", cfg.origin+"/api/tasks", strings.NewReader(`{"title":"fixture"}`))
	create.SetBasicAuth("qi", testToken)
	create.Header.Set("Origin", cfg.origin)
	create.Header.Set("X-Qi-Request", "1")
	create.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, create)
	var task tasks.Task
	if err = json.Unmarshal(w.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", cfg.origin+"/api/tasks/"+task.ID+"/artifacts", strings.NewReader(`{"path":"report.txt"}`))
	req.SetBasicAuth("qi", testToken)
	req.Header = create.Header.Clone()
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 404 {
		t.Fatalf("artifacts enabled by default: %d", w.Code)
	}
}
