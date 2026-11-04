//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package session

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"gopi/internal/testutil"
)

func TestPrivacyRejectsPublicExistingDatabaseWithoutMutation(t *testing.T) {
	path := filepath.Join(testutil.PrivateDir(t), "db")
	if err := os.WriteFile(path, []byte("original bytes"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if store != nil {
		store.Close()
	}
	if !errors.Is(err, ErrUnsafeStorage) {
		t.Fatalf("public database accepted: %v", err)
	}
	info, _ := os.Stat(path)
	data, _ := os.ReadFile(path)
	if info.Mode().Perm() != 0644 || string(data) != "original bytes" {
		t.Fatal("existing user data changed")
	}
}
func TestPrivacyRejectsUnsafeParentWithoutCreatingDatabase(t *testing.T) {
	parent := testutil.PrivateDir(t)
	if err := os.Chmod(parent, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "db")
	if _, err := Open(path); !errors.Is(err, ErrUnsafeStorage) {
		t.Fatalf("public parent accepted: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("database created despite unsafe parent: %v", err)
	}
	info, _ := os.Stat(parent)
	if info.Mode().Perm() != 0755 {
		t.Fatal("parent silently chmodded")
	}
}
func TestPrivacyRejectsSidecarLinksAndModes(t *testing.T) {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal", ".engine.lock"} {
		for _, kind := range []string{"public", "symlink", "hardlink", "directory", "fifo"} {
			t.Run(suffix+"/"+kind, func(t *testing.T) {
				dir := testutil.PrivateDir(t)
				path := filepath.Join(dir, "db")
				target := path + suffix
				switch kind {
				case "public":
					if err := os.WriteFile(target, nil, 0644); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(target, 0644); err != nil {
						t.Fatal(err)
					}
				case "symlink":
					external := filepath.Join(testutil.PrivateDir(t), "secret")
					if err := os.WriteFile(external, []byte("keep"), 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(external, target); err != nil {
						t.Fatal(err)
					}
				case "hardlink":
					external := filepath.Join(dir, "external")
					if err := os.WriteFile(external, nil, 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Link(external, target); err != nil {
						t.Fatal(err)
					}
				case "directory":
					if err := os.Mkdir(target, 0700); err != nil {
						t.Fatal(err)
					}
				case "fifo":
					if err := syscall.Mkfifo(target, 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := ValidatePrivateStorage(path); !errors.Is(err, ErrUnsafeStorage) {
					t.Fatalf("unsafe %s accepted: %v", kind, err)
				}
				if store, err := Open(path); !errors.Is(err, ErrUnsafeStorage) {
					if store != nil {
						store.Close()
					}
					t.Fatalf("Open accepted unsafe %s: %v", kind, err)
				}
			})
		}
	}
}
func TestPrivacyDirectorySymlinkAndWritableAncestor(t *testing.T) {
	root := testutil.PrivateDir(t)
	target := testutil.PrivateDir(t)
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(alias, "db")); !errors.Is(err, ErrUnsafeStorage) {
		t.Fatalf("data dir symlink accepted: %v", err)
	}
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(child, "db")); !errors.Is(err, ErrUnsafeStorage) {
		t.Fatalf("writable ancestor accepted: %v", err)
	}
}
func TestEnsurePrivateDirectoryNoPermissionMutation(t *testing.T) {
	root := testutil.PrivateDir(t)
	path := filepath.Join(root, "data")
	if err := EnsurePrivateDirectory(path); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateDirectory(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0755); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateDirectory(path); !errors.Is(err, ErrUnsafeStorage) {
		t.Fatalf("public dir accepted: %v", err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0755 {
		t.Fatal("existing mode changed")
	}
}

type fakeOwnerInfo struct {
	os.FileInfo
	stat syscall.Stat_t
}

func (f fakeOwnerInfo) Sys() any { return &f.stat }
func TestPrivacyRejectsOtherOwner(t *testing.T) {
	path := filepath.Join(testutil.PrivateDir(t), "db")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	stat := *info.Sys().(*syscall.Stat_t)
	stat.Uid = uint32(os.Geteuid()) + 1
	if err := validatePrivateFile(fakeOwnerInfo{info, stat}); !errors.Is(err, ErrUnsafeStorage) {
		t.Fatalf("foreign owner accepted: %v", err)
	}
}
func TestSQLiteSidecarsPrivate(t *testing.T) {
	store, path := openTest(t)
	if err := ValidatePrivateStorage(path); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		info, err := os.Stat(path + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0077 != 0 {
			t.Fatalf("public %s", suffix)
		}
	}
	release, err := store.AcquireEngineLock()
	if err != nil {
		t.Fatal(err)
	}
	release()
	if err := ValidatePrivateStorage(path); err != nil {
		t.Fatal(err)
	}
}
