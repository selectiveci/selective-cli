package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func startTricklingRun(t *testing.T, splitStderr, hang bool, args ...string) *hangupRun {
	t.Helper()
	t.Setenv("FAKE_TRICKLE", "500ms")
	if hang {
		t.Setenv("FAKE_TRICKLE_HANG", "1")
	}
	return startRun(t, newFakeAPI(t, everything), false, false, splitStderr, append([]string{"--timeout=50s"}, args...)...)
}

func (h *hangupRun) wantExit(code int) {
	h.t.Helper()
	if got := h.cmd.ProcessState.ExitCode(); got != code || strings.Contains(h.out.String()+h.errOut.String(), "could not write the event stream") {
		h.t.Errorf("sel: %s, stderr %q; want exit %d and no event stream write error", h.cmd.ProcessState, h.errOut.String(), code)
	}
}

func TestReaderLeavingInterruptsTheRunAndLeavesNothingBehind(t *testing.T) {
	for _, tc := range []struct {
		name  string
		split bool
		env   string
		args  []string
	}{
		{"stdout on its own pipe", true, "", nil},
		{"stdout and stderr on one pipe", false, "", nil},
		{"a runner that ignores interrupt", true, "FAKE_IGNORE_SIGINT", nil},
		{"a watch", true, "", []string{"--watch"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env != "" {
				t.Setenv(tc.env, "1")
			}
			h := startTricklingRun(t, tc.split, true, tc.args...)
			runner := h.runnerPid()

			_ = h.reader.Close()
			h.await()

			h.wantWoundDown(runner)
			h.wantExit(130)
		})
	}
}

func TestReaderLeavingDuringAListEndsInterrupted(t *testing.T) {
	api := newFakeAPI(t, everything)
	api.delay = 300 * time.Millisecond
	h := startRun(t, api, false, false, true, "--list")
	waitUntil(t, "the build_start event", func() bool { return strings.Contains(h.out.String(), `"event":"build_start"`) })
	body, _ := os.ReadFile(h.pidFile)
	runner, _ := strconv.Atoi(strings.TrimSpace(string(body)))
	if runner <= 0 {
		t.Fatal("VOID: the runner never started")
	}
	t.Cleanup(func() { _ = syscall.Kill(-runner, syscall.SIGKILL) })

	_ = h.reader.Close()
	h.await()

	h.wantWoundDown(runner)
	h.wantExit(130)
}

func TestStderrReaderLeavingDoesNotInterruptTheRun(t *testing.T) {
	h := startTricklingRun(t, true, false)
	runner := h.runnerPid()

	_ = h.errRead.Close()
	h.await()

	h.wantWoundDown(runner)
	h.wantExit(0)
	if !strings.Contains(h.out.String(), `"event":"summary"`) {
		t.Errorf("stdout %q; want the run to finish with its summary", h.out.String())
	}
}

func TestDeadlineStillWinsAfterTheReaderLeft(t *testing.T) {
	t.Setenv("FAKE_HANG", "1")
	h := startHangupRun(t, nil, false, false, "--timeout=2s")
	runner := h.runnerPid()

	_ = h.reader.Close()
	h.await()

	h.wantWoundDown(runner)
	h.wantExit(8)
}

func TestTheRunnerDoesNotInheritAnIgnoredSigpipe(t *testing.T) {
	recorded := filepath.Join(t.TempDir(), "sigign")
	t.Setenv("FAKE_SIGIGN_FILE", recorded)
	h := startHangupRun(t, nil, false, false)
	h.await()

	body, _ := os.ReadFile(recorded)
	mask, err := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(string(body), "SigIgn:")), 16, 64)
	if err != nil {
		t.Fatalf("VOID: no SigIgn line was recorded: %q", body)
	}
	if mask&(1<<(syscall.SIGPIPE-1)) != 0 {
		t.Errorf("runner SigIgn %x has SIGPIPE ignored, want it left at the default", mask)
	}
}

func TestHangupRightAfterTheReaderLeavesStillExitsInterrupted(t *testing.T) {
	t.Setenv("FAKE_MORE_IDS", "5000")
	t.Setenv("FAKE_TRICKLE", "2ms")
	t.Setenv("FAKE_TRICKLE_HANG", "1")
	h := startRun(t, newFakeAPI(t, everything), false, false, true, "--timeout=50s")
	runner := h.runnerPid()
	waitUntil(t, "events streaming", func() bool { return strings.Contains(h.out.String(), `"event":"progress"`) })

	_ = h.reader.Close()
	h.hangUp()
	h.await()

	h.wantWoundDown(runner)
	h.wantExit(130)
}
