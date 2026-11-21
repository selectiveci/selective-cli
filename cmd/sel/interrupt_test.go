package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"testing"
)

func TestReaderDeparted(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"a broken pipe", syscall.EPIPE, true},
		{"a wrapped broken pipe", fmt.Errorf("write: %w", &os.PathError{Op: "write", Path: "stdout", Err: syscall.EPIPE}), true},
		{"an input/output error", syscall.EIO, true},
		{"a wrapped input/output error", fmt.Errorf("write: %w", &os.PathError{Op: "write", Path: "stdout", Err: syscall.EIO}), true},
		{"a full disk", syscall.ENOSPC, false},
		{"a wrapped full disk", fmt.Errorf("write: %w", &os.PathError{Op: "write", Path: "stdout", Err: syscall.ENOSPC}), false},
		{"a plain error", errors.New("boom"), false},
		{"a short write", io.ErrShortWrite, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := readerDeparted(tc.err); got != tc.want {
				t.Errorf("readerDeparted(%v) = %t, want %t", tc.err, got, tc.want)
			}
		})
	}
}
