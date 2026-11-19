//go:build !windows

package browser

import (
	"os/exec"
	"syscall"
)

// A process group of its own, so Ctrl+C or an agent killing sel leaves a
// freshly launched browser running.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
