//go:build unix

package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/localrun"
	"github.com/selectiveci/selective-cli/internal/schema"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == "helper" {
		os.Exit(helper(os.Args[2], os.Args[3:]))
	}
	_ = os.Setenv("GORACE", strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	terminateGrace = 300 * time.Millisecond
	pipeDrainWindow = 2 * time.Second
	os.Exit(m.Run())
}

func floodLine(i, width int) string {
	return fmt.Sprintf(`{"v":1,"event":"line","i":%06d,"pad":"%s"}`+"\n", i, strings.Repeat("x", width))
}

func floodTerminal(lines int) string {
	return fmt.Sprintf(`{"v":1,"event":"summary","lines":%d}`+"\n", lines)
}

func floodBody(lines, width int) string {
	var b strings.Builder
	for i := 0; i < lines; i++ {
		b.WriteString(floodLine(i, width))
	}
	b.WriteString(floodTerminal(lines))
	return b.String()
}

func helperFlag(args []string, name string) int {
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "--"+name+"="); ok {
			n, _ := strconv.Atoi(v)
			return n
		}
	}
	return 0
}

func helper(mode string, args []string) int {
	switch mode {
	case "stdin":
		n, _ := io.Copy(io.Discard, os.Stdin)
		fmt.Printf("read:%d\n", n)
	case "env":
		fmt.Println(os.Getenv("SEL_TEST_MARKER"))
	case "exit":
		return helperFlag(args, "code")
	case "flood":
		_, _ = io.WriteString(os.Stdout, floodBody(helperFlag(args, "lines"), helperFlag(args, "width")))
	case "hang":
		grandchild := exec.Command(os.Args[0], "helper", "sleep")
		grandchild.Stdout = os.Stdout
		if err := grandchild.Start(); err != nil {
			return 1
		}
		pids := fmt.Sprintf("%d %d", os.Getpid(), grandchild.Process.Pid)
		pidFile := os.Getenv("SEL_TEST_PIDFILE")
		_ = os.WriteFile(pidFile+".tmp", []byte(pids), 0o600)
		_ = os.Rename(pidFile+".tmp", pidFile)
		time.Sleep(30 * time.Second)
	case "hangpg":
		grandchild := exec.Command(os.Args[0], "helper", "sleep")
		grandchild.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := grandchild.Start(); err != nil {
			return 1
		}
		terminated := make(chan os.Signal, 1)
		signal.Notify(terminated, syscall.SIGTERM)
		pidFile := os.Getenv("SEL_TEST_PIDFILE")
		pids := fmt.Sprintf("%d %d", os.Getpid(), grandchild.Process.Pid)
		_ = os.WriteFile(pidFile+".tmp", []byte(pids), 0o600)
		_ = os.Rename(pidFile+".tmp", pidFile)
		<-terminated
		_ = syscall.Kill(-grandchild.Process.Pid, syscall.SIGKILL)
	case "holdout":
		grandchild := exec.Command(os.Args[0], "helper", "lateline", "--ms="+strconv.Itoa(helperFlag(args, "ms")))
		grandchild.Stdout = os.Stdout
		if err := grandchild.Start(); err != nil {
			return 1
		}
		fmt.Println("early")
	case "lateline":
		time.Sleep(time.Duration(helperFlag(args, "ms")) * time.Millisecond)
		fmt.Println("late")
	case "holdpipe":
		grandchild := exec.Command(os.Args[0], "helper", "sleep")
		grandchild.Stdout = os.Stdout
		if err := grandchild.Start(); err != nil {
			return 1
		}
		pidFile := os.Getenv("SEL_TEST_PIDFILE")
		_ = os.WriteFile(pidFile+".tmp", []byte(strconv.Itoa(grandchild.Process.Pid)), 0o600)
		_ = os.Rename(pidFile+".tmp", pidFile)
		fmt.Println("early")
		return helperFlag(args, "code")
	case "touch":
		_ = os.WriteFile(os.Getenv("SEL_TEST_TOUCH"), nil, 0o600)
	case "sleep":
		time.Sleep(30 * time.Second)
	}
	return 0
}

