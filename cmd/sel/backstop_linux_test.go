package main

import (
	"bytes"
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

const hardBound = time.Second + 500*time.Millisecond + 250*time.Millisecond

func TestTimeoutEndsACommandWhoseConfigReadNeverReturns(t *testing.T) {
	cases := map[string][]string{
		"approvals show": {"approvals", "show", "0193b6a0-7c1e-7d3a-9f2b-4a5c6d7e8f90"},
		"tail":           {"tail", "--run-id", "r1"},
		"suites delete":  {"suites", "delete", "old-minitest", "--yes", "--org", "acme"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			res, took := runCapped(t, 5*time.Second, gitRepo(t), stalledConfigDir(t, 0), "http://127.0.0.1:1", nil, append(args, "--timeout=1s", "--json")...)

			assertTimeoutLast(t, res)
			if !strings.HasPrefix(res.stderr, "Error: ") || took > hardBound {
				t.Errorf("exit after %s with stderr %q; want an Error: line within %s", took, res.stderr, hardBound)
			}
		})
	}
}

func TestTimeoutInPrettyModeEndsATailWhoseConfigReadNeverReturns(t *testing.T) {
	res, took := runCapped(t, 5*time.Second, t.TempDir(), stalledConfigDir(t, 0), "http://127.0.0.1:1", nil, "tail", "--run-id", "r1", "--timeout=1s", "--format", "pretty")

	if res.exit != 8 || res.stdout != "" || !strings.HasPrefix(res.stderr, "Error: ") {
		t.Errorf("exit %d after %s, stdout %q stderr %q; want exit 8, empty stdout and an Error: line", res.exit, took, res.stdout, res.stderr)
	}
}

func startWithStdout(t *testing.T, dir, host string, extraEnv []string, stdout *os.File, args ...string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "SELECTIVE_TEST_RUN_MAIN=1", "SELECTIVE_NO_UPDATE_NOTIFIER=1", "SELECTIVE_HOST="+host, "SELECTIVE_TOKEN=tok", "SELECTIVE_CONFIG_DIR="+t.TempDir())
	cmd.Env = append(cmd.Env, extraEnv...)
	stderr := &bytes.Buffer{}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return cmd, stderr
}

func waitForExit(t *testing.T, cmd *exec.Cmd, limit time.Duration) (exit int, took time.Duration) {
	t.Helper()
	began := time.Now()
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("still running after %s", limit)
	}
	return cmd.ProcessState.ExitCode(), time.Since(began)
}

func TestTimeoutRemovesTheRunnerAndSeatWhenSelTestsStdoutIsNeverRead(t *testing.T) {
	runnerEnv(t)
	t.Setenv("FAKE_HANG", "1")
	pidFile := filepath.Join(t.TempDir(), "runner.pid")
	t.Setenv("FAKE_PID_FILE", pidFile)
	tmp, err := os.MkdirTemp("/tmp", "k")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	bin, gitPids := t.TempDir(), filepath.Join(t.TempDir(), "git.pids")
	shim := "#!/bin/sh\necho $$ >> " + gitPids + "\nPATH=" + os.Getenv("PATH") + "\nexec git \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	for chunk := []byte(strings.Repeat("x", 4095) + "\n"); ; {
		_ = w.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
		if _, err := w.Write(chunk); err != nil {
			break
		}
	}
	env := []string{"TMPDIR=" + tmp, "PATH=" + bin + ":" + os.Getenv("PATH")}
	project := pytestProject(t)
	client := "#!/bin/sh\necho selective_pytest >/dev/null\nsleep 300 >/dev/null 2>&1 &\nexec env FAKE_RUNNER=1 " + os.Args[0] + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(project, ".venv/bin/selective"), []byte(client), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd, stderr := startWithStdout(t, project, newFakeAPI(t, selected).srv.URL, env, w, "test", "--org", "acme", "--timeout=1s", "--json")
	_ = w.Close()
	exit, took := waitForExit(t, cmd, 30*time.Second)

	body, _ := os.ReadFile(pidFile)
	pid, err := strconv.Atoi(strings.TrimSpace(string(body)))
	if err != nil || pid <= 0 {
		t.Fatalf("the runner never started (exit %d after %s, stderr %q), so this proves nothing", exit, took, stderr.String())
	}
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	leftovers, _ := os.ReadDir(tmp)
	if exit != 8 || took < time.Second+localrun.ShutdownBudget || !processGone(-pid) || len(leftovers) != 0 || !strings.Contains(stderr.String(), "Error: ") {
		t.Errorf("exit %d after %s, process group gone=%t, temp entries %d, stderr %q; want the backstop's exit 8 after the wind-down budget, an empty process group, no seat dir and an Error: line", exit, took, processGone(-pid), len(leftovers), stderr.String())
	}
	pids := strings.Fields(string(mustRead(t, gitPids)))
	if len(pids) == 0 {
		t.Fatal("git never ran, so the no-git-child check proves nothing")
	}
	for _, p := range pids {
		if n, _ := strconv.Atoi(p); !processGone(n) {
			t.Errorf("git pid %d is still alive after the timeout exit", n)
		}
	}
}
