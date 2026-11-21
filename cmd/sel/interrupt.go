package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"syscall"
)

var (
	errHangup     = errors.New("the terminal hung up")
	errReaderLeft = errors.New("the stdout reader left")
)

type readerWatch struct {
	io.Writer
	left func()
}

func (w readerWatch) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if errors.Is(err, syscall.EPIPE) {
		w.left()
		return len(p), nil
	}
	return n, err
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
