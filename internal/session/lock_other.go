//go:build !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package session

import (
	"errors"
	"fmt"
	"os"
	"sync"
)

var ErrEngineLocked = errors.New("another engine owns this session database")

// AcquireEngineLock conservatively uses exclusive sidecar creation on systems
// without the supported advisory-lock implementation. After an unclean exit,
// remove the .engine.lock file manually only after confirming no engine is alive.
func (s *Store) AcquireEngineLock() (func(), error) {
	path := s.path + ".engine.lock"
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("%w: if the prior process crashed, verify it is stopped before removing %s", ErrEngineLocked, path)
		}
		return nil, err
	}
	if _, err = fmt.Fprintf(f, "pid=%d\n", os.Getpid()); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	if err = f.Close(); err != nil {
		os.Remove(path)
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { _ = os.Remove(path) }) }, nil
}
