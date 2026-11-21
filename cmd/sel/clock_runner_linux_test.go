package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/localrun"
	"github.com/spf13/cobra"
)

const runnerChildEnv = "SEL_CLOCK_RUNNER_CHILD_ROOT"

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
	runnerPid, err = strconv.Atoi(strings.TrimSpace(string(body)))
	if err != nil || runnerPid <= 0 {
		t.Fatalf("the runner's pid file holds %q, not a pid: %v", body, err)
	}
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

func TestBackstopFiringBeforeHoldBackstopStillRefusesALaterRunner(t *testing.T) {
	if root := os.Getenv(runnerChildEnv); root != "" {
		launchAfterTheBackstopFired(root, os.Getenv("SEL_CLOCK_RUNNER_PID_FILE"))
		return
	}
	root, pidFile := pytestProject(t), filepath.Join(t.TempDir(), "runner.pid")
	client := "#!/bin/sh\necho selective_pytest >/dev/null\necho $$ > " + pidFile + "\nexec sleep 300\n"
	if err := os.WriteFile(filepath.Join(root, ".venv/bin/selective"), []byte(client), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		body, _ := os.ReadFile(pidFile)
		if pid, _ := strconv.Atoi(strings.TrimSpace(string(body))); pid > 0 {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	})
	child := exec.Command(os.Args[0], "-test.run=^TestBackstopFiringBeforeHoldBackstopStillRefusesALaterRunner$")
	child.Env = append(os.Environ(), runnerChildEnv+"="+root, "SEL_CLOCK_RUNNER_PID_FILE="+pidFile)

	out, err := child.CombinedOutput()

	if err != nil {
		t.Errorf("child failed: %v\n%s\nwant no runner to start after the backstop fired", err, out)
	}
}

func launchAfterTheBackstopFired(root, pidFile string) {
	c := newClock(&cobra.Command{}, nil, io.Discard, io.Discard)
	c.exit = func(int) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = localrun.Launch{Root: root, Framework: localrun.Pytest, Sel: os.Args[0], Stderr: io.Discard}.Run(ctx, io.Discard, localrun.Options{Timeout: 2 * time.Second})
		if _, err := os.Stat(pidFile); err == nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
	c.arm(time.Now().Add(-time.Second), contract.Errorf(contract.Timeout, "--timeout expired"), &guard{w: io.Discard}, false)
	select {}
}
