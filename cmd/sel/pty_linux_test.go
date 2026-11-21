package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pty: %v", err)
	}
	t.Cleanup(func() { _ = master.Close() })
	var unlock, index int32
	for _, call := range []struct {
		req uintptr
		arg *int32
	}{{syscall.TIOCSPTLCK, &unlock}, {syscall.TIOCGPTN, &index}} {
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), call.req, uintptr(unsafe.Pointer(call.arg))); errno != 0 {
			t.Fatal(errno)
		}
	}
	slave, err = os.OpenFile("/dev/pts/"+strconv.Itoa(int(index)), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	return master, slave
}

func TestSelPicksPrettyOutputOnATerminalWithAndWithoutATimeout(t *testing.T) {
	cases := map[string][]string{
		"no --timeout": {"doctor"},
		"--timeout":    {"doctor", "--timeout=30s"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			master, slave := openPTY(t)
			cmd := exec.Command(os.Args[0], args...)
			cmd.Dir = t.TempDir()
			cmd.Env = append(os.Environ(), "SELECTIVE_TEST_RUN_MAIN=1", "SELECTIVE_NO_UPDATE_NOTIFIER=1", "SELECTIVE_HOST=http://127.0.0.1:1", "SELECTIVE_TOKEN=tok", "SELECTIVE_CONFIG_DIR="+t.TempDir(), "DBUS_SESSION_BUS_ADDRESS=unix:path=/nonexistent")
			cmd.Stdout = slave
			var exitErr *exec.ExitError
			if err := cmd.Run(); err != nil && !errors.As(err, &exitErr) {
				t.Fatal(err)
			}
			_ = slave.Close()
			shown, _ := io.ReadAll(master)

			if len(shown) == 0 || strings.Contains(string(shown), `"v":1`) {
				t.Errorf("terminal output %q; want the pretty checklist, not a JSON document", shown)
			}
		})
	}
}
