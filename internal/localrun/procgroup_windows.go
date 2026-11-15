package localrun

import (
	"os/exec"
	"syscall"

	"github.com/selectiveci/selective-cli/internal/contract"
)

func isolate(*exec.Cmd) error {
	return contract.Errorf(contract.UsageError, "sel test does not run on Windows yet")
}

func signalGroup(*exec.Cmd, syscall.Signal) {}
