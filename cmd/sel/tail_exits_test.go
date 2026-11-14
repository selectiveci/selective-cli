package main

import (
	"bytes"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func runWithConfig(t *testing.T, dir, configDir string) cliResult {
	t.Helper()
	cmd := exec.Command(os.Args[0], "tail", "--json")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "SELECTIVE_TEST_RUN_MAIN=1", "SELECTIVE_TOKEN=tok", "SELECTIVE_CONFIG_DIR="+configDir)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	res := cliResult{stdout: stdout.String()}
	if exitErr, ok := err.(*exec.ExitError); ok {
		res.exit = exitErr.ExitCode()
	}
	return res
}

func TestTailOutsideAGitRepoIsGitRepoNotFound(t *testing.T) {
	srv := tailServer{lookupStatus: http.StatusNotFound, lookupBody: `{}`}.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "tail", "--json")

	assertRefusal(t, res, 3, "git_repo_not_found")
}

func TestTailWithInvalidConfigContentsIsAUsageError(t *testing.T) {
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	assertRefusal(t, runWithConfig(t, gitRepo(t), configDir), 5, "usage_error")
}

func TestTailWithAnUnreadableConfigIsAUsageError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads mode 000 files")
	}
	configDir := t.TempDir()
	path := filepath.Join(configDir, "config.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}

	assertRefusal(t, runWithConfig(t, gitRepo(t), configDir), 5, "usage_error")
}

func TestTailWithAMalformedHostIsAUsageError(t *testing.T) {
	for _, host := range []string{"not a url", "http://[::1", "/no/scheme", "   "} {
		t.Run(host, func(t *testing.T) {
			assertRefusal(t, runBinary(t, gitRepo(t), host, "tail", "--json"), 5, "usage_error")
		})
	}
}

func TestTailWithAnUnresolvableHostStaysANetworkError(t *testing.T) {
	assertRefusal(t, runBinary(t, gitRepo(t), "https://nope.invalid", "tail", "--json"), 4, "network_error")
}
