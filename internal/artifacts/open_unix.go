//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package artifacts

import (
	"golang.org/x/sys/unix"
	"os"
	"strings"
)

// Open every directory relative to a pinned descriptor. O_NOFOLLOW prevents
// symlink swaps, including swaps between validation and file opening.
func openSecure(root, relative string) (*os.File, error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimPrefix(root, "/"), "/")
	if relative != "" {
		parts = append(parts, strings.Split(relative, "/")...)
	}
	for i, p := range parts {
		if p == "" {
			continue
		}
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
		if i < len(parts)-1 || relative == "" {
			flags |= unix.O_DIRECTORY
		}
		next, e := unix.Openat(fd, p, flags, 0)
		unix.Close(fd)
		if e != nil {
			return nil, e
		}
		fd = next
	}
	// A descriptor-based link-count check rejects ordinary hardlink escapes.
	// This is only a mitigation: a hostile writer can unlink another name or
	// replace regular files. Storage must not be writable by untrusted workers.
	if relative != "" {
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			unix.Close(fd)
			return nil, err
		}
		if st.Nlink != 1 {
			unix.Close(fd)
			return nil, ErrUnavailable
		}
	}
	return os.NewFile(uintptr(fd), "artifact"), nil
}
