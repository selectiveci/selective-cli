package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/gitctx"
	"github.com/selectiveci/selective-cli/internal/localrun"
)

var (
	errHangup     = errors.New("the terminal hung up")
	errReaderLeft = errors.New("the stdout reader left")
)

type readerWatch struct {
	io.Writer
	file     *os.File
	terminal bool
	left     func()
}

func (w readerWatch) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.EIO) && eioMeansReaderGone(w.file, w.terminal) {
		w.left()
		return len(p), nil
	}
	return n, err
}

func underlyingFile(w io.Writer) *os.File {
	for {
		switch v := w.(type) {
		case *os.File:
			return v
		case interface{ Unwrap() io.Writer }:
			w = v.Unwrap()
		default:
			return nil
		}
	}
}

func isRunnerError(err error) bool {
	var failure *contract.Error
	return errors.As(err, &failure) && failure.Code == contract.RunnerError
}

func readerDeparted(err error) bool {
	return errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.EIO)
}

func interruptContext(parent context.Context) (context.Context, context.CancelCauseFunc, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(parent)
	signal.Notify(make(chan os.Signal, 1), syscall.SIGPIPE)
	signals := []os.Signal{os.Interrupt, syscall.SIGTERM}
	if !signal.Ignored(syscall.SIGHUP) {
		signals = append(signals, syscall.SIGHUP)
	}
	received := make(chan os.Signal, 1)
	signal.Notify(received, signals...)
	_, capped := parent.Value(backstopKey{}).(*clock)
	wound := make(chan struct{})
	go func() {
		select {
		case sig := <-received:
			if sig == syscall.SIGHUP {
				cancel(errHangup)
			} else {
				if sig == os.Interrupt {
					noteCtrlC(parent)
				}
				cancel(context.Canceled)
			}
			if !capped {
				exitUnlessWoundDown(wound)
			}
		case <-ctx.Done():
		}
	}()
	closeWound := sync.OnceFunc(func() { close(wound) })
	return ctx, cancel, func() {
		signal.Stop(received)
		cancel(context.Canceled)
		closeWound()
	}
}

func exitUnlessWoundDown(wound <-chan struct{}) {
	select {
	case <-wound:
		return
	case <-time.After(localrun.ShutdownBudget + backstopGrace):
	}
	cleaned := make(chan struct{})
	go func() {
		gitctx.KillRunning()
		localrun.Cleanup()
		close(cleaned)
	}()
	select {
	case <-cleaned:
	case <-time.After(backstopExit / 2):
	}
	hardExit(contract.ExitInterrupted)
}
