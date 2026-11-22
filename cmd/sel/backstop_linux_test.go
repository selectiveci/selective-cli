package main

import (
	"bytes"
	"encoding/json"
	"io"
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
	"unsafe"

	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/localrun"
	"nhooyr.io/websocket"
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

func startWithStdout(t *testing.T, dir, host string, extraEnv []string, stdout *os.File, shared bool, args ...string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "SELECTIVE_TEST_RUN_MAIN=1", "SELECTIVE_NO_UPDATE_NOTIFIER=1", "SELECTIVE_HOST="+host, "SELECTIVE_TOKEN=tok", "SELECTIVE_CONFIG_DIR="+t.TempDir())
	cmd.Env = append(cmd.Env, extraEnv...)
	stderr := &bytes.Buffer{}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if shared {
		cmd.Stderr = stdout
	}
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

func fullPipe(t *testing.T) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	for chunk := []byte(strings.Repeat("x", 4095) + "\n"); ; {
		_ = w.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
		if _, err := w.Write(chunk); err != nil {
			return w
		}
	}
}

func TestTimeoutRemovesTheRunnerAndSeatWhenSelTestsStdoutIsNeverRead(t *testing.T) {
	runnerEnv(t)
	t.Setenv("FAKE_HANG", "1")
	pidFile, siblingFile := filepath.Join(t.TempDir(), "runner.pid"), filepath.Join(t.TempDir(), "sibling.pid")
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
	w := fullPipe(t)
	env := []string{"TMPDIR=" + tmp, "PATH=" + bin + ":" + os.Getenv("PATH")}
	project := pytestProject(t)
	client := "#!/bin/sh\necho selective_pytest >/dev/null\nsleep 300 >/dev/null 2>&1 &\necho $! > " + siblingFile + "\nexec env FAKE_RUNNER=1 " + os.Args[0] + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(project, ".venv/bin/selective"), []byte(client), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd, stderr := startWithStdout(t, project, newFakeAPI(t, selected).srv.URL, env, w, false, "test", "--org", "acme", "--timeout=1s", "--json")
	_ = w.Close()
	exit, took := waitForExit(t, cmd, 30*time.Second)

	body, _ := os.ReadFile(pidFile)
	pid, err := strconv.Atoi(strings.TrimSpace(string(body)))
	if err != nil || pid <= 0 {
		t.Fatalf("the runner never started (exit %d after %s, stderr %q), so this proves nothing", exit, took, stderr.String())
	}
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	siblingBody, _ := os.ReadFile(siblingFile)
	sibling, _ := strconv.Atoi(strings.TrimSpace(string(siblingBody)))
	if sibling <= 0 {
		t.Fatalf("the runner's sibling never started (exit %d after %s), so this proves nothing about the group", exit, took)
	}
	leftovers, _ := os.ReadDir(tmp)
	if exit != 8 || !processGone(sibling) || took < time.Second+localrun.ShutdownBudget || !processGone(-pid) || len(leftovers) != 0 || !strings.Contains(stderr.String(), "Error: ") {
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

func TestTimeoutCapsTheWholeCommandAroundTheUpdateCheck(t *testing.T) {
	cases := map[string]struct {
		args            []string
		fifo, tty, hang bool
		exit            int
	}{
		"state file never reads, output piped":              {fifo: true, exit: contract.ExitNotFound},
		"state file never reads, output on a terminal":      {fifo: true, tty: true, exit: contract.ExitNotFound},
		"command runs to the timeout, output on a terminal": {tty: true, hang: true, exit: contract.ExitTimeout},
		"usage error before the clock arms, output piped":   {args: []string{"approvals", "show"}, fifo: true, exit: contract.ExitUsage},
		"usage error before the clock arms, on a terminal":  {args: []string{"tail", "--timeout=1s", "--bogus"}, fifo: true, tty: true, exit: contract.ExitUsage},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			state := filepath.Join(t.TempDir(), "update-check.json")
			if c.fifo {
				if err := syscall.Mkfifo(state, 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(state, []byte(cacheJSON("")), 0o600); err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if c.hang {
					<-r.Context().Done()
					return
				}
				http.Error(w, `{"error":"not_found"}`, http.StatusNotFound)
			}))
			t.Cleanup(srv.Close)
			_, out := newPipe(t, false)
			master := (*os.File)(nil)
			if c.tty {
				master, out = openPTY(t)
			}
			env := []string{"SELECTIVE_CONFIG_DIR=" + filepath.Dir(state), "SELECTIVE_TEST_VERSION=0.1.0", "SELECTIVE_NO_UPDATE_NOTIFIER=", "CI=", "GITHUB_ACTIONS=", "BUILD_NUMBER=", "RUN_ID=", "CODESPACES="}

			args := c.args
			if args == nil {
				args = []string{"approvals", "show", "0193b6a0-7c1e-7d3a-9f2b-4a5c6d7e8f90"}
			}
			cmd, _ := startWithStdout(t, t.TempDir(), srv.URL, env, out, c.tty, append(args, "--timeout=1s")...)
			exit, took := waitForExit(t, cmd, 5*time.Second)
			var shown []byte
			if c.tty {
				_ = out.Close()
				shown, _ = io.ReadAll(master)
			}

			if noticed := strings.Contains(string(shown), "A new release of sel is available"); exit != c.exit || took > hardBound || noticed != c.hang {
				t.Errorf("exit %d after %s, notice printed %t; want exit %d within %s and the notice only when the command ran to the timeout", exit, took, noticed, c.exit, hardBound)
			}
		})
	}
}

