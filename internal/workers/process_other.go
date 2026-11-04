//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package workers

import "os/exec"

func configureProcess(cmd *exec.Cmd) {}

func cleanupProcess(cmd *exec.Cmd) {}
