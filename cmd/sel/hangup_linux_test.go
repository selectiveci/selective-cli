package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/selectiveci/selective-cli/internal/localrun"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type hangupRun struct {
	t       *testing.T
	cmd     *exec.Cmd
	reader  *os.File
	out     *syncBuffer
	pidFile string
	tmp     string
	exited  chan struct{}
}

func openPty(t *testing.T) (master, slave *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("a pty is required on Linux: %v", err)
	}
	var unlock int32
	var number uint32
	conn, _ := master.SyscallConn()
	var lockErr, numberErr syscall.Errno
	_ = conn.Control(func(fd uintptr) {
		_, _, lockErr = syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock)))
		_, _, numberErr = syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCGPTN, uintptr(unsafe.Pointer(&number)))
	})
	if lockErr != 0 || numberErr != 0 {
		t.Fatalf("a pty is required on Linux: unlock %v, number %v", lockErr, numberErr)
	}
	if slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0); err != nil {
		t.Fatalf("a pty is required on Linux: %v", err)
	}
	return master, slave
}

func startHangupRun(t *testing.T, api *fakeAPI, pty, ignoreHup bool, extraArgs ...string) *hangupRun {
	t.Helper()
	runnerEnv(t)
	t.Setenv("FAKE_PID_FILE", filepath.Join(t.TempDir(), "runner.pid"))
	tmp, err := os.MkdirTemp("/tmp", "k")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	if api == nil {
		api = newFakeAPI(t, selected)
	}
	args := append([]string{"test", "--org", "acme", "--json"}, extraArgs...)
	h := &hangupRun{t: t, out: &syncBuffer{}, pidFile: os.Getenv("FAKE_PID_FILE"), tmp: tmp, exited: make(chan struct{})}
	h.cmd = exec.Command(os.Args[0], args...)
	if ignoreHup {
		h.cmd = exec.Command("sh", append([]string{"-c", `trap "" HUP; exec "$0" "$@"`, os.Args[0]}, args...)...)
	}
	h.cmd.Dir = pytestProject(t)
	h.cmd.Env = append(os.Environ(), "SELECTIVE_TEST_RUN_MAIN=1", "SELECTIVE_NO_UPDATE_NOTIFIER=1", "SELECTIVE_HOST="+api.srv.URL, "SELECTIVE_TOKEN=tok", "SELECTIVE_CONFIG_DIR="+t.TempDir(), "TMPDIR="+tmp)
	h.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var childEnd *os.File
	if pty {
		h.reader, childEnd = openPty(t)
		h.cmd.Stdin = childEnd
		h.cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	} else if h.reader, childEnd, err = os.Pipe(); err != nil {
		t.Fatal(err)
	}
	h.cmd.Stdout, h.cmd.Stderr = childEnd, childEnd
	if err := h.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = childEnd.Close()
	go func() { _, _ = io.Copy(h.out, h.reader) }()
	go func() { _ = h.cmd.Wait(); close(h.exited) }()
	t.Cleanup(func() {
		_ = syscall.Kill(-h.cmd.Process.Pid, syscall.SIGKILL)
		_ = h.reader.Close()
	})
	return h
}

func (h *hangupRun) runnerPid() int {
	h.t.Helper()
	var pid int
	waitUntil(h.t, "the runner to start", func() bool {
		body, _ := os.ReadFile(h.pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(body)))
		return pid > 0
	})
	h.t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	waitUntil(h.t, "the selection event", func() bool { return strings.Contains(h.out.String(), `"event":"selection"`) })
	if err := syscall.Kill(pid, 0); err != nil {
		h.t.Fatalf("VOID: the runner was not alive before the signal: %v", err)
	}
	return pid
}

func (h *hangupRun) hangUp() {
	h.t.Helper()
	if err := syscall.Kill(-h.cmd.Process.Pid, syscall.SIGHUP); err != nil {
		h.t.Fatal(err)
	}
}

func (h *hangupRun) await() {
	h.t.Helper()
	bound := localrun.ShutdownBudget + 2*time.Second
	select {
	case <-h.exited:
	case <-time.After(bound):
		h.t.Fatalf("sel was still running %s after the signal", bound)
	}
}

func (h *hangupRun) wantWoundDown(runner int) {
	h.t.Helper()
	gone := false
	for deadline := time.Now().Add(3 * time.Second); !gone && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		gone = errors.Is(syscall.Kill(-runner, 0), syscall.ESRCH)
	}
	leftovers, _ := os.ReadDir(h.tmp)
	if !gone || len(leftovers) != 0 {
		h.t.Errorf("sel: %s, runner group gone=%t, temp entries %d; want no runner and no temp dir", h.cmd.ProcessState, gone, len(leftovers))
	}
}

func (h *hangupRun) wantInterruptedSummary() {
	h.t.Helper()
	var documents []string
	for _, line := range strings.Split(h.out.String(), "\n") {
		if strings.HasPrefix(line, "{") {
			documents = append(documents, line)
		}
	}
	last := map[string]any{}
	if len(documents) > 0 {
		last = stdoutEvents(h.t, documents[len(documents)-1])[0]
	}
	if h.cmd.ProcessState.ExitCode() != 130 || last["event"] != "summary" || last["interrupted"] != true {
		h.t.Errorf("sel: %s, stdout %q; want exit 130 and a last summary with interrupted=true", h.cmd.ProcessState, h.out.String())
	}
}

