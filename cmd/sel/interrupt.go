package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"
)

var errHangup = errors.New("the terminal hung up")

func interruptContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(parent)
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
				signal.Ignore(syscall.SIGPIPE)
				cancel(errHangup)
				return
			}
			cancel(context.Canceled)
		case <-ctx.Done():
		}
	}()
	return ctx, func() {
		signal.Stop(received)
		cancel(context.Canceled)
	}
}