func newPipe(t *testing.T, nonblocking bool) (r, w *os.File) {
	t.Helper()
	flags := syscall.O_CLOEXEC
	if nonblocking {
		flags |= syscall.O_NONBLOCK
	}
	var fds [2]int
	if err := syscall.Pipe2(fds[:], flags); err != nil {
		t.Fatal(err)
	}
	r, w = os.NewFile(uintptr(fds[0]), "pipe-r"), os.NewFile(uintptr(fds[1]), "pipe-w")
	t.Cleanup(func() { _ = r.Close() })
	return r, w
}

func hasNonblockingStdout(t *testing.T, pid int) bool {
	t.Helper()
	info := string(mustRead(t, "/proc/"+strconv.Itoa(pid)+"/fdinfo/1"))
	_, flags, _ := strings.Cut(info, "flags:")
	n, err := strconv.ParseInt(strings.TrimSpace(strings.SplitN(flags, "\n", 2)[0]), 8, 64)
	return err == nil && n&syscall.O_NONBLOCK != 0
}

func pipeBytes(t *testing.T, r *os.File) int {
	t.Helper()
	var n int32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, r.Fd(), syscall.TIOCINQ, uintptr(unsafe.Pointer(&n))); errno != 0 {
		t.Fatal(errno)
	}
	return int(n)
}

func TestTimeoutExitsEightOnTimeWhenStdoutIsBackedUpAndNeverSplicesALine(t *testing.T) {
	cases := map[string]struct {
		lineBytes   int
		nonblocking bool
		shared      bool
		afterExit   bool
	}{
		"blocking pipe, 12000-byte lines":           {12000, false, false, false},
		"blocking pipe, 20000-byte lines":           {20000, false, false, false},
		"nonblocking pipe, 8192-byte lines":         {8192, true, false, false},
		"nonblocking pipe, 20000-byte line":         {20000, true, false, false},
		"stdout and stderr on one pipe, 12000-byte": {12000, false, true, false},
		"stdout and stderr on one pipe, 20000-byte": {20000, false, true, false},

		"blocking pipe, 12000-byte lines, read only after exit":           {lineBytes: 12000, afterExit: true},
		"nonblocking pipe, 8192-byte lines, read only after exit":         {lineBytes: 8192, nonblocking: true, afterExit: true},
		"stdout and stderr on one pipe, 12000-byte, read only after exit": {lineBytes: 12000, shared: true, afterExit: true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			pad := strings.Repeat("x", c.lineBytes)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				ctx := conn.CloseRead(r.Context())
				for i := 0; conn.Write(ctx, websocket.MessageText, []byte(`{"event":"progress","completed":`+strconv.Itoa(i)+`,"total":99999,"percent":0,"failed":0,"pad":"`+pad+`"}`)) == nil; i++ {
					time.Sleep(5 * time.Millisecond)
				}
			}))
			t.Cleanup(srv.Close)
			r, w := newPipe(t, c.nonblocking)
			cmd, stderr := startWithStdout(t, t.TempDir(), srv.URL, nil, w, c.shared, "tail", "--run-id", "r1", "--timeout=1s", "--json")
			if c.nonblocking && !hasNonblockingStdout(t, cmd.Process.Pid) {
				t.Fatal("the child's stdout was not O_NONBLOCK when it started, so this case proves nothing")
			}
			_ = w.Close()

			time.Sleep(500 * time.Millisecond)
			before := pipeBytes(t, r)
			time.Sleep(300 * time.Millisecond)
			if after := pipeBytes(t, r); before == 0 || after != before {
				t.Fatalf("pipe held %d bytes, then %d: the writer was not stalled, so this proves nothing", before, after)
			}
			t.Logf("writer stalled with %d bytes in the pipe at 800ms", before)
			var dump []byte
			drained, exited := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(drained)
				if c.afterExit {
					<-exited
				} else {
					time.Sleep(550 * time.Millisecond)
				}
				_ = syscall.SetNonblock(int(r.Fd()), false)
				dump, _ = io.ReadAll(r)
			}()
			exit, took := waitForExit(t, cmd, 5*time.Second)
			close(exited)
			<-drained
			lines := strings.Split(string(dump), "\n")
			for i, line := range lines[:len(lines)-1] {
				if !json.Valid([]byte(line)) && !(c.shared && strings.HasPrefix(line, "Error: ") && !strings.Contains(line, `{"`)) {
					t.Fatalf("stdout line %d is not complete JSON (%d bytes): spliced; head %.60q, last 600 bytes %q", i, len(line), line, line[max(0, len(line)-600):])
				}
			}
			if tail := lines[len(lines)-1]; strings.Contains(tail, `"error"`) || strings.Contains(tail, "Error:") {
				t.Errorf("an error object was glued onto an unfinished line: %.80q", tail)
			}
			if c.shared == strings.HasPrefix(stderr.String(), "Error: ") || exit != 8 || took > hardBound {
				t.Errorf("exit %d after %s, stderr %q; want exit 8 within %s and an Error: line on a stderr of its own", exit, took, stderr.String(), hardBound)
			}
		})
	}
}

func TestInterruptBeforeTheDeadlineStillExitsOneThirtyUnderATimeout(t *testing.T) {
	runnerEnv(t)
	t.Setenv("FAKE_HANG", "1")
	ready := func(stdout func() string) {
		waitUntil(t, "the selection event", func() bool { return strings.Contains(stdout(), `"event":"selection"`) })
	}

	events, code := interruptedRun(t, newFakeAPI(t, selected), ready, "--timeout", "5m")

	if last := events[len(events)-1]; code != 130 || last["event"] != "summary" || last["interrupted"] != true {
		t.Errorf("exit %d, last event %v; want exit 130 and an interrupted summary", code, last)
	}
}
