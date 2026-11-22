package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/contract"
)

const updateBound = 3 * time.Second

var updateEnabled = []string{"SELECTIVE_TEST_VERSION=0.1.0", "SELECTIVE_NO_UPDATE_NOTIFIER=", "CI=", "GITHUB_ACTIONS=", "BUILD_NUMBER=", "RUN_ID=", "CODESPACES="}

func stuckUpdateState(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "update-check.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
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
			res, took := runCapped(t, 10*time.Second, t.TempDir(), stuckUpdateState(t), notFound.URL, updateEnabled, c.args...)

			if res.exit != c.exit || took > updateBound {
				t.Errorf("exit %d after %s, stderr %q; want exit %d within %s", res.exit, took, res.stderr, c.exit, updateBound)
			}
			if c.exit == contract.ExitUsage && (!strings.HasPrefix(res.stderr, "Error: ") || !strings.Contains(res.stdout, `"error":"usage_error"`)) {
				t.Errorf("stdout %q, stderr %q; want the usage error's normal output", res.stdout, res.stderr)
			}
		})
	}
}
