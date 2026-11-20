package gitctx

import (
	"os"
	"os/exec"
)

func isolate(*exec.Cmd) {}

func kill(p *os.Process) {
	_ = p.Kill()
}
