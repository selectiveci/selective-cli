package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/contract"
)

const updateBound = 3 * time.Second

var updateEnabled = []string{"SELECTIVE_TEST_VERSION=0.1.0", "SELECTIVE_NO_UPDATE_NOTIFIER=", "CI=", "GITHUB_ACTIONS=", "BUILD_NUMBER=", "RUN_ID=", "CODESPACES=", "HTTPS_PROXY=http://127.0.0.1:1", "HTTP_PROXY=http://127.0.0.1:1", "NO_PROXY="}

type stuckState struct {
	dir  string
	path string
	fd   int
}

func stuckUpdateState(t *testing.T, writerHeld bool) stuckState {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "update-check.json")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if writerHeld {
		holder, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = holder.Close() })
	}
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(fd) })
	if _, err := syscall.InotifyAddWatch(fd, path, syscall.IN_OPEN); err != nil {
		t.Fatal(err)
	}
	return stuckState{dir: dir, path: path, fd: fd}
}

func (s stuckState) assertOpenedAndStillAFIFO(t *testing.T) {
	t.Helper()
	if n, _ := syscall.Read(s.fd, make([]byte, 4096)); n <= 0 {
		t.Error("the update state file was never opened, so this run proves nothing")
	}
	if info, err := os.Lstat(s.path); err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("the update state file is no longer a FIFO: %v, %v", info, err)
	}
}

func TestAStuckUpdateStateFileCannotHoldUpACommandsExit(t *testing.T) {
	notFound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"not_found"}`, http.StatusNotFound)
	}))
	t.Cleanup(notFound.Close)
	cases := map[string]struct {
		args []string
		exit int
	}{
		"usage error before --timeout is parsed": {[]string{"tail", "--bogus", "--timeout=1s"}, contract.ExitUsage},
		"usage error with no --timeout":          {[]string{"tail", "--bogus"}, contract.ExitUsage},
		"usage error on a bare group":            {[]string{"auth", "--bogus"}, contract.ExitUsage},
		"unknown command":                        {[]string{"nonsense"}, contract.ExitUsage},
		"valid command with no --timeout":        {[]string{"approvals", "show", "0193b6a0-7c1e-7d3a-9f2b-4a5c6d7e8f90"}, contract.ExitNotFound},
		"valid command under --timeout":          {[]string{"approvals", "show", "0193b6a0-7c1e-7d3a-9f2b-4a5c6d7e8f90", "--timeout=30s"}, contract.ExitNotFound},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stuck := stuckUpdateState(t, false)
			res, took := runCapped(t, 10*time.Second, t.TempDir(), stuck.dir, notFound.URL, updateEnabled, c.args...)
			stuck.assertOpenedAndStillAFIFO(t)

			if res.exit != c.exit || took > updateBound {
				t.Errorf("exit %d after %s, stderr %q; want exit %d within %s", res.exit, took, res.stderr, c.exit, updateBound)
			}
			if c.exit == contract.ExitUsage && (!strings.HasPrefix(res.stderr, "Error: ") || !strings.Contains(res.stdout, `"error":"usage_error"`)) {
				t.Errorf("stdout %q, stderr %q; want the usage error's normal output", res.stdout, res.stderr)
			}
		})
	}
}

func TestAStuckUpdateStateFileCannotHangVersionOrARecordedServerHeader(t *testing.T) {
	versionHeader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Selective-Cli-Latest-Version", "9.9.9")
		http.Error(w, `{"error":"not_found"}`, http.StatusNotFound)
	}))
	t.Cleanup(versionHeader.Close)
	cases := map[string]struct {
		args []string
		env  []string
		exit int
	}{
		"version with update checks on":        {[]string{"version"}, updateEnabled, 0},
		"version opted out of checks":          {[]string{"version"}, []string{"SELECTIVE_TEST_VERSION=0.1.0"}, 0},
		"a response carrying a version header": {[]string{"approvals", "show", "0193b6a0-7c1e-7d3a-9f2b-4a5c6d7e8f90"}, updateEnabled, contract.ExitNotFound},
	}
	for name, c := range cases {
		for shape, writerHeld := range map[string]bool{"no writer": false, "writer held": true} {
			t.Run(name+", "+shape, func(t *testing.T) {
				t.Parallel()
				stuck := stuckUpdateState(t, writerHeld)
				res, took := runCapped(t, 10*time.Second, t.TempDir(), stuck.dir, versionHeader.URL, c.env, c.args...)

				if res.exit != c.exit || took > updateBound {
					t.Errorf("exit %d after %s, stderr %q; want exit %d within %s", res.exit, took, res.stderr, c.exit, updateBound)
				}
				stuck.assertOpenedAndStillAFIFO(t)
			})
		}
	}
}
