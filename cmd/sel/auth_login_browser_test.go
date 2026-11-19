//go:build !windows

package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The grant is denied so nothing is stored in a real keychain.
// An agent's shell reads sel's output through a pipe until EOF. A browser
// that outlives sel must not hold that pipe open.
func TestLoginInAPipeOpensTheBrowserAndStillExits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/device/code":
			_, _ = w.Write([]byte(`{"device_code":"d","user_code":"ABCD-EFGH","verification_uri_complete":"http://x/device?code=ABCD-EFGH","expires_in":600,"interval":1}`))
		case "/api/v1/device/token":
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"access_denied"}`))
		}
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	marker := filepath.Join(dir, "opened")
	fakeBrowser := filepath.Join(dir, "browser")
	script := "#!/bin/sh\necho \"$1\" > " + marker + "\necho noise; echo noise >&2\nexec sleep 30\n"
	if err := os.WriteFile(fakeBrowser, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "auth", "login", "--json")
	cmd.Env = append(os.Environ(),
		"SELECTIVE_TEST_RUN_MAIN=1",
		"SELECTIVE_HOST="+srv.URL,
		"SELECTIVE_TOKEN=",
		"SELECTIVE_CONFIG_DIR="+t.TempDir(),
		"BROWSER="+fakeBrowser,
		"SELECTIVE_NO_BROWSER=", "CI=", "GITHUB_ACTIONS=", "BUILD_NUMBER=", "RUN_ID=",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = 10 * time.Second
	start := time.Now()
	_ = cmd.Run()
	if code := cmd.ProcessState.ExitCode(); code != 6 {
		t.Fatalf("exit %d, want 6 (approval_denied)\nstderr: %s", code, stderr.String())
	}
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Errorf("sel held its output open for %s while the browser ran", elapsed)
	}

	if !strings.Contains(stdout.String(), `"browser_opened":true`) {
		t.Errorf("stdout = %q, want browser_opened true", stdout.String())
	}
	if !strings.Contains(stderr.String(), "  http://x/device?code=ABCD-EFGH\n") || strings.Contains(stdout.String()+stderr.String(), "noise") {
		t.Errorf("stderr = %q, want the URL printed and none of the browser's output", stderr.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := os.ReadFile(marker)
		if err == nil && strings.TrimSpace(string(got)) == "http://x/device?code=ABCD-EFGH" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("browser was not launched with the URL (marker %q, %v)", got, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
