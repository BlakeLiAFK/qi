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
