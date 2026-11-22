package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/localrun"
)

const (
	raceTimeout = 3 * time.Second
	raceBound   = raceTimeout + localrun.ShutdownBudget + backstopGrace + backstopExit
	execSlack   = 500 * time.Millisecond
)

type raceRun struct {
	*hangupRun
	began    time.Time
	seen     time.Time
	runner   int
	sigintAt string
	gate     string
}

func startRaceRun(t *testing.T, gated bool) *raceRun {
	t.Helper()
	dir := t.TempDir()
	r := &raceRun{sigintAt: filepath.Join(dir, "sigint"), gate: filepath.Join(dir, "gate")}
	t.Setenv("FAKE_HANG", "1")
	t.Setenv("FAKE_SIGINT_AT_FILE", r.sigintAt)
	if gated {
		t.Setenv("FAKE_STOP_GATE", r.gate)
	}
	r.began = time.Now()
	r.hangupRun = startRun(t, newFakeAPI(t, selected), false, false, true, "--timeout="+raceTimeout.String(), "--json")
	r.runner = r.runnerPid()
	r.seen = time.Now()
	return r
}

func (r *raceRun) ctrlC() time.Time {
	r.t.Helper()
	sent := time.Now()
	if err := syscall.Kill(r.cmd.Process.Pid, syscall.SIGINT); err != nil {
		r.t.Fatal(err)
	}
	return sent
}

func (r *raceRun) runnerGotInterruptAt() time.Time {
	r.t.Helper()
	var at int64
	waitUntil(r.t, "the runner to receive an interrupt from sel", func() bool {
		body, _ := os.ReadFile(r.sigintAt)
		at, _ = strconv.ParseInt(string(body), 10, 64)
		return at > 0
	})
	return time.Unix(0, at)
}

func (r *raceRun) releaseAfterTheDeadline() {
	r.t.Helper()
	waitUntil(r.t, "the deadline to pass", func() bool { return time.Now().After(r.seen.Add(raceTimeout)) })
	if err := os.WriteFile(r.gate, nil, 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *raceRun) wantExitWithinTheBound(code int) {
	r.t.Helper()
	took := time.Since(r.began)
	r.wantExit(code)
	if took < raceTimeout || took > raceBound+execSlack {
		r.t.Errorf("exit after %s; want it after the deadline %s and within %s", took, raceTimeout, raceBound+execSlack)
	}
}

func TestCtrlCBeforeTheDeadlineExitsInterruptedWhenTheWindDownCrossesIt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		gated bool
	}{{"the runner stops once released", true}, {"the runner ignores it and is reaped", false}} {
		t.Run(tc.name, func(t *testing.T) {
			r := startRaceRun(t, tc.gated)
			if time.Until(r.began.Add(raceTimeout)) < time.Second {
				t.Fatal("VOID: the run was too late to leave a margin before the deadline")
			}

			r.ctrlC()
			if got := r.runnerGotInterruptAt(); !got.Before(r.began.Add(raceTimeout)) {
				t.Fatalf("VOID: the runner was interrupted at %s, not before the deadline at %s", got, r.began.Add(raceTimeout))
			}
			if tc.gated {
				r.releaseAfterTheDeadline()
			}
			r.await()

			r.wantWoundDown(r.runner)
			r.wantInterruptedSummary()
			r.wantExitWithinTheBound(130)
		})
	}
}

func TestCtrlCAfterTheDeadlineFiredStillExitsWithTheTimeout(t *testing.T) {
	r := startRaceRun(t, true)
	interruptedByDeadline := r.runnerGotInterruptAt()
	if interruptedByDeadline.Before(r.began.Add(raceTimeout)) {
		t.Fatalf("VOID: the runner was interrupted at %s, before the deadline at %s", interruptedByDeadline, r.began.Add(raceTimeout))
	}

	if sent := r.ctrlC(); !sent.After(interruptedByDeadline) {
		t.Fatal("VOID: Ctrl-C was sent before the deadline's interrupt reached the runner")
	}
	r.releaseAfterTheDeadline()
	r.await()

	r.wantWoundDown(r.runner)
	r.wantExitWithinTheBound(8)
	if !strings.Contains(r.out.String(), `"error":"timeout"`) {
		t.Errorf("stdout %q; want the timeout error object", r.out.String())
	}
}

