package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReaderGoneReportsEachKindOfDeadReader(t *testing.T) {
	for _, tc := range []struct {
		name string
		gone bool
		open func(t *testing.T) *os.File
	}{
		{"a pipe with a reader", false, func(t *testing.T) *os.File {
			r, w, _ := os.Pipe()
			t.Cleanup(func() { _ = r.Close() })
			return w
		}},
		{"a pipe whose reader closed", true, func(t *testing.T) *os.File {
			r, w, _ := os.Pipe()
			_ = r.Close()
			return w
		}},
		{"a pty with its master open", false, func(t *testing.T) *os.File {
			master, slave := openPty(t)
			t.Cleanup(func() { _ = master.Close() })
			return slave
		}},
		{"a pty whose master closed", true, func(t *testing.T) *os.File {
			master, slave := openPty(t)
			_ = master.Close()
			return slave
		}},
		{"a socket with its peer open", false, func(t *testing.T) *os.File {
			fds, _ := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
			t.Cleanup(func() { _ = syscall.Close(fds[1]) })
			return os.NewFile(uintptr(fds[0]), "socket")
		}},
		{"a socket whose peer closed", true, func(t *testing.T) *os.File {
			fds, _ := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
			_ = syscall.Close(fds[1])
			return os.NewFile(uintptr(fds[0]), "socket")
		}},
		{"a regular file", false, func(t *testing.T) *os.File {
			f, _ := os.Create(filepath.Join(t.TempDir(), "out"))
			return f
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.open(t)
			defer f.Close()
			if got := readerGone(f); got != tc.gone {
				t.Errorf("readerGone = %t, want %t", got, tc.gone)
			}
		})
	}
}

func selOn(t *testing.T, api *fakeAPI, stdout, stderr *os.File, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"test", "--org", "acme", "--json"}, args...)...)
	cmd.Dir = pytestProject(t)
	cmd.Env = append(os.Environ(), "SELECTIVE_TEST_RUN_MAIN=1", "SELECTIVE_NO_UPDATE_NOTIFIER=1", "SELECTIVE_HOST="+api.srv.URL, "SELECTIVE_TOKEN=tok", "SELECTIVE_CONFIG_DIR="+t.TempDir())
	cmd.Stdout, cmd.Stderr = stdout, stderr
	return cmd
}

func TestAPtyAlreadyClosedWhenSelStartsExitsInterrupted(t *testing.T) {
	runnerEnv(t)
	master, slave := openPty(t)
	_ = master.Close()
	cmd := selOn(t, newFakeAPI(t, everything), slave, slave)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 30 * time.Second
	_ = cmd.Run()
	_ = slave.Close()

	if got := cmd.ProcessState.ExitCode(); got != 130 {
		t.Errorf("sel: %s, want exit 130 for a pty that closed before sel started", cmd.ProcessState)
	}
}

func TestEIOOnALiveTerminalFromAnOrphanedBackgroundJobStillExitsWithTheWriteError(t *testing.T) {
	runnerEnv(t)
	api := newFakeAPI(t, everything)
	rc, errFile := filepath.Join(t.TempDir(), "rc"), filepath.Join(t.TempDir(), "err")
	master, slave := openPty(t)
	t.Cleanup(func() { _ = master.Close() })
	go func() { _, _ = io.Copy(io.Discard, master) }()
	script := `set -m; stty tostop; ( sh -c '"$0" test --org acme --json --timeout=20s 2>"$2"; echo $? >"$1"' "$0" "$1" "$2" & ); while [ ! -s "$1" ]; do sleep 0.1; done`
	cmd := exec.Command("sh", "-c", script, os.Args[0], rc, errFile)
	cmd.Dir = pytestProject(t)
	cmd.Env = append(os.Environ(), "SELECTIVE_TEST_RUN_MAIN=1", "SELECTIVE_NO_UPDATE_NOTIFIER=1", "SELECTIVE_HOST="+api.srv.URL, "SELECTIVE_TOKEN=tok", "SELECTIVE_CONFIG_DIR="+t.TempDir())
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = slave.Close()
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	waitUntil(t, "the orphaned sel to finish", func() bool { body, _ := os.ReadFile(rc); return len(body) > 0 })

	code, stderr := mustRead(t, rc), mustRead(t, errFile)
	if strings.TrimSpace(string(code)) != "9" || !strings.Contains(string(stderr), "could not write the event stream") {
		t.Errorf("exit %q, stderr %q; want exit 9 and the event stream write error on a live terminal", code, stderr)
	}
}

func TestARunnerThatReallyFailsWithLiveStdoutAndDeadStderrStillExitsOne(t *testing.T) {
	t.Setenv("FAKE_DIE", "1")
	t.Setenv("FAKE_IGNORE_SIGPIPE", "1")
	runnerEnv(t)
	r, w, _ := os.Pipe()
	_ = r.Close()
	outR, outW, _ := os.Pipe()
	cmd := selOn(t, newFakeAPI(t, everything), outW, w)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = outW.Close()
	_ = w.Close()
	out, _ := io.ReadAll(outR)
	_ = cmd.Wait()

	if cmd.ProcessState.ExitCode() != 1 || !strings.Contains(string(out), "runner_error") {
		t.Errorf("exit %d, stdout %q; want exit 1 and runner_error", cmd.ProcessState.ExitCode(), out)
	}
}
