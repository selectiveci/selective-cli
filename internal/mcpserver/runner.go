package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"time"
)

const (
	stdoutCap       = 256 * 1024
	pipeDrainWindow = time.Second
)

type Result struct {
	Stdout    []byte
	ExitCode  int
	Truncated bool
	TimedOut  bool
}

func (r *Runner) Run(ctx context.Context, name string, raw json.RawMessage) (Result, error) {
	argv, timeout, err := r.prepare(name, raw)
	if err != nil {
		return Result{}, err
	}
	return execute(ctx, argv, timeout)
}

func execute(ctx context.Context, argv []string, timeout time.Duration) (Result, error) {
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(deadline, argv[0], argv[1:]...)
	stdout := &capped{limit: stdoutCap}
	cmd.Stdout = stdout
	cmd.Cancel = func() error { return killTree(cmd) }
	cmd.WaitDelay = pipeDrainWindow
	isolate(cmd)
	_ = cmd.Run()
	if cmd.ProcessState == nil {
		return Result{}, errors.New("could not start " + argv[0])
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	out, truncated := stdout.result()
	return Result{
		Stdout:    out,
		ExitCode:  cmd.ProcessState.ExitCode(),
		Truncated: truncated,
		TimedOut:  errors.Is(deadline.Err(), context.DeadlineExceeded),
	}, nil
}
