package webui

import (
	"context"
	"encoding/json"
	"gopi/internal/artifacts"
	"gopi/internal/session"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArtifactRoutesAuthenticatedScopedAndAttachmentOnly(t *testing.T) {
	ctx := context.Background()
	store, err := session.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task, err := store.Create(ctx, "one")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, task.ID), 0700)
	os.WriteFile(filepath.Join(root, task.ID, "unsafe.html"), []byte("<script>alert(1)</script>"), 0600)
	svc, err := artifacts.New(root, store)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Config{Origin: "http://127.0.0.1:8080", Token: token, Backend: &fakeBackend{}, Artifacts: svc})
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/tasks/" + task.ID + "/artifacts"
	origin := "http://127.0.0.1:8080"
	for _, p := range []string{base, base + "/x"} {
		if w := request(h, "GET", p, "", false, ""); w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	if w := request(h, "POST", base, `{"path":"unsafe.html"}`, true, ""); w.Code != 403 {
		t.Fatal("CSRF", w.Code)
	}
	w := request(h, "POST", base, `{"path":"unsafe.html"}`, true, origin)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var a artifacts.Artifact
	if err = json.Unmarshal(w.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	w = request(h, "GET", base+"/"+a.ID, "", true, "")
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatal(w.Code, w.Header())
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatal("missing sandbox")
	}
	w = request(h, "GET", base, "", true, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), root) || strings.Contains(w.Body.String(), `"path"`) {
		t.Fatal("path exposure", w.Body.String())
	}
	if w = request(h, "GET", "/api/tasks/other/artifacts/"+a.ID, "", true, ""); w.Code != 404 {
		t.Fatal("cross task", w.Code)
	}
	for _, p := range []string{"../unsafe.html", "/etc/passwd"} {
		b, _ := json.Marshal(map[string]string{"path": p})
		if w = request(h, "POST", base, string(b), true, origin); w.Code != 400 {
			t.Fatal("path", p, w.Code)
		}
	}
	os.WriteFile(filepath.Join(root, task.ID, "unsafe.html"), []byte("changed"), 0600)
	if w = request(h, "GET", base+"/"+a.ID, "", true, ""); w.Code != 404 {
		t.Fatal("changed download", w.Code)
	}
}

func TestArtifactResponseSlotsBoundSlowClients(t *testing.T) {
	for i := 0; i < cap(artifactResponses); i++ {
		artifactResponses <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(artifactResponses); i++ {
			<-artifactResponses
		}
	}()
	store, err := session.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc, err := artifacts.New(t.TempDir(), store)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Config{Origin: "http://127.0.0.1:8080", Token: token, Backend: &fakeBackend{}, Artifacts: svc})
	if err != nil {
		t.Fatal(err)
	}
	if w := request(h, "GET", "/api/tasks/one/artifacts/id", "", true, ""); w.Code != 429 {
		t.Fatal("unbounded response buffers", w.Code)
	}
	if w := request(h, "GET", "/api/tasks/one/artifacts", "", true, ""); w.Code != 200 {
		t.Fatal("metadata list unnecessarily blocked", w.Code)
	}
}
