//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package artifacts

import (
	"errors"
	"os"
)

func openSecure(root, relative string) (*os.File, error) {
	return nil, errors.New("secure artifact access unsupported on this platform")
}
