package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync/atomic"
	"time"

	"github.com/selectiveci/selective-cli/internal/localrun"
	"github.com/selectiveci/selective-cli/internal/update"
)

const (
	stdoutCap                = 256 * 1024
	terminateMargin          = time.Second
	productionTerminateGrace = localrun.ShutdownBudget + terminateMargin
)

var (
	terminateGrace  = productionTerminateGrace
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
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	reader, writer, err := os.Pipe()
	if err != nil {
		return Result{}, fmt.Errorf("run %s: %w", argv[0], err)
	}
	defer reader.Close()
	cmd := exec.CommandContext(deadline, argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), update.OptOutVariable+"=1")
	cmd.Stdout = writer
	stdout := &capped{limit: stdoutCap}
	drained := make(chan struct{})
	go func() {
		_, _ = io.Copy(stdout, reader)
		close(drained)
	}()
	var cancelled, timedOut atomic.Bool
	finished := make(chan struct{})
	cmd.Cancel = cancelGroup(cmd, ctx, &cancelled, &timedOut, finished)
	cmd.WaitDelay = terminateGrace + pipeDrainWindow
	isolate(cmd)
	startErr := cmd.Start()
	_ = writer.Close()
	if startErr != nil {
		return Result{}, fmt.Errorf("run %s: %w", argv[0], startErr)
	}
	_ = cmd.Wait()
	close(finished)
	if cancelled.Load() {
		_ = killTree(cmd)
	}
	held := false
	select {
	case <-drained:
	case <-time.After(pipeDrainWindow):
		held = true
		_ = killTree(cmd)
		_ = reader.Close()
		<-drained
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	out, truncated := stdout.result(held, timedOut.Load())
	return Result{
		Stdout:    out,
		ExitCode:  cmd.ProcessState.ExitCode(),
		Truncated: truncated,
		TimedOut:  timedOut.Load(),
	}, nil
}

func cancelGroup(cmd *exec.Cmd, ctx context.Context, cancelled, timedOut *atomic.Bool, finished <-chan struct{}) func() error {
	return func() error {
		err := terminate(cmd)
		if err != nil {
			return err
		}
		cancelled.Store(true)
		timedOut.Store(ctx.Err() == nil)
		go func() {
			select {
			case <-time.After(terminateGrace):
				_ = killTree(cmd)
			case <-finished:
			}
		}()
		return nil
	}
}
