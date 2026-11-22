package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

func stalledConfigDir(t *testing.T, releaseAfter time.Duration) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if releaseAfter > 0 {
		go func() {
			time.Sleep(releaseAfter)
			if f, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
				_, _ = f.WriteString("{}")
				_ = f.Close()
			}
		}()
	}
	return dir
}

func runCapped(t *testing.T, limit time.Duration, dir, configDir, host string, extraEnv []string, args ...string) (res cliResult, took time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "SELECTIVE_TEST_RUN_MAIN=1", "SELECTIVE_NO_UPDATE_NOTIFIER=1", "SELECTIVE_HOST="+host, "SELECTIVE_TOKEN=tok", "SELECTIVE_CONFIG_DIR="+configDir)
	cmd.Env = append(cmd.Env, extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	began := time.Now()
	err := cmd.Run()
	took = time.Since(began)
	if ctx.Err() != nil {
		t.Fatalf("still running after %s: --timeout did not end the command", limit)
	}
	res = cliResult{stdout: stdout.String(), stderr: stderr.String()}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.exit = exitErr.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return res, took
}

func stdoutLines(t *testing.T, stdout string) []map[string]any {
	t.Helper()
	var objects []map[string]any
	for i, line := range strings.Split(strings.TrimSuffix(stdout, "\n"), "\n") {
		var object map[string]any
		if err := json.Unmarshal([]byte(line), &object); err != nil {
			t.Fatalf("stdout line %d is not one JSON object: %q", i, line)
		}
		objects = append(objects, object)
	}
	return objects
}

func assertTimeoutLast(t *testing.T, res cliResult) {
	t.Helper()
	lines := stdoutLines(t, res.stdout)
	if last := lines[len(lines)-1]; res.exit != 8 || last["error"] != "timeout" || last["v"] != float64(1) {
		t.Errorf("exit %d, last line %v; want exit 8 and the timeout object last", res.exit, last)
	}
}

func TestTimeoutEndsACommandWhoseSetupReadStalls(t *testing.T) {
	cases := map[string]stalledSetupCase{
		"test": {[]string{"test", "--org", "acme", "--against", "HEAD"}, 0},
		"tail": {[]string{"tail", "--run-id", "r1"}, 1500 * time.Millisecond},
	}
	runStalledSetupCases(t, cases)
}

func TestTimeoutAfterASlowSetupStillExitsEightAsOnMain(t *testing.T) {
	cases := map[string]stalledSetupCase{
		"approvals show": {[]string{"approvals", "show", "0193b6a0-7c1e-7d3a-9f2b-4a5c6d7e8f90"}, 1500 * time.Millisecond},
		"doctor":         {[]string{"doctor"}, 0},
		"suites delete":  {[]string{"suites", "delete", "old-minitest", "--yes", "--org", "acme"}, 1500 * time.Millisecond},
	}
	runStalledSetupCases(t, cases)
}

type stalledSetupCase struct {
	args         []string
	releaseAfter time.Duration
}

func runStalledSetupCases(t *testing.T, cases map[string]stalledSetupCase) {
	t.Helper()
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := gitRepo(t)
			if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[tool.pytest.ini_options]\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			res, took := runCapped(t, 5*time.Second, dir, stalledConfigDir(t, c.releaseAfter), "http://127.0.0.1:1", nil, append(c.args, "--timeout=1s", "--json")...)

			assertTimeoutLast(t, res)
			if took > 2*time.Second {
				t.Errorf("exited after %s, want within 2s of a --timeout=1s", took)
			}
		})
	}
}

func TestTimeoutInPrettyModeWritesTheErrorToStderrAndNothingToStdout(t *testing.T) {
	res, _ := runCapped(t, 5*time.Second, t.TempDir(), stalledConfigDir(t, 1500*time.Millisecond), "http://127.0.0.1:1", nil, "tail", "--run-id", "r1", "--timeout=1s", "--format", "pretty")

	if res.exit != 8 || res.stdout != "" || !strings.HasPrefix(res.stderr, "Error: ") {
		t.Errorf("exit %d stdout %q stderr %q, want exit 8, empty stdout and an Error: line", res.exit, res.stdout, res.stderr)
	}
}

func TestTimeoutAfterASlowSetupEndsAStreamThroughTheCommandsOwnDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ctx := c.CloseRead(r.Context())
		for i := 0; c.Write(ctx, websocket.MessageText, []byte(`{"event":"progress","completed":`+strconv.Itoa(i)+`,"total":99999,"percent":0,"failed":0}`)) == nil; i++ {
			time.Sleep(10 * time.Millisecond)
		}
	}))
	t.Cleanup(srv.Close)
	const limit = 1500 * time.Millisecond

	res, took := runCapped(t, 5*time.Second, t.TempDir(), stalledConfigDir(t, 600*time.Millisecond), srv.URL, nil, "tail", "--run-id", "r1", "--timeout", limit.String(), "--json")

	assertTimeoutLast(t, res)
	if n := len(stdoutLines(t, res.stdout)); n < 10 || took >= limit+300*time.Millisecond {
		t.Errorf("%d lines in %s; want a stream of lines and an exit within 300ms of the %s limit", n, took, limit)
	}
}

func TestTimeoutNamesDoctorsFailedCheckAndKeepsItsReportOnStderrWhenTheConfigReadStalls(t *testing.T) {
	res, _ := runCapped(t, 5*time.Second, t.TempDir(), stalledConfigDir(t, 0), "http://127.0.0.1:1", nil, "doctor", "--timeout=1s", "--json")

	assertTimeoutLast(t, res)
	lines := stdoutLines(t, res.stdout)
	last := lines[len(lines)-1]
	if _, kept := last["doctor"]; kept || last["failed_check"] == nil || !strings.Contains(res.stderr, `{"v":1,"doctor":{"status":"fail","checks":[`) {
		t.Errorf("last line %v, stderr %.80q; want failed_check on stdout and the per-check report on stderr", last, res.stderr)
	}
}

func hungGit(t *testing.T) (env []string, pidFile string) {
	t.Helper()
	bin, pidFile := t.TempDir(), filepath.Join(t.TempDir(), "git.pid")
	script := "#!/bin/sh\necho $$ > " + pidFile + "\nexec sleep 300\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{"PATH=" + bin + ":" + os.Getenv("PATH")}, pidFile
}

func TestTimeoutLeavesNoHungGitBehind(t *testing.T) {
	cases := map[string][]string{
		"tail": {"tail"},
		"test": {"test", "--org", "acme"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[tool.pytest.ini_options]\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			env, pidFile := hungGit(t)
			t.Cleanup(func() {
				if body, err := os.ReadFile(pidFile); err == nil {
					if pid, err := strconv.Atoi(strings.TrimSpace(string(body))); err == nil {
						_ = syscall.Kill(pid, syscall.SIGKILL)
					}
				}
			})

			res, _ := runCapped(t, 5*time.Second, dir, t.TempDir(), "http://127.0.0.1:1", env, append(args, "--timeout=1s", "--json")...)

			body, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatalf("git never ran, so this proves nothing: %v", err)
			}
			pid, _ := strconv.Atoi(strings.TrimSpace(string(body)))
			assertTimeoutLast(t, res)
			if !processGone(pid) {
				t.Errorf("git pid %d is still alive after the timeout exit", pid)
			}
		})
	}
}
