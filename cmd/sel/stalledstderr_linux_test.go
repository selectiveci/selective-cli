package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func startPipedRun(t *testing.T, stdout, stderr io.Writer) (cmd *exec.Cmd, runner int, began time.Time) {
	t.Helper()
	runnerEnv(t)
	t.Setenv("FAKE_HANG", "1")
	pidFile := filepath.Join(t.TempDir(), "runner.pid")
	t.Setenv("FAKE_PID_FILE", pidFile)
	cmd = exec.Command(os.Args[0], "test", "--org", "acme", "--timeout="+raceTimeout.String(), "--json")
	cmd.Dir = pytestProject(t)
	cmd.Env = append(os.Environ(), "SELECTIVE_TEST_RUN_MAIN=1", "SELECTIVE_NO_UPDATE_NOTIFIER=1", "SELECTIVE_HOST="+newFakeAPI(t, selected).srv.URL, "SELECTIVE_TOKEN=tok", "SELECTIVE_CONFIG_DIR="+t.TempDir())
	cmd.Stdout, cmd.Stderr = stdout, stderr
	began = time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	waitUntil(t, "the runner to start", func() bool {
		body, _ := os.ReadFile(pidFile)
		runner, _ = strconv.Atoi(strings.TrimSpace(string(body)))
		return runner > 0
	})
	t.Cleanup(func() { _ = syscall.Kill(-runner, syscall.SIGKILL) })
	if time.Until(began.Add(raceTimeout)) < time.Second {
		t.Fatal("VOID: the run was too late to leave a margin before the deadline")
	}
	return cmd, runner, began
}

func signalAndWait(t *testing.T, cmd *exec.Cmd, runner int, sig syscall.Signal) (exit int) {
	t.Helper()
	if err := syscall.Kill(cmd.Process.Pid, sig); err != nil {
		t.Fatal(err)
	}
	exit, _ = waitForExit(t, cmd, 30*time.Second)
	if !processGone(-runner) {
		t.Errorf("the runner group %d is still alive after sel exited", runner)
	}
	return exit
}

func ctrlCAndWait(t *testing.T, cmd *exec.Cmd, runner int) (exit int) {
	t.Helper()
	return signalAndWait(t, cmd, runner, syscall.SIGINT)
}

func wantSummaryLast(t *testing.T, out string) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, `"event":"summary"`) || !strings.Contains(last, `"interrupted":true`) || strings.Count(out, `"event":"summary"`) != 1 || strings.Contains(out, `"error":"timeout"`) {
		t.Errorf("output %q; want one interrupted summary as the last line and no timeout object", out)
	}
}

func TestAStalledStderrStillLeavesTheInterruptedSummaryLast(t *testing.T) {
	w := fullPipe(t)
	var out bytes.Buffer
	cmd, runner, _ := startPipedRun(t, &out, w)
	_ = w.Close()

	if exit := ctrlCAndWait(t, cmd, runner); exit != 130 {
		t.Errorf("exit %d; want 130", exit)
	}
	wantSummaryLast(t, out.String())
}

func TestCtrlCOnOneReadPipeKeepsTheNoticeBeforeTheSummary(t *testing.T) {
	var out bytes.Buffer
	cmd, runner, _ := startPipedRun(t, &out, &out)

	if exit := ctrlCAndWait(t, cmd, runner); exit != 130 {
		t.Errorf("exit %d; want 130", exit)
	}
	got := out.String()
	wantSummaryLast(t, got)
	if strings.Count(got, "Interrupted") != 1 || strings.Index(got, "Interrupted") > strings.Index(got, `"event":"summary"`) {
		t.Errorf("output %q; want one Interrupted line before the summary", got)
	}
}

func TestCtrlCOnOneStalledPipeStillExitsInterruptedWithinTheBound(t *testing.T) {
	w := fullPipe(t)
	cmd, runner, began := startPipedRun(t, w, w)
	_ = w.Close()

	exit := ctrlCAndWait(t, cmd, runner)
	if took := time.Since(began); exit != 130 || took > raceBound+execSlack {
		t.Errorf("exit %d after %s; want 130 within %s", exit, took, raceBound+execSlack)
	}
}

