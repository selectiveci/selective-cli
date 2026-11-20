package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func timedOutSelTest(t *testing.T) (res cliResult, took time.Duration, runnerPid int, tmp string) {
	t.Helper()
	runnerEnv(t)
	t.Setenv("FAKE_HANG", "1")
	pidFile := filepath.Join(t.TempDir(), "runner.pid")
	t.Setenv("FAKE_PID_FILE", pidFile)
	tmp, err := os.MkdirTemp("/tmp", "k")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	api := newFakeAPI(t, selected)

	res, took = runCapped(t, 20*time.Second, pytestProject(t), t.TempDir(), api.srv.URL, []string{"TMPDIR=" + tmp}, "test", "--org", "acme", "--timeout=1s", "--json")

	body, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the runner never started, so this proves nothing: %v", err)
	}
	runnerPid, _ = strconv.Atoi(strings.TrimSpace(string(body)))
	t.Cleanup(func() { _ = syscall.Kill(-runnerPid, syscall.SIGKILL) })
	return res, took, runnerPid, tmp
}

func TestTimeoutLetsASelTestRunnerFinishItsInterruptWindDownBeforeExiting(t *testing.T) {
	done := filepath.Join(t.TempDir(), "done")
	t.Setenv("FAKE_SLOW_STOP_FILE", done)

	res, took, pid, tmp := timedOutSelTest(t)

	finished, _ := os.ReadFile(done)
	leftovers, _ := os.ReadDir(tmp)
	if res.exit != 8 || string(finished) != "done" || !processGone(pid) || len(leftovers) != 0 {
		t.Errorf("exit %d after %s, runner finished its wind-down %q, gone=%t, temp entries %d; want exit 8, a completed wind-down, no runner and no temp dir", res.exit, took, finished, processGone(pid), len(leftovers))
	}
}

func TestTimeoutLeavesNoRunnerOrTempDirWhenTheRunnerIgnoresInterrupt(t *testing.T) {
	t.Setenv("FAKE_IGNORE_SIGINT", "1")

	res, took, pid, tmp := timedOutSelTest(t)

	leftovers, _ := os.ReadDir(tmp)
	if res.exit != 8 || !processGone(pid) || len(leftovers) != 0 {
		t.Errorf("exit %d after %s, runner gone=%t, temp entries %d; want exit 8, no runner and no temp dir", res.exit, took, processGone(pid), len(leftovers))
	}
}

func processGone(pid int) bool {
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return true
		}
	}
	return false
}
