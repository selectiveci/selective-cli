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

func TestTimeoutKillsASelTestRunnerThatIgnoresInterruptBeforeTheFallbackExits(t *testing.T) {
	runnerEnv(t)
	t.Setenv("FAKE_HANG", "1")
	t.Setenv("FAKE_IGNORE_SIGINT", "1")
	pidFile := filepath.Join(t.TempDir(), "runner.pid")
	t.Setenv("FAKE_PID_FILE", pidFile)
	api := newFakeAPI(t, selected)

	res, took := runCapped(t, 10*time.Second, pytestProject(t), t.TempDir(), api.srv.URL, nil, "test", "--org", "acme", "--timeout=1s", "--json")

	body, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the runner never started, so this proves nothing: %v", err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(body)))
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	if res.exit != 8 || !processGone(pid) {
		t.Errorf("exit %d after %s, runner pid %d gone=%t; want exit 8 and no runner left alive", res.exit, took, pid, processGone(pid))
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
