//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package session

import (
	"errors"
	"gopi/internal/testutil"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestEngineLockCrashRelease(t *testing.T) {
	if path := os.Getenv("QI_LOCK_TEST_CHILD"); path != "" {
		store, err := Open(path)
		if err != nil {
			os.Exit(2)
		}
		if _, err = store.AcquireEngineLock(); err != nil {
			os.Exit(3)
		}
		os.Exit(0) // Deliberately bypass release and all Go defers.
	}
	store, path := openTest(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestEngineLockCrashRelease$")
	cmd.Env = append(os.Environ(), "QI_LOCK_TEST_CHILD="+path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v %s", err, output)
	}
	release, err := store.AcquireEngineLock()
	if err != nil {
		t.Fatalf("stale lock after crash: %v", err)
	}
	release()
}
func TestEngineLockSymlinkAlias(t *testing.T) {
	_, path := openTest(t)
	alias := filepath.Join(testutil.PrivateDir(t), "alias.db")
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	if store, err := Open(alias); !errors.Is(err, ErrUnsafeStorage) {
		if store != nil {
			store.Close()
		}
		t.Fatalf("symlink accepted: %v", err)
	}
}
