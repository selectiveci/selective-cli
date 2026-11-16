//go:build !unix

package mcpserver

import "os/exec"

func isolate(*exec.Cmd) {}

func killTree(cmd *exec.Cmd) error {
	return cmd.Process.Kill()
}
