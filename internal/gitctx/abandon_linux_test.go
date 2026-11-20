package gitctx

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

func TestAbandonKillsARunningGitAndFreezesTheCallerAndLaterCalls(t *testing.T) {
	bin, pidFile := t.TempDir(), filepath.Join(t.TempDir(), "git.pid")
	script := "#!/bin/sh\necho $$ > " + pidFile + "\nexec sleep 300\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Cleanup(func() {
		live.Lock()
		live.abandoned = false
		live.Unlock()
	})
	returned := make(chan struct{}, 2)
	go func() {
		_, _ = Repo()
		returned <- struct{}{}
	}()
	var pid int
	for deadline := time.Now().Add(5 * time.Second); pid == 0 && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		body, _ := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(body)))
	}
	if pid == 0 {
		t.Fatal("the fake git never started")
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	Abandon()
	go func() {
		_, _ = Repo()
		returned <- struct{}{}
	}()

	killed := false
	for deadline := time.Now().Add(3 * time.Second); !killed && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		killed = errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
	}
	select {
	case <-returned:
		t.Error("a git call returned after Abandon, want it to block for good")
	case <-time.After(300 * time.Millisecond):
	}
	if !killed {
		t.Errorf("git pid %d is still alive after Abandon", pid)
	}
}
