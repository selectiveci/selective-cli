package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const noticeVersionVariable = "SELECTIVE_TEST_NOTICE_VERSION"

func init() {
	if v := os.Getenv(noticeVersionVariable); v != "" {
		version = v
		isTerminal = func(io.Writer) bool { return true }
	}
}

func runWithNotice(t *testing.T, dir, host string, args ...string) cliResult {
	t.Helper()
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "update-check.json"), []byte(cacheJSON("")), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"SELECTIVE_TEST_RUN_MAIN=1",
		noticeVersionVariable+"=0.1.0",
		"SELECTIVE_HOST="+host,
		"SELECTIVE_TOKEN=tok",
		"SELECTIVE_CONFIG_DIR="+configDir,
		"CI=", "GITHUB_ACTIONS=", "BUILD_NUMBER=", "RUN_ID=", "CODESPACES=", "SELECTIVE_NO_UPDATE_NOTIFIER=",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	timer := time.AfterFunc(time.Minute, func() { _ = cmd.Process.Kill() })
	defer timer.Stop()
	err := cmd.Run()
	res := cliResult{stdout: stdout.String(), stderr: stderr.String()}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.exit = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res
}

func assertNoticeAfterNonZeroExit(t *testing.T, res cliResult, wantExit int) {
	t.Helper()
	if res.exit != wantExit {
		t.Errorf("exit = %d, want %d (stderr: %s)", res.exit, wantExit, res.stderr)
	}
	if !strings.Contains(res.stderr, "A new release of sel is available: 0.1.0 → 0.3.0") {
		t.Errorf("stderr has no update notice: %q", res.stderr)
	}
	if strings.Contains(res.stdout, "release of sel") {
		t.Errorf("stdout carries the update notice: %q", res.stdout)
	}
	lines := strings.Split(strings.TrimSpace(res.stdout), "\n")
	if res.stdout == "" {
		t.Errorf("stdout is empty, want NDJSON events")
	}
	for _, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Errorf("stdout line is not JSON: %q", line)
		}
	}
}

func TestTailThatExitsNonZeroStillPrintsTheUpdateNotice(t *testing.T) {
	srv := tailServer{
		frames:       []string{frameStart, frameSel, frameFail, frameProgress, summaryFail},
		lookupStatus: http.StatusOK,
		lookupBody:   `{"build_id":"b","status":"completed","run_count":1,"suite_count":1}`,
	}.start(t)

	res := runWithNotice(t, gitRepo(t), srv.URL, "tail", "--json")

	assertNoticeAfterNonZeroExit(t, res, 1)
}

func TestSelTestThatExitsNonZeroStillPrintsTheUpdateNotice(t *testing.T) {
	api := newFakeAPI(t, everything)
	runnerEnv(t)
	t.Setenv("FAKE_FAIL", "tests/test_calc.py::test_sub")

	res := runWithNotice(t, pytestProject(t), api.srv.URL, "test", "--org", "acme", "--json")

	assertNoticeAfterNonZeroExit(t, res, 1)
}
