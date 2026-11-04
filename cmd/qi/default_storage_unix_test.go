//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"gopi/internal/session"
)

func isolatedConfigDirectory(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if runtime.GOOS == "darwin" {
		t.Setenv("HOME", root)
		root = filepath.Join(root, "Library", "Application Support")
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Setenv("XDG_CONFIG_HOME", root)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}
func defaultStorageEnv(key string) string {
	if key == "QI_WEB_TOKEN" {
		return testToken
	}
	return ""
}
func TestDefaultStorageRejectsUnsafeAncestorBeforeCreation(t *testing.T) {
	root := isolatedConfigDirectory(t)
	if err := os.Chmod(root, 0777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0700) })
	err := runServer(context.Background(), []string{"-model", "fixture", "-listen", "127.0.0.1:0"}, io.Discard, defaultStorageEnv)
	if !errors.Is(err, session.ErrUnsafeStorage) {
		t.Fatalf("expected unsafe storage error, got %v", err)
	}
	if _, err = os.Stat(filepath.Join(root, "qi")); !os.IsNotExist(err) {
		t.Fatalf("unsafe ancestor received qi directory: %v", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0777 {
		t.Fatal("ancestor permissions were silently changed")
	}
}
func TestDefaultStorageRejectsExistingUnsafeDirectoryWithoutChmod(t *testing.T) {
	root := isolatedConfigDirectory(t)
	dir := filepath.Join(root, "qi")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	err := runServer(context.Background(), []string{"-model", "fixture", "-listen", "127.0.0.1:0"}, io.Discard, defaultStorageEnv)
	if !errors.Is(err, session.ErrUnsafeStorage) {
		t.Fatalf("expected unsafe storage error, got %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0755 {
		t.Fatal("existing directory permissions were silently changed")
	}
	if _, err = os.Stat(filepath.Join(dir, "tasks.db")); !os.IsNotExist(err) {
		t.Fatalf("unsafe directory received database: %v", err)
	}
}
func TestDefaultStorageCreatesOnlyPrivateFinalDirectory(t *testing.T) {
	root := isolatedConfigDirectory(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- runServer(ctx, []string{"-model", "fixture", "-listen", "127.0.0.1:0"}, readyWriter{Writer: io.Discard, ready: ready}, defaultStorageEnv)
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("startup failed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("startup timed out")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timed out")
	}
	for path, mode := range map[string]os.FileMode{filepath.Join(root, "qi"): 0700, filepath.Join(root, "qi", "tasks.db"): 0600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Fatalf("%s mode=%o want=%o", path, info.Mode().Perm(), mode)
		}
	}
}

func TestDefaultStorageDoesNotCreateMissingConfigurationAncestors(t *testing.T) {
	root := isolatedConfigDirectory(t)
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	err := runServer(context.Background(), []string{"-model", "fixture", "-listen", "127.0.0.1:0"}, io.Discard, defaultStorageEnv)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing ancestor error, got %v", err)
	}
	if _, err = os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("configuration ancestor was created: %v", err)
	}
}
