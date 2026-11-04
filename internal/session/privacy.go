package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var ErrUnsafeStorage = errors.New("session storage is not private")
var ErrPrivacyUnsupported = errors.New("private session storage validation is unsupported on this platform")

// EnsurePrivateDirectory creates only the final directory component with mode
// 0700, or validates an existing directory. It never changes existing modes or
// ownership. Parent directories must exist. Symlink directories are rejected.
func EnsurePrivateDirectory(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err = validateStorageDirectory(filepath.Dir(absolute), false); err != nil {
		return err
	}
	if err = os.Mkdir(absolute, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return validateStorageDirectory(absolute, true)
}

// ValidatePrivateStorage checks the directory, database and SQLite/engine
// sidecars without modifying them. Missing files are allowed. Unix permissions
// protect against other OS users, not an attacker controlling the same account
// or an administrator. Network filesystems and external ACL grants are outside
// this local Unix permission check.
func ValidatePrivateStorage(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err = validateStorageDirectory(filepath.Dir(absolute), true); err != nil {
		return err
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal", ".engine.lock"} {
		info, err := os.Lstat(absolute + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if err = validatePrivateFile(info); err != nil {
			return fmt.Errorf("%w: %s", err, absolute+suffix)
		}
	}
	return nil
}
