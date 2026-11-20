package gitctx

import (
	"os"
	"os/exec"
	"syscall"
)

func isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func kill(p *os.Process) {
	_ = syscall.Kill(-p.Pid, syscall.SIGKILL)
}
