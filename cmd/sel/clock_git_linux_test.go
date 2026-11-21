package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/gitctx"
	"github.com/spf13/cobra"
)

const gitChildEnv = "SEL_CLOCK_GIT_CHILD_PID_FILE"

func TestArmingTheClockRegistersTheKillOfAnyRunningGit(t *testing.T) {
	if pidFile := os.Getenv(gitChildEnv); pidFile != "" {
		fireWithAHungGitTheContextCannotReach(pidFile)
		return
	}
	env, pidFile := hungGit(t)
	t.Cleanup(func() {
		if body, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(body))); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestArmingTheClockRegistersTheKillOfAnyRunningGit$")
	child.Env = append(append(os.Environ(), env...), gitChildEnv+"="+pidFile)

	out, err := child.CombinedOutput()

	if err != nil {
		t.Errorf("child failed: %v\n%s\nwant the git to be gone when the backstop exits", err, out)
	}
}

func fireWithAHungGitTheContextCannotReach(pidFile string) {
	c := newClock(&cobra.Command{}, nil, io.Discard, io.Discard)
	c.exit = func(int) {
		body, _ := os.ReadFile(pidFile)
		pid, _ := strconv.Atoi(strings.TrimSpace(string(body)))
		if processGone(pid) {
			os.Exit(0)
		}
		os.Exit(3)
	}
	go func() { _, _ = gitctx.Output(gitctx.Command(context.Background(), "status")) }()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if body, err := os.ReadFile(pidFile); err == nil && strings.HasSuffix(string(body), "\n") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.arm(time.Now().Add(-time.Second), contract.Errorf(contract.Timeout, "--timeout expired"), &guard{w: io.Discard}, false)
	select {}
}