func helperRunner() *Runner {
	command := func(path string, flags ...schema.Flag) schema.Command {
		return schema.Command{Path: "helper " + path, Runnable: true, Flags: flags}
	}
	return NewRunner(schema.Document{Commands: []schema.Command{
		command("stdin"),
		command("env"),
		command("hang"),
		command("hangpg"),
		command("holdpipe", schema.Flag{Name: "code", Type: "int"}),
		command("touch"),
		command("exit", schema.Flag{Name: "code", Type: "int"}),
		command("flood", schema.Flag{Name: "lines", Type: "int"}, schema.Flag{Name: "width", Type: "int"}),
	}}, os.Args[0])
}

func runHelper(t *testing.T, tool, args string, timeoutSeconds int) (Result, time.Duration) {
	t.Helper()
	var arguments map[string]any
	if err := json.Unmarshal([]byte(args), &arguments); err != nil {
		t.Fatal(err)
	}
	arguments[timeoutProperty] = timeoutSeconds
	raw, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	timeout := time.Duration(timeoutSeconds) * time.Second
	type outcome struct {
		res Result
		err error
	}
	done := make(chan outcome, 1)
	started := time.Now()
	go func() {
		res, err := helperRunner().Run(context.Background(), tool, raw)
		done <- outcome{res, err}
	}()
	select {
	case o := <-done:
		if o.err != nil {
			t.Fatal(o.err)
		}
		return o.res, time.Since(started)
	case <-time.After(timeout + 3*time.Second):
		t.Fatalf("%s did not return within 3s of its %v deadline", tool, timeout)
		return Result{}, 0
	}
}

func TestChildReadsEndOfFileFromStdinEvenWhenTheParentHasInputWaiting(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdin
	os.Stdin = reader
	t.Cleanup(func() {
		os.Stdin = original
		_ = reader.Close()
		_ = writer.Close()
	})
	if _, err := writer.WriteString("parent input\n"); err != nil {
		t.Fatal(err)
	}

	res, _ := runHelper(t, "helper_stdin", `{}`, 5)

	if got := string(res.Stdout); got != "read:0\n" || res.TimedOut {
		t.Errorf("stdout = %q timedOut = %v, want %q: the child must see /dev/null", got, res.TimedOut, "read:0\n")
	}
}

func TestChildInheritsTheParentEnvironment(t *testing.T) {
	t.Setenv("SEL_TEST_MARKER", "marker-7f3a")

	res, _ := runHelper(t, "helper_env", `{}`, 5)

	if got := string(res.Stdout); got != "marker-7f3a\n" {
		t.Errorf("stdout = %q, want the marker the parent set", got)
	}
}

func TestExitCodeComesBackRawAndAChildThatFinishesIsNotTimedOut(t *testing.T) {
	res, _ := runHelper(t, "helper_exit", `{"code":3}`, 5)

	if res.ExitCode != 3 || res.TimedOut || res.Truncated {
		t.Errorf("result = %+v, want exit 3, not timed out, not truncated", res)
	}
}

func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		_, procErr := os.Stat("/proc/self/stat")
		return procErr != nil
	}
	return !bytes.Contains(stat, []byte(") Z "))
}

