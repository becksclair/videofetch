//go:build !windows

package download

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

func configureSubprocess(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return killSubprocessGroup(cmd)
	}
	cmd.WaitDelay = 3 * time.Second
}

func killSubprocessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
