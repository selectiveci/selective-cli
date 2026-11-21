package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/selectiveci/selective-cli/internal/contract"
)

var (
	errHangup     = errors.New("the terminal hung up")
	errReaderLeft = errors.New("the stdout reader left")
)

type readerWatch struct {
	io.Writer
	terminal bool
	left     func()
}

func (w readerWatch) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if errors.Is(err, syscall.EPIPE) || w.terminal && errors.Is(err, syscall.EIO) {
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
	go func() {
		select {
		case sig := <-received:
			if sig == syscall.SIGHUP {
				cancel(errHangup)
				return
			}
			cancel(context.Canceled)
		case <-ctx.Done():
		}
	}()
	return ctx, cancel, func() {
		signal.Stop(received)
		cancel(context.Canceled)
	}
}
