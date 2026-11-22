package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type stuckLaunchResult struct {
	exit           int
	stdout, stderr string
}

func blockedOnFifo(pid int) bool {
	dir := fmt.Sprintf("/proc/%d/task", pid)
	tasks, _ := os.ReadDir(dir)
	for _, task := range tasks {
		wchan, _ := os.ReadFile(filepath.Join(dir, task.Name(), "wchan"))
		if strings.Contains(string(wchan), "fifo") || strings.Contains(string(wchan), "wait_for_partner") {
			return true
		}
	}
	return false
}

func stuckLaunchRun(t *testing.T, sig syscall.Signal) stuckLaunchResult {
	t.Helper()
	runnerEnv(t)
	project := pytestProject(t)
	client := filepath.Join(project, ".venv/bin/selective")
	if err := os.Remove(client); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(client, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(client, 0o755); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(os.Args[0], "test", "--org", "acme", "--timeout="+raceTimeout.String(), "--json")
	cmd.Dir = project
	cmd.Env = append(os.Environ(), "SELECTIVE_TEST_RUN_MAIN=1", "SELECTIVE_NO_UPDATE_NOTIFIER=1", "SELECTIVE_HOST="+newFakeAPI(t, selected).srv.URL, "SELECTIVE_TOKEN=tok", "SELECTIVE_CONFIG_DIR="+t.TempDir())
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	began := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	blocked := false
	for time.Since(began) < 2*time.Second && !blocked {
		if blocked = blockedOnFifo(cmd.Process.Pid); !blocked {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if !blocked {
		t.Fatalf("VOID: sel never blocked on the client FIFO; stdout %q stderr %q", stdout.String(), stderr.String())
	}
	time.Sleep(100 * time.Millisecond)
	sentAt := time.Since(began)
	if sig != 0 {
		if sentAt > raceTimeout-time.Second {
			t.Fatalf("VOID: the signal would land at %s, too near the %s deadline", sentAt, raceTimeout)
		}
		if err := syscall.Kill(cmd.Process.Pid, sig); err != nil {
			t.Fatal(err)
		}
	}
	exit, took := waitForExit(t, cmd, 30*time.Second)
	t.Logf("pid %d blocked on the FIFO, signal %d sent at %s, exit %d after %s, stdout %q, stderr %q", cmd.Process.Pid, sig, sentAt.Round(time.Millisecond), exit, took.Round(time.Millisecond), stdout.String(), stderr.String())
	return stuckLaunchResult{exit, stdout.String(), stderr.String()}
}

func wantOneInterruptedObject(t *testing.T, stdout string) {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal([]byte(stdout), &object); err != nil || strings.Count(stdout, "\n") != 1 || !strings.HasSuffix(stdout, "\n") || object["error"] != "interrupted" || object["v"] != float64(1) || strings.Contains(stdout, `"error":"timeout"`) {
		t.Errorf("stdout %q; want exactly one newline-terminated interrupted object and no timeout object", stdout)
	}
}

func wantOneTimeoutObject(t *testing.T, stdout string) {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal([]byte(stdout), &object); err != nil || strings.Count(stdout, "\n") != 1 || object["error"] != "timeout" {
		t.Errorf("stdout %q; want exactly one timeout object", stdout)
	}
}

func TestAStuckLaunchAfterACtrlCLeavesOneInterruptedObject(t *testing.T) {
	res := stuckLaunchRun(t, syscall.SIGINT)

	if res.exit != 130 {
		t.Errorf("exit %d; want 130", res.exit)
	}
	wantOneInterruptedObject(t, res.stdout)
	if strings.Count(res.stderr, "Interrupted") != 1 {
		t.Errorf("stderr %q; want one Interrupted line", res.stderr)
	}
}

func TestAStuckLaunchWithNoCtrlCKeepsItsTimeoutExit(t *testing.T) {
	for name, sig := range map[string]syscall.Signal{"no signal": 0, "SIGTERM": syscall.SIGTERM} {
		t.Run(name, func(t *testing.T) {
			res := stuckLaunchRun(t, sig)

			if res.exit != 8 || !strings.Contains(res.stderr, "Error: --timeout") {
				t.Errorf("exit %d, stderr %q; want 8 and the timeout notice", res.exit, res.stderr)
			}
			wantOneTimeoutObject(t, res.stdout)
		})
	}
}
