//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package session

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

var ErrEngineLocked = errors.New("another engine owns this session database")

// AcquireEngineLock holds exclusive process ownership until release. Acquire
// before recovery and retain until all runs stop. Unix releases the lock after
// process death. The sidecar must not be deleted, including during release:
// unlinking it could allow two processes to lock different filesystem inodes.
func (s *Store) AcquireEngineLock() (func(), error) {
	f, err := os.OpenFile(s.path+".engine.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrEngineLocked
		}
		return nil, fmt.Errorf("acquire engine lock: %w", err)
	}
	var once sync.Once
	return func() { once.Do(func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); _ = f.Close() }) }, nil
}
