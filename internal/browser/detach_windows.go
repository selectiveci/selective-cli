package browser

import (
	"os/exec"
	"syscall"
)

// A process group of its own, so Ctrl+C in sel's console leaves the browser running.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}
