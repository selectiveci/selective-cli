package main

import (
	"bytes"
	"context"
	"encoding/json"
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

func stalledConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "config.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runBinaryWithin(t *testing.T, limit time.Duration, dir, configDir string, extraEnv []string, args ...string) (res cliResult, took time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "SELECTIVE_TEST_RUN_MAIN=1", "SELECTIVE_HOST=http://127.0.0.1:1", "SELECTIVE_TOKEN=tok", "SELECTIVE_CONFIG_DIR="+configDir)
	cmd.Env = append(cmd.Env, extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	began := time.Now()
	err := cmd.Run()
	took = time.Since(began)
	if ctx.Err() != nil {
		t.Fatalf("still running after %s: --timeout does not cap the setup read", limit)
	}
	res = cliResult{stdout: stdout.String(), stderr: stderr.String()}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.exit = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res, took
}

func TestTimeoutCapsASetupReadThatNeverReturnsOnEveryCommandWithATimeoutFlag(t *testing.T) {
	const approvalID = "0193b6a0-7c1e-7d3a-9f2b-4a5c6d7e8f90"
	cases := map[string][]string{
		"approvals show": {"approvals", "show", approvalID},
		"tail":           {"tail", "--run-id", "r1"},
		"suites delete":  {"suites", "delete", "old-minitest", "--yes", "--org", "acme"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			res, took := runBinaryWithin(t, 5*time.Second, t.TempDir(), stalledConfigDir(t), nil, append(args, "--timeout=1s", "--json")...)

			lines := strings.Split(strings.TrimSuffix(res.stdout, "\n"), "\n")
			var last struct {
				Error string `json:"error"`
				V     int    `json:"v"`
			}
			if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil || last.Error != "timeout" || last.V != 1 {
				t.Errorf("stdout %q, want a last line that is the timeout error object with v 1", res.stdout)
			}
			if res.exit != 8 || took > 3*time.Second {
				t.Errorf("exit %d after %s, want exit 8 within 3s", res.exit, took)
			}
		})
	}
}

func TestTimeoutCapInPrettyModeWritesTheErrorToStderrAndNothingToStdout(t *testing.T) {
	res, _ := runBinaryWithin(t, 5*time.Second, t.TempDir(), stalledConfigDir(t), nil, "tail", "--run-id", "r1", "--timeout=1s", "--format", "pretty")

	if res.exit != 8 || res.stdout != "" || !strings.HasPrefix(res.stderr, "Error: ") {
		t.Errorf("exit %d stdout %q stderr %q, want exit 8, empty stdout and an Error: line", res.exit, res.stdout, res.stderr)
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

func recordedPid(t *testing.T, path string) int {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no pid recorded: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func killRecordedPidAtCleanup(t *testing.T, path string) {
	t.Cleanup(func() {
		if body, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(body))); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
}

func TestSelTestTimeoutStillReapsTheRunnerBecauseItOwnsItsDeadline(t *testing.T) {
	runnerEnv(t)
	pidFile := filepath.Join(t.TempDir(), "runner.pid")
	t.Setenv("FAKE_HANG", "1")
	t.Setenv("FAKE_PID_FILE", pidFile)
	killRecordedPidAtCleanup(t, pidFile)

	res := selTest(t, pytestProject(t), newFakeAPI(t, selected), "--timeout", "1s")

	pid := recordedPid(t, pidFile)
	if res.exit != 8 || !processGone(pid) {
		t.Errorf("exit %d, runner pid %d gone=%t; want exit 8 and no live runner", res.exit, pid, processGone(pid))
	}
}
