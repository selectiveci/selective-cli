package localrun

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/contract"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "__transport" {
		os.Exit(RunSeat(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

type launched struct {
	launch Launch
	stderr *bytes.Buffer
	events *bytes.Buffer
	dir    string
}

func scriptedClient(t *testing.T, script string) *launched {
	t.Helper()
	root := project(t, map[string]string{"Gemfile.lock": lockWithCore("0.3.0"), "bin/bundle": "#!/bin/sh\n" + script})
	t.Setenv("PATH", filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SELECTIVE_API_KEY", "sk_live_SHOULDNOTLEAK")
	t.Setenv("SELECTIVE_TOKEN", "tok_SHOULDNOTLEAK")
	stderr := &bytes.Buffer{}
	return &launched{
		launch: Launch{Root: root, Framework: RSpec, Sel: os.Args[0], Stderr: stderr, Grace: 150 * time.Millisecond},
		stderr: stderr, events: &bytes.Buffer{}, dir: root,
	}
}

func (l *launched) run(ctx context.Context) (Summary, error) {
	return l.launch.Run(ctx, l.events, Options{Selector: selecting("a"), Timeout: 5 * time.Second})
}

func awaitFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if body, err := os.ReadFile(path); err == nil && len(body) > 0 {
			return strings.TrimSpace(string(body))
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never appeared", path)
	return ""
}

func gone(pid int) bool {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func TestChildEnvReplacesEverySelectiveVariableAndKeepsTheRest(t *testing.T) {
	parent := []string{"HOME=/h", "SELECTIVE_API_KEY=sk_live_PARENT", "SELECTIVE_TOKEN=tok_PARENT", "SELECTIVE_HOST=wss://app", "SELECTIVE_TRANSPORT=/real", "PATH=/bin"}
	seat := []string{"SELECTIVE_TRANSPORT=/seat/transport", "SELECTIVE_SEAT_SOCKET=/seat/s.sock"}
	env := childEnv(parent, seat, t.TempDir())
	counts := map[string]int{}
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		counts[name]++
		if strings.Contains(kv, "PARENT") || strings.Contains(kv, "wss://app") || strings.Contains(kv, "/real") {
			t.Errorf("a parent value survived: %q", kv)
		}
	}
	for _, want := range []string{"HOME=/h", "PATH=/bin", "SELECTIVE_API_KEY=sel-local", "SELECTIVE_HOST=ws://localhost", "SELECTIVE_TRANSPORT=/seat/transport", "SELECTIVE_SEAT_SOCKET=/seat/s.sock"} {
		if !contains(env, want) {
			t.Errorf("env lacks %q", want)
		}
	}
	for name, n := range counts {
		if n != 1 {
			t.Errorf("%s appears %d times, want once", name, n)
		}
	}
	if counts["SELECTIVE_TOKEN"] != 0 {
		t.Error("SELECTIVE_TOKEN reached the child")
	}
	if a, b := envValue(childEnv(nil, nil, t.TempDir()), "SELECTIVE_RUN_ID"), envValue(env, "SELECTIVE_RUN_ID"); !strings.HasPrefix(a, "local-") || a == b {
		t.Errorf("run ids %q and %q must start with local- and differ per launch", a, b)
	}
}

func contains(env []string, want string) bool {
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}

func TestLaunchGivesTheClientOnlyPlaceholderCredentials(t *testing.T) {
	l := scriptedClient(t, "env > \"$DUMP\"\n")
	dump := filepath.Join(l.dir, "env.txt")
	t.Setenv("DUMP", dump)
	_, _ = l.run(context.Background())
	got := awaitFile(t, dump)
	if !strings.Contains(got, "SELECTIVE_API_KEY=sel-local\n") || strings.Contains(got, "SHOULDNOTLEAK") || strings.Contains(got, "SELECTIVE_TOKEN") {
		t.Errorf("child environment leaked a parent credential or lacks the placeholder:\n%s", got)
	}
}

func TestLaunchSendsTheChildsStdoutAndStderrToTheCLIsStderr(t *testing.T) {
	l := scriptedClient(t, "echo from-stdout \"$@\"\necho from-stderr >&2\nexit 4\n")
	l.launch.Args = []string{"spec/a_spec.rb"}
	_, err := l.run(context.Background())
	if l.events.Len() != 0 {
		t.Errorf("stdout got %q, want nothing", l.events)
	}
	if !strings.Contains(l.stderr.String(), "from-stdout exec selective rspec spec/a_spec.rb") || !strings.Contains(l.stderr.String(), "from-stderr") {
		t.Errorf("stderr %q lacks the child's output", l.stderr)
	}
	if codeOf(err) != contract.RunnerError || !strings.Contains(err.Error(), "status 4") {
		t.Errorf("err %v, want runner_error naming status 4 for a client that exited before connecting", err)
	}
}

func TestLaunchReportsAClientThatDiesMidRunAsRunnerErrorNotAsItsExitStatus(t *testing.T) {
	manifest := `{"type":"test_manifest","data":{"test_cases":[{"id":"a","file_path":"f"},{"id":"b","file_path":"f"}]}}`
	l := scriptedClient(t, "{ printf '%s\\n' '"+manifest+"'; sleep 0.3; } | \"$SELECTIVE_TRANSPORT\" --stdio >/dev/null\nexit 0\n")
	_, err := l.run(context.Background())
	if codeOf(err) != contract.RunnerError || !strings.Contains(err.Error(), "went away") {
		t.Errorf("err %v, want runner_error from the protocol engine even though the client exited 0", err)
	}
}

func TestLaunchForwardsInterruptToTheClientsOwnProcessGroupAndReportsInterrupted(t *testing.T) {
	l := scriptedClient(t, "echo $$ > \"$PIDF\"\ntrap 'echo int > \"$MARK\"; exit 0' INT\nsleep 30 &\necho $! > \"$CHILDF\"\nwait\n")
	pidf, mark, childf := filepath.Join(l.dir, "pid"), filepath.Join(l.dir, "mark"), filepath.Join(l.dir, "child")
	t.Setenv("PIDF", pidf)
	t.Setenv("MARK", mark)
	t.Setenv("CHILDF", childf)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := l.run(ctx); done <- err }()
	pid, _ := strconv.Atoi(awaitFile(t, pidf))
	child, _ := strconv.Atoi(awaitFile(t, childf))
	if pgid, _ := syscall.Getpgid(pid); pgid != pid || pgid == syscall.Getpgrp() {
		t.Errorf("client pgid %d, want its own group (its pid %d), not ours %d", pgid, pid, syscall.Getpgrp())
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrInterrupted) {
			t.Errorf("err %v, want ErrInterrupted", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Launch did not return after the interrupt")
	}
	if got := awaitFile(t, mark); got != "int" {
		t.Errorf("client saw %q, want SIGINT", got)
	}
	if !gone(pid) || !gone(child) {
		t.Error("a process of the client's group survived")
	}
}

func TestLaunchEscalatesToKillWhenTheClientIgnoresInterruptAndTerminate(t *testing.T) {
	l := scriptedClient(t, "trap '' INT TERM\necho $$ > \"$PIDF\"\nwhile :; do sleep 0.05; done\n")
	pidf := filepath.Join(l.dir, "pid")
	t.Setenv("PIDF", pidf)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := l.run(ctx); done <- err }()
	pid, _ := strconv.Atoi(awaitFile(t, pidf))
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrInterrupted) {
			t.Errorf("err %v, want ErrInterrupted", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Launch did not return: the client was never killed")
	}
	if !gone(pid) {
		t.Error("the client survived the kill")
	}
}

func TestLaunchSweepsGrandchildrenAndDoesNotHangOnTheirOpenPipes(t *testing.T) {
	l := scriptedClient(t, "sleep 60 &\necho $! > \"$CHILDF\"\nexit 0\n")
	childf := filepath.Join(l.dir, "child")
	t.Setenv("CHILDF", childf)
	started := time.Now()
	_, _ = l.run(context.Background())
	if time.Since(started) > 5*time.Second {
		t.Error("Launch waited for a grandchild that holds the output pipe")
	}
	child, _ := strconv.Atoi(awaitFile(t, childf))
	if !gone(child) {
		t.Error("the grandchild outlived the launch")
	}
}

func TestLaunchReportsAMissingBundleAsRunnerMissing(t *testing.T) {
	l := scriptedClient(t, "exit 0\n")
	t.Setenv("PATH", t.TempDir())
	_, err := l.run(context.Background())
	if codeOf(err) != contract.RunnerMissing || !strings.Contains(err.Error(), "bundle") {
		t.Errorf("err %v, want runner_missing naming bundle", err)
	}
}
