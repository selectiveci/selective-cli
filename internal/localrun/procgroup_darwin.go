package localrun

import (
	"os/exec"
	"syscall"
)

func isolate(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}

func signalGroup(cmd *exec.Cmd, sig syscall.Signal) {
	_ = syscall.Kill(-cmd.Process.Pid, sig)
}
