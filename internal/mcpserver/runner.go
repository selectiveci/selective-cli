package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

const (
	stdoutCap       = 256 * 1024
	pipeDrainWindow = time.Second
)

var terminateGrace = 7 * time.Second

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
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(deadline, argv[0], argv[1:]...)
	stdout := &capped{limit: stdoutCap}
	cmd.Stdout = stdout
	finished := make(chan struct{})
	cmd.Cancel = func() error {
		go func() {
			select {
			case <-time.After(terminateGrace):
				_ = killTree(cmd)
			case <-finished:
			}
		}()
		return terminate(cmd)
	}
	cmd.WaitDelay = terminateGrace + pipeDrainWindow
	isolate(cmd)
	runErr := cmd.Run()
	close(finished)
	if deadline.Err() != nil && cmd.Process != nil {
		_ = killTree(cmd)
	}
	if cmd.ProcessState == nil {
		return Result{}, fmt.Errorf("run %s: %w", argv[0], runErr)
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
