//go:build windows

package download

import "os/exec"

func configureSubprocess(_ *exec.Cmd) {}

func killSubprocessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
