package gitctx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func gitShim(t *testing.T, body string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() {
		running.Lock()
		running.killed = false
		running.Unlock()
	})
}

func waitForPid(t *testing.T, path string) int {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if body, err := os.ReadFile(path); err == nil && strings.HasSuffix(string(body), "\n") {
			pid, _ := strconv.Atoi(strings.TrimSpace(string(body)))
			return pid
		}
	}
	t.Fatalf("%s never appeared", path)
	return 0
}

func TestKillRunningStopsAGitThatTheContextCannotReach(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	t.Setenv("GIT_PID_FILE", pidFile)
	gitShim(t, "echo $$ > \"$GIT_PID_FILE\"\nexec sleep 300\n")
	finished := make(chan error, 1)
	go func() {
		_, err := Output(Command(context.Background(), "status"))
		finished <- err
	}()
	pid := waitForPid(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	KillRunning()

	select {
	case err := <-finished:
		if err == nil {
			t.Error("the killed git reported success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("git was still running 5s after KillRunning")
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("kill -0 on the git pid returned %v, want ESRCH", err)
	}
}

func TestKillRunningRefusesToStartAnotherGit(t *testing.T) {
	seen := filepath.Join(t.TempDir(), "seen")
	t.Setenv("GIT_SEEN_FILE", seen)
	gitShim(t, "echo ran > \"$GIT_SEEN_FILE\"\n")
	KillRunning()

	err := Run(Command(context.Background(), "status"))

	if !errors.Is(err, ErrKilled) {
		t.Errorf("Run after KillRunning returned %v, want ErrKilled", err)
	}
	if _, statErr := os.Stat(seen); statErr == nil {
		t.Error("git ran after KillRunning")
	}
}

func TestOutputKeepsStderrOnTheExitError(t *testing.T) {
	gitShim(t, "echo no such remote >&2\nexit 2\n")

	_, err := Output(Command(context.Background(), "remote", "get-url", "origin"))

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || strings.TrimSpace(string(exitErr.Stderr)) != "no such remote" {
		t.Errorf("error %v, want an ExitError whose Stderr is %q", err, "no such remote")
	}
}
