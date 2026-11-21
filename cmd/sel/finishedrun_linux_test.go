package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

type finishingRun struct {
	*hangupRun
	gate     string
	finished string
}

func startFinishingRun(t *testing.T, splitStderr, fail, linger bool, args ...string) *finishingRun {
	t.Helper()
	dir := t.TempDir()
	gate, finished := filepath.Join(dir, "gate"), filepath.Join(dir, "finished")
	t.Setenv("FAKE_FINISH_GATE", gate)
	t.Setenv("FAKE_FINISHED_FILE", finished)
	if fail {
		t.Setenv("FAKE_FAIL", "tests/test_calc.py::test_mul")
	}
	if linger {
		t.Setenv("FAKE_LINGER", "1")
	}
	h := startRun(t, newFakeAPI(t, everything), false, false, splitStderr, append([]string{"--timeout=50s"}, args...)...)
	return &finishingRun{hangupRun: h, gate: gate, finished: finished}
}

func (f *finishingRun) awaitCount(event string, count int) {
	f.t.Helper()
	waitUntil(f.t, event+" events", func() bool {
		return strings.Count(f.out.String(), `"event":"`+event+`"`) >= count
	})
}

func (f *finishingRun) releaseTheFinish() {
	f.t.Helper()
	if err := os.WriteFile(f.gate, nil, 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *finishingRun) finishes() int {
	body, _ := os.ReadFile(f.finished)
	return len(body)
}

func (f *finishingRun) leaveBeforeTheFinish(progress, finishes int) {
	f.t.Helper()
	f.awaitCount("progress", progress)
	_ = f.reader.Close()
	f.releaseTheFinish()
	waitUntil(f.t, "the runner to report its finish after the reader left", func() bool { return f.finishes() >= finishes })
}

func TestAReaderLeavingAtTheSummaryOfAFinishedRunKeepsTheRealExitCode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		split  bool
		fail   bool
		linger bool
		want   int
	}{
		{"failing, own pipe", true, true, false, 1},
		{"passing, own pipe", true, false, false, 0},
		{"failing, shared pipe", false, true, false, 1},
		{"passing, shared pipe", false, false, false, 0},
		{"failing, runner still alive", true, true, true, 1},
		{"passing, runner still alive", true, false, true, 0},
		{"failing, shared pipe, runner still alive", false, true, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := startFinishingRun(t, tc.split, tc.fail, tc.linger)
			runner := f.runnerPid()

			f.leaveBeforeTheFinish(3, 1)
			f.await()

			f.wantWoundDown(runner)
			f.wantExit(tc.want)
			if combined := f.out.String() + f.errOut.String(); strings.Contains(combined, `"event":"summary"`) || strings.Contains(combined, "Interrupted") {
				t.Errorf("output %q; want no summary, which the reader left before, and no interrupt notice", combined)
			}
		})
	}
}

func TestAReaderLeavingAfterTheSummaryOfAFinishedRunWhoseRunnerStaysAliveKeepsTheRealExitCode(t *testing.T) {
	f := startFinishingRun(t, true, false, true)
	runner := f.runnerPid()
	f.releaseTheFinish()
	waitUntil(t, "the summary of a finished run with its runner alive", func() bool {
		return f.finishes() >= 1 && strings.Contains(f.out.String(), `"event":"summary"`)
	})

	_ = f.reader.Close()
	f.await()

	f.wantWoundDown(runner)
	f.wantExit(0)
	if strings.Contains(f.errOut.String(), "error") {
		t.Errorf("stderr %q; want no error, the run had finished", f.errOut.String())
	}
}

func TestASignalAfterTheRunFinishedStillExitsInterrupted(t *testing.T) {
	for _, tc := range []struct {
		name string
		send func(*finishingRun)
	}{
		{"Ctrl-C", func(f *finishingRun) { _ = syscall.Kill(f.cmd.Process.Pid, syscall.SIGINT) }},
		{"SIGHUP", func(f *finishingRun) { f.hangUp() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := startFinishingRun(t, true, true, true)
			f.releaseTheFinish()
			runner := f.runnerPid()
			waitUntil(t, "the run to be finished with its runner alive", func() bool {
				return f.finishes() >= 1 && strings.Contains(f.out.String(), `"event":"summary"`)
			})
			if err := syscall.Kill(runner, 0); err != nil {
				t.Fatalf("VOID: the runner was gone before the signal: %v", err)
			}

			tc.send(f)
			f.await()

			f.wantWoundDown(runner)
			f.wantExit(130)
		})
	}
}

func TestAReaderLeavingRightAfterAnIterationUnderWatchStillExitsInterrupted(t *testing.T) {
	for _, tc := range []struct {
		name  string
		later bool
	}{
		{"the first iteration", false},
		{"a later iteration", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := startFinishingRun(t, true, true, false, "--watch")
			progress, finishes := 3, 1
			if tc.later {
				f.awaitCount("progress", 3)
				f.releaseTheFinish()
				f.awaitCount("iteration_end", 1)
				if err := os.WriteFile(filepath.Join(f.cmd.Dir, "src/calc.py"), []byte("v3\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				progress, finishes = 6, 2
			}
			f.awaitCount("progress", progress)
			runner := f.runnerPid()

			f.leaveBeforeTheFinish(progress, finishes)
			f.await()

			f.wantWoundDown(runner)
			f.wantExit(130)
		})
	}
}
