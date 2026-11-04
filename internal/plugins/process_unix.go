//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package plugins

import (
	"os"
	"os/exec"
	"syscall"
)

const processGroupsSupported = true

func configureProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func killProcess(cmd *exec.Cmd) error {
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if err == syscall.ESRCH {
		return os.ErrProcessDone
	}
	return err
}
