//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package plugins

import "os/exec"

const processGroupsSupported = false

func configureProcess(cmd *exec.Cmd)  {}
func killProcess(cmd *exec.Cmd) error { return cmd.Process.Kill() }