func TestAHungRunWithNoCtrlCStillExitsWithTheTimeoutWithinTheBound(t *testing.T) {
	t.Setenv("FAKE_HANG", "1")
	r := &raceRun{began: time.Now()}
	r.hangupRun = startRun(t, newFakeAPI(t, selected), false, false, true, "--timeout="+raceTimeout.String(), "--json")
	r.runner = r.runnerPid()
	r.await()

	r.wantWoundDown(r.runner)
	r.wantExitWithinTheBound(8)
}

func stalledStdoutRun(t *testing.T, sig syscall.Signal, afterDeadline bool) (exit int, stderr string) {
	t.Helper()
	runnerEnv(t)
	t.Setenv("FAKE_HANG", "1")
	t.Setenv("FAKE_IGNORE_SIGINT", "1")
	pidFile := filepath.Join(t.TempDir(), "runner.pid")
	t.Setenv("FAKE_PID_FILE", pidFile)
	w := fullPipe(t)
	began := time.Now()
	cmd, errOut := startWithStdout(t, pytestProject(t), newFakeAPI(t, selected).srv.URL, nil, w, false, "test", "--org", "acme", "--timeout="+raceTimeout.String(), "--json")
	_ = w.Close()
	var runner int
	waitUntil(t, "the runner to start", func() bool {
		body, _ := os.ReadFile(pidFile)
		runner, _ = strconv.Atoi(strings.TrimSpace(string(body)))
		return runner > 0
	})
	t.Cleanup(func() { _ = syscall.Kill(-runner, syscall.SIGKILL) })
	seen := time.Now()
	if afterDeadline {
		waitUntil(t, "the deadline to pass", func() bool { return time.Now().After(seen.Add(raceTimeout)) })
	} else if time.Until(began.Add(raceTimeout)) < time.Second {
		t.Fatal("VOID: the run was too late to leave a margin before the deadline")
	}
	if sig != 0 {
		if err := syscall.Kill(cmd.Process.Pid, sig); err != nil {
			t.Fatal(err)
		}
	}
	exit, _ = waitForExit(t, cmd, 30*time.Second)
	took := time.Since(began)
	t.Logf("overshoot of the bound: %s", took-raceBound)
	if took < raceTimeout+localrun.ShutdownBudget || took > raceBound+execSlack {
		t.Fatalf("VOID: sel exited after %s; want the backstop's exit between %s and %s", took, raceTimeout+localrun.ShutdownBudget, raceBound+execSlack)
	}
	if !processGone(-runner) {
		t.Errorf("the runner group %d is still alive after sel exited", runner)
	}
	return exit, errOut.String()
}

func TestACtrlCCountsOnlyBeforeTheDeadline(t *testing.T) {
	for offset, want := range map[time.Duration]bool{time.Hour: true, -time.Hour: false} {
		c := &clock{deadline: time.Now().Add(offset)}
		if noteCtrlC(context.WithValue(context.Background(), backstopKey{}, c)); c.ctrlC != want {
			t.Errorf("deadline %s away: ctrlC = %v, want %v", offset, c.ctrlC, want)
		}
	}
}

func TestAStalledRunsBackstopExitCode(t *testing.T) {
	for _, tc := range []struct {
		name          string
		sig           syscall.Signal
		afterDeadline bool
		want          int
		notice        string
		absent        string
	}{
		{"Ctrl-C before the deadline", syscall.SIGINT, false, 130, "Interrupted", "expired"},
		{"no signal", 0, false, 8, "Error: --timeout", ""},
		{"Ctrl-C after the deadline", syscall.SIGINT, true, 8, "Error: --timeout", "Interrupted"},
		{"SIGTERM before the deadline", syscall.SIGTERM, false, 8, "Error: --timeout", ""},
		{"SIGHUP before the deadline", syscall.SIGHUP, false, 8, "Error: --timeout", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exit, stderr := stalledStdoutRun(t, tc.sig, tc.afterDeadline)

			if exit != tc.want || !strings.Contains(stderr, tc.notice) || tc.absent != "" && strings.Contains(stderr, tc.absent) {
				t.Errorf("exit %d, stderr %q; want exit %d with %q and without %q", exit, stderr, tc.want, tc.notice, tc.absent)
			}
			if tc.want == 130 && strings.Count(stderr, "Interrupted") != 1 {
				t.Errorf("stderr %q; want exactly one Interrupted line", stderr)
			}
		})
	}
}
