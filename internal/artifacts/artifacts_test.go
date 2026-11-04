package artifacts_test

import (
	"context"
	"errors"
	"fmt"
	"gopi/internal/artifacts"
	"gopi/internal/session"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRegistrationBoundariesAndPersistence(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db := filepath.Join(t.TempDir(), "state.db")
	store, err := session.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task, err := store.Create(ctx, "one")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, task.ID)
	os.Mkdir(dir, 0700)
	os.WriteFile(filepath.Join(dir, "report.txt"), []byte("hello"), 0600)
	svc, err := artifacts.New(root, store)
	if err != nil {
		t.Fatal(err)
	}
	a, err := svc.Register(ctx, task.ID, "report.txt")
	if err != nil {
		t.Fatal(err)
	}
	if a.Size != 5 || len(a.SHA256) != 64 {
		t.Fatal(a)
	}
	_, b, err := svc.Download(ctx, task.ID, a.ID)
	if err != nil || string(b) != "hello" {
		t.Fatal(string(b), err)
	}
	if _, _, err = svc.Download(ctx, "other-task", a.ID); err == nil {
		t.Fatal("cross-task download")
	}
	for _, p := range []string{"../report.txt", "/etc/passwd", "a/../report.txt", ".hidden", "a\\b", "report.txt/", "./report.txt", ""} {
		if _, err = svc.Register(ctx, task.ID, p); err == nil {
			t.Fatalf("accepted %q", p)
		}
	}
	for _, p := range []string{"..", "../" + task.ID, "/" + task.ID} {
		if _, err = svc.Register(ctx, p, "report.txt"); err == nil {
			t.Fatalf("task accepted %q", p)
		}
	}
	os.Symlink(filepath.Join(dir, "report.txt"), filepath.Join(dir, "link"))
	if _, err = svc.Register(ctx, task.ID, "link"); err == nil {
		t.Fatal("symlink accepted")
	}
	os.Symlink(dir, filepath.Join(dir, "linked-dir"))
	if _, err = svc.Register(ctx, task.ID, "linked-dir/report.txt"); err == nil {
		t.Fatal("directory symlink accepted")
	}
	big, err := os.Create(filepath.Join(dir, "big"))
	if err != nil {
		t.Fatal(err)
	}
	big.Truncate(artifacts.MaxBytes + 1)
	big.Close()
	if _, err = svc.Register(ctx, task.ID, "big"); err == nil {
		t.Fatal("oversized accepted")
	}
	os.WriteFile(filepath.Join(dir, "report.txt"), []byte("other"), 0600)
	if _, _, err = svc.Download(ctx, task.ID, a.ID); !errors.Is(err, artifacts.ErrUnavailable) {
		t.Fatal("changed bytes accepted", err)
	}
	store.Close()
	store, err = session.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc, err = artifacts.New(root, store)
	if err != nil {
		t.Fatal(err)
	}
	items, err := svc.List(ctx, task.ID)
	if err != nil || len(items) != 1 || items[0].ID != a.ID {
		t.Fatal(items, err)
	}
}
func TestSymlinkRootRejected(t *testing.T) {
	root := t.TempDir()
	db, err := session.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	link := filepath.Join(t.TempDir(), "link")
	os.Symlink(root, link)
	if _, err = artifacts.New(link, db); err == nil {
		t.Fatal("symlink root accepted")
	}
}

func TestHardlinkedFileRejectedAtRegistrationAndDownload(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := session.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task, err := store.Create(ctx, "task")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, task.ID)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(original, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(original, filepath.Join(dir, "linked")); err != nil {
		t.Fatal(err)
	}
	svc, err := artifacts.New(root, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Register(ctx, task.ID, "linked"); err == nil {
		t.Fatal("hardlink registered")
	}
	file := filepath.Join(dir, "report")
	if err := os.WriteFile(file, []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := svc.Register(ctx, task.ID, "report")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Link(file, filepath.Join(t.TempDir(), "second")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Download(ctx, task.ID, a.ID); err == nil {
		t.Fatal("new hardlink not rejected on download")
	}
}

func TestRegistrationDeduplicatesAndEnforcesQuota(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := session.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task, err := store.Create(ctx, "task")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, task.ID)
	os.Mkdir(dir, 0700)
	svc, err := artifacts.New(root, store)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < artifacts.MaxPerTask; i++ {
		name := fmt.Sprintf("file-%d", i)
		os.WriteFile(filepath.Join(dir, name), []byte("content"), 0600)
		a, err := svc.Register(ctx, task.ID, name)
		if err != nil {
			t.Fatal(i, err)
		}
		again, err := svc.Register(ctx, task.ID, name)
		if err != nil || again.ID != a.ID {
			t.Fatal("duplicate", i, err)
		}
	}
	list, err := svc.List(ctx, task.ID)
	if err != nil || len(list) != artifacts.MaxPerTask {
		t.Fatal(len(list), err)
	}
	os.WriteFile(filepath.Join(dir, "excess"), []byte("content"), 0600)
	if _, err := svc.Register(ctx, task.ID, "excess"); !errors.Is(err, artifacts.ErrQuota) {
		t.Fatal("quota", err)
	}
}
func TestGlobalReadConcurrencyLimit(t *testing.T) {
	var releases []func()
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	for i := 0; i < artifacts.MaxConcurrentReads; i++ {
		release, err := artifacts.AcquireRead()
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	if release, err := artifacts.AcquireRead(); !errors.Is(err, artifacts.ErrBusy) {
		if release != nil {
			release()
		}
		t.Fatal("unbounded readers", err)
	}
	releases[0]()
	releases = releases[1:]
	release, err := artifacts.AcquireRead()
	if err != nil {
		t.Fatal("slot not reusable", err)
	}
	release()
}

func TestConcurrentRegistryLastSlotIsAtomic(t *testing.T) {
	ctx := context.Background()
	store, err := session.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task, err := store.Create(ctx, "task")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < artifacts.MaxPerTask-1; i++ {
		name := fmt.Sprint(i)
		_, err = store.SaveArtifact(ctx, artifacts.Artifact{ID: name, TaskID: task.ID, Name: name, Path: name, SHA256: name, CreatedAt: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
	}
	results := make(chan error, 2)
	for _, id := range []string{"last-a", "last-b"} {
		go func(id string) {
			_, err := store.SaveArtifact(ctx, artifacts.Artifact{ID: id, TaskID: task.ID, Name: id, Path: id, SHA256: id, CreatedAt: time.Now()})
			results <- err
		}(id)
	}
	successes := 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			successes++
		} else if !errors.Is(err, artifacts.ErrQuota) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatal("quota race", successes)
	}
}
