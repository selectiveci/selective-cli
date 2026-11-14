package main

import (
	"bytes"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestTailOutsideAGitRepoIsMissingLocalSetup(t *testing.T) {
	srv := tailServer{lookupStatus: http.StatusNotFound, lookupBody: `{}`}.start(t)

	res := runBinary(t, t.TempDir(), srv.URL, "tail", "--json")

	assertRefusal(t, res, 3, "not_found")
}

func TestTailWithAnUnreadableConfigIsAnInternalError(t *testing.T) {
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "tail", "--json")
	cmd.Dir = gitRepo(t)
	cmd.Env = append(os.Environ(), "SELECTIVE_TEST_RUN_MAIN=1", "SELECTIVE_TOKEN=tok", "SELECTIVE_CONFIG_DIR="+configDir)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()

	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 9 || !bytes.Contains(stdout.Bytes(), []byte(`"error":"internal_error"`)) {
		t.Errorf("err = %v stdout = %q, want exit 9 with internal_error", err, stdout.String())
	}
}
