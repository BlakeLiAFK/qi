//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func validateStorageDirectory(path string, private bool) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	current := string(filepath.Separator)
	parts := strings.Split(strings.TrimPrefix(absolute, current), string(filepath.Separator))
	for i, part := range parts {
		if part != "" {
			current = filepath.Join(current, part)
		}
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		// macOS /tmp and /var are root-managed symlinks. Permit only such
		// ancestor aliases, after validating their resolved directory chain.
		// The private data directory itself and all database files cannot alias.
		if info.Mode()&os.ModeSymlink != 0 && (!private || i < len(parts)-1) {
			var link unix.Stat_t
			if err = unix.Lstat(current, &link); err != nil {
				return err
			}
			if link.Uid != 0 {
				return fmt.Errorf("%w: untrusted ancestor symlink: %s", ErrUnsafeStorage, current)
			}
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return err
			}
			if err = validateStorageDirectory(resolved, false); err != nil {
				return err
			}
			current = resolved
			info, err = os.Lstat(current)
			if err != nil {
				return err
			}
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: directory must not be a symlink: %s", ErrUnsafeStorage, current)
		}
		var st unix.Stat_t
		if err = unix.Lstat(current, &st); err != nil {
			return err
		}
		owner := uint32(os.Geteuid())
		if st.Uid != 0 && st.Uid != owner {
			return fmt.Errorf("%w: untrusted directory owner: %s", ErrUnsafeStorage, current)
		}
		if private && i == len(parts)-1 {
			if st.Uid != owner || info.Mode().Perm()&0077 != 0 {
				return fmt.Errorf("%w: data directory must be owned by this user with mode 0700 or stricter: %s", ErrUnsafeStorage, current)
			}
		} else if info.Mode().Perm()&0022 != 0 && !(st.Uid == 0 && info.Mode()&os.ModeSticky != 0) {
			return fmt.Errorf("%w: ancestor directory is writable by other users: %s", ErrUnsafeStorage, current)
		}
	}
	return nil
}
func validatePrivateFile(info os.FileInfo) error {
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("%w: expected regular file with mode 0600 or stricter", ErrUnsafeStorage)
	}
	// FileInfo.Sys uses the platform syscall.Stat_t. Re-stat through the descriptor
	// in openPrivateFile; use the portable ownership helper here.
	return validateFileOwner(info)
}
func openPrivateFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err == nil {
		err = validatePrivateFile(info)
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func validateFileOwner(info os.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
		return fmt.Errorf("%w: file must be owned by this user and have exactly one link", ErrUnsafeStorage)
	}
	return nil
}
