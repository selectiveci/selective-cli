package main

import (
	"io"
	"os"
	"time"
)

const inFlightWriteWait = 100 * time.Millisecond

func silence(w io.Writer) io.Writer {
	f, ok := w.(*os.File)
	if !ok || f != os.Stdout && f != os.Stderr {
		return w
	}
	saved, err := dupFd(int(f.Fd()))
	if err != nil {
		return w
	}
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return w
	}
	defer null.Close()
	if dup2(int(null.Fd()), int(f.Fd())) != nil {
		return w
	}
	drained := make(chan struct{})
	go func() {
		_, _ = f.Write(nil)
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(inFlightWriteWait):
	}
	return os.NewFile(uintptr(saved), f.Name())
}