func assertDeadlineLeavesNoSurvivors(t *testing.T, tool string) {
	t.Helper()
	pidFile := t.TempDir() + "/pids"
	t.Setenv("SEL_TEST_PIDFILE", pidFile)
	t.Cleanup(func() {
		raw, _ := os.ReadFile(pidFile)
		for _, field := range strings.Fields(string(raw)) {
			pid, _ := strconv.Atoi(field)
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	var res Result
	var elapsed time.Duration
	for attempt := 0; attempt < 3; attempt++ {
		res, elapsed = runHelper(t, tool, `{}`, 2)
		if _, err := os.Stat(pidFile); err == nil {
			break
		}
	}

	if !res.TimedOut {
		t.Errorf("result = %+v, want timed out", res)
	}
	if elapsed > 5*time.Second {
		t.Errorf("returned after %v, want shortly after the 2s timeout_s", elapsed)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the helper never reported its pids: %v", err)
	}
	pids := strings.Fields(string(raw))
	if len(pids) != 2 {
		t.Fatalf("pid file = %q, want child and grandchild", raw)
	}
	for _, field := range pids {
		pid, _ := strconv.Atoi(field)
		deadline := time.Now().Add(2 * time.Second)
		for alive(pid) && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if alive(pid) {
			t.Errorf("process %d is still running after the deadline", pid)
		}
	}
}

func TestChildThatNeverExitsIsKilledAtItsDeadlineWithItsGrandchild(t *testing.T) {
	assertDeadlineLeavesNoSurvivors(t, "helper_hang")
}

func TestDeadlineReachesADescendantThatStartedItsOwnProcessGroup(t *testing.T) {
	assertDeadlineLeavesNoSurvivors(t, "helper_hangpg")
}

func TestCancelledContextReturnsItsErrorAndStartsNoProcess(t *testing.T) {
	touched := t.TempDir() + "/touched"
	t.Setenv("SEL_TEST_TOUCH", touched)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := helperRunner().Run(ctx, "helper_touch", nil)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if _, statErr := os.Stat(touched); statErr == nil {
		t.Error("the child ran")
	}
}

func TestOutputUnderTheCapPassesThroughByteForByte(t *testing.T) {
	res, _ := runHelper(t, "helper_flood", `{"lines":100,"width":50}`, 10)

	if want := floodBody(100, 50); string(res.Stdout) != want || res.Truncated {
		t.Errorf("stdout is %d bytes, truncated = %v, want the %d-byte body unchanged", len(res.Stdout), res.Truncated, len(want))
	}
}

func TestOutputOverOneMegabyteIsCappedAndKeepsTheTerminalEvent(t *testing.T) {
	const lines, width = 8000, 100
	body := floodBody(lines, width)
	if len(body) < 1<<20 {
		t.Fatalf("body is %d bytes, want over 1 MB", len(body))
	}
	terminal := floodTerminal(lines)

	res, _ := runHelper(t, "helper_flood", fmt.Sprintf(`{"lines":%d,"width":%d}`, lines, width), 10)

	out := string(res.Stdout)
	if !res.Truncated {
		t.Error("result is not flagged truncated")
	}
	if len(out) > stdoutCap+len(terminal) {
		t.Errorf("stdout is %d bytes, want at most %d", len(out), stdoutCap+len(terminal))
	}
	if !strings.HasSuffix(out, terminal) {
		t.Errorf("stdout ends %q, want the terminal event %q", out[max(0, len(out)-80):], terminal)
	}
	head := strings.TrimSuffix(out, terminal)
	if !strings.HasPrefix(body, head) || len(head) < stdoutCap-len(floodLine(0, width)) {
		t.Errorf("the kept head is %d bytes, want whole lines from the start of the output filling the cap", len(head))
	}
}

func capture(limit, chunk int, cut bool, input string) (string, bool) {
	c := &capped{limit: limit}
	for rest := []byte(input); len(rest) > 0; {
		n := min(chunk, len(rest))
		_, _ = c.Write(rest[:n])
		rest = rest[n:]
	}
	out, truncated := c.result(cut)
	return string(out), truncated
}

func TestCappedKeepsWholeLinesThenTheLastLineHoweverTheBytesArrive(t *testing.T) {
	cases := map[string]struct {
		input     string
		want      string
		truncated bool
	}{
		"under the cap is untouched":                   {"a\nb\nc\n", "a\nb\nc\n", false},
		"exactly the cap is untouched":                 {"aaaa\nbbbb\n", "aaaa\nbbbb\n", false},
		"cuts at a line boundary and appends the last": {"aaaa\nbbbb\ncccc\nterm\n", "aaaa\nbbbb\nterm\n", true},
		"the last line straddles the cap":              {"aaaa\nbbbb\ncc\nterminal\n", "aaaa\nbbbb\nterminal\n", true},
		"the last line has no newline":                 {"aaaa\nbbbb\ncccc\nterm", "aaaa\nbbbb\nterm", true},
		"blank lines after the last are ignored":       {"aaaa\nbbbb\ncccc\nterm\n\n\n", "aaaa\nbbbb\nterm\n", true},
		"an oversized last line is dropped":            {"aaaa\nbbbb\ncccc\nterm" + strings.Repeat("x", 30) + "\n", "aaaa\nbbbb\n", true},
		"a single oversized line is dropped whole":     {strings.Repeat("x", 30) + "\n", "", true},
		"an oversized first line, then a small last":   {strings.Repeat("x", 30) + "\nterm\n", "term\n", true},
		"every line oversized":                         {strings.Repeat("x", 30) + "\n" + strings.Repeat("y", 30) + "\n", "", true},
	}
	for name, c := range cases {
		for _, chunk := range []int{1, 3, 7, 1000} {
			got, truncated := capture(10, chunk, false, c.input)
			if got != c.want || truncated != c.truncated {
				t.Errorf("%s (chunks of %d): got %q truncated=%v, want %q truncated=%v", name, chunk, got, truncated, c.want, c.truncated)
			}
		}
	}
}

func TestCappedCutKeepsOnlyWholeLinesAndIsAlwaysFlagged(t *testing.T) {
	cases := map[string]struct {
		limit int
		input string
		want  string
	}{
		"a half line at the end is dropped":                 {100, "aaaa\nbb", "aaaa\n"},
		"complete lines stay, still flagged":                {100, "aaaa\nbbbb\n", "aaaa\nbbbb\n"},
		"no complete line at all":                           {100, "aaaa", ""},
		"over the cap, the half line after the last is out": {10, "aaaa\nbbbb\ncccc\nterm\nha", "aaaa\nbbbb\nterm\n"},
	}
	for name, c := range cases {
		for _, chunk := range []int{1, 3, 1000} {
			got, truncated := capture(c.limit, chunk, true, c.input)
			if got != c.want || !truncated {
				t.Errorf("%s (chunks of %d): got %q truncated=%v, want %q truncated=true", name, chunk, got, truncated, c.want)
			}
		}
	}
}

func TestFirstEventOverTheCapLeavesTheTerminalEventOnItsOwnLine(t *testing.T) {
	res, _ := runHelper(t, "helper_flood", fmt.Sprintf(`{"lines":1,"width":%d}`, stdoutCap+1000), 10)

	if want := floodTerminal(1); string(res.Stdout) != want || !res.Truncated {
		t.Errorf("stdout is %d bytes ending %q, truncated = %v, want exactly %q", len(res.Stdout), res.Stdout[max(0, len(res.Stdout)-60):], res.Truncated, want)
	}
}

func TestTerminateGraceOutlastsTheNestedRunnersWorstCaseShutdownByASecond(t *testing.T) {
	if productionTerminateGrace < localrun.ShutdownBudget+time.Second {
		t.Errorf("grace = %v, want at least %v plus 1s", productionTerminateGrace, localrun.ShutdownBudget)
	}
}

func TestChildThatExitedZeroIsNotTimedOutWhenADescendantHoldsStdoutAcrossTheDeadline(t *testing.T) {
	argv := []string{os.Args[0], "helper", "holdout", "--ms=1600"}

	res, err := execute(context.Background(), argv, time.Second)

	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || res.TimedOut || res.Truncated || string(res.Stdout) != "early\nlate\n" {
		t.Errorf("result = %+v stdout %q, want exit 0, not timed out, not truncated, both lines", res, res.Stdout)
	}
}

func TestDescendantHoldingStdoutPastTheDrainWindowFlagsTruncationAndIsKilled(t *testing.T) {
	for _, code := range []int{0, 1} {
		t.Run(fmt.Sprintf("exit %d", code), func(t *testing.T) {
			pidFile := t.TempDir() + "/pid"
			t.Setenv("SEL_TEST_PIDFILE", pidFile)
			t.Cleanup(func() {
				raw, _ := os.ReadFile(pidFile)
				pid, _ := strconv.Atoi(string(raw))
				if pid > 0 {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			})

			res, elapsed := runHelper(t, "helper_holdpipe", fmt.Sprintf(`{"code":%d}`, code), 10)

			if res.ExitCode != code || !res.Truncated || res.TimedOut || string(res.Stdout) != "early\n" {
				t.Errorf("result = %+v stdout %q, want exit %d, truncated, not timed out, the whole early line", res, res.Stdout, code)
			}
			if elapsed > pipeDrainWindow+time.Second {
				t.Errorf("returned after %v, want within the %v drain window of the child's exit", elapsed, pipeDrainWindow)
			}
			raw, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatal(err)
			}
			pid, _ := strconv.Atoi(string(raw))
			settled := time.Now().Add(2 * time.Second)
			for alive(pid) && time.Now().Before(settled) {
				time.Sleep(10 * time.Millisecond)
			}
			if alive(pid) {
				t.Errorf("the descendant holding stdout (pid %d) is still running", pid)
			}
		})
	}
}