func heldAPI(t *testing.T) (api *fakeAPI, release func()) {
	t.Helper()
	api = newFakeAPI(t, selected)
	api.hold, api.held = make(chan struct{}), make(chan struct{}, 1)
	release = sync.OnceFunc(func() { close(api.hold) })
	t.Cleanup(release)
	return api, release
}

func awaitHeld(t *testing.T, api *fakeAPI) {
	t.Helper()
	select {
	case <-api.held:
	case <-time.After(20 * time.Second):
		t.Fatal("the suite listing never started")
	}
}

func TestHangupWhenThePtyClosesWindsDownTheRunner(t *testing.T) {
	t.Setenv("FAKE_HANG", "1")
	h := startHangupRun(t, nil, true, false)
	runner := h.runnerPid()

	if err := h.reader.Close(); err != nil {
		t.Fatal(err)
	}
	h.await()

	h.wantWoundDown(runner)
	if h.cmd.ProcessState.ExitCode() != 130 {
		t.Errorf("sel: %s, want exit 130 after the terminal closed", h.cmd.ProcessState)
	}
}

func TestHangupSignalEndsTheRunWithTheInterruptedSummary(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  string
		args []string
	}{
		{"healthy pipes", "FAKE_HANG", nil},
		{"a runner that ignores interrupt", "FAKE_IGNORE_SIGINT", nil},
		{"a watch", "FAKE_HANG", []string{"--watch"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FAKE_HANG", "1")
			t.Setenv(tc.env, "1")
			h := startHangupRun(t, nil, false, false, tc.args...)
			runner := h.runnerPid()

			h.hangUp()
			h.await()

			h.wantWoundDown(runner)
			h.wantInterruptedSummary()
		})
	}
}

func TestHangupWithADeadStdoutStillWindsDownTheRunner(t *testing.T) {
	t.Setenv("FAKE_HANG", "1")
	h := startHangupRun(t, nil, false, false)
	runner := h.runnerPid()
	if err := h.reader.Close(); err != nil {
		t.Fatal(err)
	}

	h.hangUp()
	h.await()

	h.wantWoundDown(runner)
	if h.cmd.ProcessState.ExitCode() != 130 {
		t.Errorf("sel: %s, want exit 130 even though nothing could be written", h.cmd.ProcessState)
	}
}

func TestHangupIgnoredAtStartupLeavesARunningSelTestAlone(t *testing.T) {
	t.Run("a hung runner keeps running until it is told to stop", func(t *testing.T) {
		t.Setenv("FAKE_HANG", "1")
		h := startHangupRun(t, nil, false, true)
		runner := h.runnerPid()

		h.hangUp()
		time.Sleep(time.Second)

		select {
		case <-h.exited:
			t.Fatalf("sel: %s, want it still running after a SIGHUP that was ignored at startup", h.cmd.ProcessState)
		default:
		}
		if err := syscall.Kill(runner, 0); err != nil {
			t.Errorf("the runner is gone after an ignored SIGHUP: %v", err)
		}
		_ = syscall.Kill(-h.cmd.Process.Pid, syscall.SIGTERM)
		h.await()
		h.wantWoundDown(runner)
	})

	t.Run("a run in planning still finishes with its normal exit code", func(t *testing.T) {
		api, release := heldAPI(t)
		h := startHangupRun(t, api, false, true)
		awaitHeld(t, api)

		h.hangUp()
		time.Sleep(time.Second)
		release()
		h.await()

		if h.cmd.ProcessState.ExitCode() != 0 || !strings.Contains(h.out.String(), `"event":"summary"`) {
			t.Errorf("sel: %s, stdout %q; want exit 0 and a summary", h.cmd.ProcessState, h.out.String())
		}
	})
}

func TestHangupDuringPlanningExitsInterruptedWithNothingLeftBehind(t *testing.T) {
	api, _ := heldAPI(t)
	h := startHangupRun(t, api, false, false)
	awaitHeld(t, api)

	h.hangUp()
	h.await()

	h.wantInterruptedSummary()
	leftovers, _ := os.ReadDir(h.tmp)
	if _, err := os.Stat(h.pidFile); err == nil || len(leftovers) != 0 {
		t.Errorf("runner started=%t, temp entries %d; want neither after a hangup during planning", err == nil, len(leftovers))
	}
}

func TestHangupStillKillsACommandOtherThanSelTest(t *testing.T) {
	reached := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		reached <- struct{}{}
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	cmd := exec.Command(os.Args[0], "suites", "--org", "acme", "--json")
	cmd.Env = append(os.Environ(), "SELECTIVE_TEST_RUN_MAIN=1", "SELECTIVE_HOST="+srv.URL, "SELECTIVE_TOKEN=tok", "SELECTIVE_CONFIG_DIR="+t.TempDir())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	select {
	case <-reached:
	case <-time.After(20 * time.Second):
		t.Fatal("sel suites never reached the server")
	}

	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGHUP)
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		t.Fatal("sel suites was still running 10s after SIGHUP, want it killed by the signal as before")
	}

	t.Logf("sel suites after SIGHUP: %s", cmd.ProcessState)
	if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGHUP {
		t.Errorf("sel suites: %s, want it killed by the hangup signal as before", cmd.ProcessState)
	}
}
