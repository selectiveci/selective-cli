package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/localrun"
)

const windDownBound = localrun.ShutdownBudget + backstopGrace + backstopExit

type uncappedRun struct {
	cmd     *exec.Cmd
	runner  int
	sibling int
	tmp     string
}

func startUncappedRun(t *testing.T, stdout *os.File, extraEnv ...string) *uncappedRun {
	t.Helper()
	tmp, err := os.MkdirTemp("/tmp", "k")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	pidFile, siblingFile := filepath.Join(t.TempDir(), "runner.pid"), filepath.Join(t.TempDir(), "sibling.pid")
	project := pytestProject(t)
	client := "#!/bin/sh\necho selective_pytest >/dev/null\nsleep 300 >/dev/null 2>&1 &\necho $! > " + siblingFile + "\nexec env FAKE_RUNNER=1 " + os.Args[0] + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(project, ".venv/bin/selective"), []byte(client), 0o755); err != nil {
		t.Fatal(err)
	}
	run := &uncappedRun{tmp: tmp}
	run.cmd = exec.Command(os.Args[0], "test", "--org", "acme", "--json")
	run.cmd.Dir = project
	run.cmd.Env = append(os.Environ(),
		"SELECTIVE_TEST_RUN_MAIN=1", "SELECTIVE_NO_UPDATE_NOTIFIER=1", "SELECTIVE_HOST="+newFakeAPI(t, selected).srv.URL, "SELECTIVE_TOKEN=tok", "SELECTIVE_CONFIG_DIR="+t.TempDir(), "TMPDIR="+tmp,
		"FAKE_HANG=1", "FAKE_PID_FILE="+pidFile, "FAKE_IDS=tests/test_calc.py::test_add,tests/test_calc.py::test_sub,tests/test_calc.py::test_mul")
	run.cmd.Env = append(run.cmd.Env, extraEnv...)
	run.cmd.Stdout = stdout
	if err := run.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.cmd.Process.Kill() })
	waitUntil(t, "the runner to start", func() bool {
		body, _ := os.ReadFile(pidFile)
		run.runner, _ = strconv.Atoi(strings.TrimSpace(string(body)))
		return run.runner > 0
	})
	t.Cleanup(func() { _ = syscall.Kill(-run.runner, syscall.SIGKILL) })
	waitUntil(t, "the runner's sibling to start", func() bool {
		body, _ := os.ReadFile(siblingFile)
		run.sibling, _ = strconv.Atoi(strings.TrimSpace(string(body)))
		return run.sibling > 0
	})
	return run
}

func (r *uncappedRun) signal(t *testing.T, sig syscall.Signal) {
	t.Helper()
	if err := syscall.Kill(r.cmd.Process.Pid, sig); err != nil {
		t.Fatal(err)
	}
}

func (r *uncappedRun) wantWoundDown(t *testing.T, bound time.Duration) time.Duration {
	t.Helper()
	exit, took := waitForExit(t, r.cmd, bound)
	leftovers, _ := os.ReadDir(r.tmp)
	if exit != 130 || !processGone(-r.runner) || !processGone(r.sibling) || len(leftovers) != 0 {
		t.Errorf("exit %d after %s, runner group gone=%t, sibling gone=%t, temp entries %d; want 130, an empty runner group and no seat dir", exit, took, processGone(-r.runner), processGone(r.sibling), len(leftovers))
	}
	return took
}

func TestATermOrHangupWithAStalledStdoutReaderStillEndsTheRunWithinTheBudget(t *testing.T) {
	t.Parallel()
	for name, sig := range map[string]syscall.Signal{"term": syscall.SIGTERM, "hup": syscall.SIGHUP} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := fullPipe(t)
			run := startUncappedRun(t, w)
			_ = w.Close()
			waitUntil(t, "sel to block writing to a stdout nobody reads", func() bool { return stdoutWriteBlocked(run.cmd.Process.Pid) })

			run.signal(t, sig)

			run.wantWoundDown(t, windDownBound+execSlack)
		})
	}
}

func TestASlowReaderStillGetsTheInterruptedSummaryLastWhileTheRunnerWindsDown(t *testing.T) {
	t.Parallel()
	r, w := filledPipe(t)
	run := startUncappedRun(t, w, "FAKE_IGNORE_SIGINT=1", "FAKE_LINGER=1")
	_ = w.Close()
	waitUntil(t, "sel to block writing to a stdout nobody reads", func() bool { return stdoutWriteBlocked(run.cmd.Process.Pid) })

	run.signal(t, syscall.SIGTERM)
	var out syncBuffer
	read := make(chan struct{})
	go func() {
		defer close(read)
		for chunk := make([]byte, 4096); ; time.Sleep(50 * time.Millisecond) {
			n, err := r.Read(chunk)
			_, _ = out.Write(chunk[:n])
			if err != nil {
				return
			}
		}
	}()

	took := run.wantWoundDown(t, windDownBound+execSlack)
	select {
	case <-read:
	case <-time.After(5 * time.Second):
		t.Fatal("the reader never reached the end of the output")
	}
	if took < localrun.ShutdownBudget/3 {
		t.Errorf("sel exited after %s; want it to wait out the runner's interrupt grace while the reader keeps reading", took)
	}
	wantSummaryLast(t, strings.ReplaceAll(out.String(), "\x00", ""))
}
