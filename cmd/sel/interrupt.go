package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func interruptContext(parent context.Context) (context.Context, context.CancelFunc) {
	signals := []os.Signal{os.Interrupt, syscall.SIGTERM}
	if !signal.Ignored(syscall.SIGHUP) {
		signals = append(signals, syscall.SIGHUP)
	}
	return signal.NotifyContext(parent, signals...)
}