func TestABackstopAfterACtrlCWritesNoTimeoutObject(t *testing.T) {
	var stdout, stderr lockedBuffer
	c, _, exited := expiredClock(&stdout, &stderr, true)
	c.mu.Lock()
	c.ctrlC = true
	c.mu.Unlock()
	expire(c)

	code, _ := awaitExit(t, exited)

	if code != 130 || stdout.String() != "" || strings.Count(stderr.String(), "Interrupted") != 1 {
		t.Errorf("exit %d, stdout %q, stderr %q; want 130, no stdout and one Interrupted", code, stdout.String(), stderr.String())
	}
}

func TestATermOrHangupOnAStalledStderrKeepsTheTimeoutExit(t *testing.T) {
	for name, sig := range map[string]syscall.Signal{"term": syscall.SIGTERM, "hup": syscall.SIGHUP} {
		t.Run(name, func(t *testing.T) {
			w := fullPipe(t)
			var out bytes.Buffer
			cmd, runner, _ := startPipedRun(t, &out, w)
			_ = w.Close()

			exit := signalAndWait(t, cmd, runner, sig)
			got := out.String()
			if exit != 8 || strings.Count(got, `"error":"timeout"`) != 1 || strings.Contains(got, `"event":"summary"`) {
				t.Errorf("exit %d, output %q; want 8, one timeout object and no summary", exit, got)
			}
		})
	}
}

func TestACtrlCAtTheDeadlineWritesTheSummaryBeforeTheBackstop(t *testing.T) {
	_, w := filledPipe(t)
	var stdout lockedBuffer
	c, g, exited := expiredClock(&stdout, w, true)
	c.mu.Lock()
	c.ctrlC = true
	c.deadline = time.Now().Add(-20 * time.Millisecond)
	c.timer.Reset(c.untilFire())
	c.mu.Unlock()
	line := "{\"event\":\"summary\",\"interrupted\":true}\n"

	wrote := make(chan struct{})
	go func() {
		announceInterrupt(context.WithValue(context.Background(), backstopKey{}, c), w)
		_, _ = g.Write([]byte(line))
		close(wrote)
	}()
	select {
	case <-wrote:
	case <-time.After(2 * time.Second):
		t.Fatalf("the summary was never written; stdout %q", stdout.String())
	}

	code, _ := awaitExit(t, exited)
	if code != 130 || stdout.String() != line {
		t.Errorf("exit %d, stdout %q; want 130 and only the summary", code, stdout.String())
	}
}

func TestTheInterruptNoticeIsWrittenOnceAcrossBothPaths(t *testing.T) {
	var stdout, stderr lockedBuffer
	c, _, exited := expiredClock(&stdout, &stderr, true)
	c.mu.Lock()
	c.ctrlC = true
	c.mu.Unlock()

	announceInterrupt(context.WithValue(context.Background(), backstopKey{}, c), &stderr)
	expire(c)
	code, _ := awaitExit(t, exited)

	if code != 130 || strings.Count(stderr.String(), "Interrupted") != 1 {
		t.Errorf("exit %d, stderr %q; want 130 and one Interrupted", code, stderr.String())
	}
}

func filledPipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = r.Close()
		_ = w.Close()
	})
	for chunk := make([]byte, 4096); ; {
		_ = w.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
		if _, err := w.Write(chunk); err != nil {
			_ = w.SetWriteDeadline(time.Time{})
			return r, w
		}
	}
}

func TestTheInterruptNoticeWaitsForStderrOnlyWhenItSharesStdout(t *testing.T) {
	r, w := filledPipe(t)
	other, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	notice := func(stdout *os.File) chan struct{} {
		ctx := context.WithValue(context.Background(), backstopKey{}, &clock{stdout: stdout, stderr: w, ctrlC: true})
		done := make(chan struct{})
		go func() {
			announceInterrupt(ctx, w)
			close(done)
		}()
		return done
	}

	select {
	case <-notice(other):
	case <-time.After(2 * backstopExit):
		t.Error("the notice blocked on a stalled stderr that is not stdout")
	}
	shared := notice(w)
	select {
	case <-shared:
		t.Error("the notice moved on while stdout and stderr share a stalled pipe")
	case <-time.After(2 * backstopExit):
	}
	go func() { _, _ = io.Copy(io.Discard, r) }()
	select {
	case <-shared:
	case <-time.After(5 * time.Second):
		t.Error("the notice never finished after the pipe drained")
	}
}
