//go:build !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestUnsupportedPrivacyFailsBeforeCreatingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	if store, err := Open(path); !errors.Is(err, ErrPrivacyUnsupported) {
		if store != nil {
			store.Close()
		}
		t.Fatalf("expected unsupported privacy validation: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("database unexpectedly created: %v", err)
	}
	dir := filepath.Join(t.TempDir(), "private")
	if err := EnsurePrivateDirectory(dir); !errors.Is(err, ErrPrivacyUnsupported) {
		t.Fatalf("directory validation: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("directory unexpectedly created: %v", err)
	}
}

// Unsupported platforms must reject even superficially private mode bits. Go's
// 0600/0700 values are not evidence of an effective Windows security descriptor.
func TestUnsupportedPrivacyPreservesExistingFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "db")
	before := make(map[string][]byte)
	for _, suffix := range []string{"", "-wal", "-shm", "-journal", ".engine.lock"} {
		name := path + suffix
		payload := []byte("existing data must not change: " + suffix)
		if err := os.WriteFile(name, payload, 0600); err != nil {
			t.Fatal(err)
		}
		before[name] = payload
	}
	if err := ValidatePrivateStorage(path); !errors.Is(err, ErrPrivacyUnsupported) {
		t.Fatalf("validation must fail closed: %v", err)
	}
	if err := EnsurePrivateDirectory(dir); !errors.Is(err, ErrPrivacyUnsupported) {
		t.Fatalf("existing directory must fail closed: %v", err)
	}
	if store, err := Open(path); !errors.Is(err, ErrPrivacyUnsupported) {
		if store != nil {
			store.Close()
		}
		t.Fatalf("open must fail closed: %v", err)
	}
	for name, want := range before {
		got, err := os.ReadFile(name)
		if err != nil || string(got) != string(want) {
			t.Errorf("existing file changed: %s: %q, %v", name, got, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(before) {
		t.Fatalf("unexpected files created: %v", entries)
	}
}

func TestUnsupportedPrivacyFileHelpersFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	file, err := openPrivateFile(path)
	if file != nil {
		file.Close()
		t.Fatal("unsupported private open returned a file")
	}
	if !errors.Is(err, ErrPrivacyUnsupported) {
		t.Fatalf("private open must fail closed: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private open created a file: %v", err)
	}
	if err := validatePrivateFile(nil); !errors.Is(err, ErrPrivacyUnsupported) {
		t.Fatalf("file validation must fail closed: %v", err)
	}
}
