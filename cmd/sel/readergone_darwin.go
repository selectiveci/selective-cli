package main

import (
	"os"

	"golang.org/x/sys/unix"
)

func readerGone(f *os.File) bool {
	fds := []unix.PollFd{{Fd: int32(f.Fd()), Events: unix.POLLOUT}}
	n, _ := unix.Poll(fds, 0)
	return n > 0 && fds[0].Revents&(unix.POLLERR|unix.POLLHUP) != 0
}

func eioMeansReaderGone(f *os.File, _ bool) bool { return readerGone(f) }
