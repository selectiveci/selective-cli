package localrun

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func decodeJSON(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("not one JSON object: %v\n%s", err, raw)
	}
	return doc
}

func TestSeatSnapshotEqualsTheRealTransportOnADirtyFixtureRepo(t *testing.T) {
	repo := t.TempDir()
	if out, err := exec.Command("sh", "testdata/snapshot-fixture.sh", repo).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v\n%s", err, out)
	}
	for golden, dir := range map[string]string{"snapshot-root.golden.json": repo, "snapshot-sub.golden.json": filepath.Join(repo, "sub")} {
		want, err := os.ReadFile(filepath.Join("testdata", golden))
		if err != nil {
			t.Fatal(err)
		}
		var out, errw bytes.Buffer
		if code := RunSeat([]string{"--snapshot", dir}, strings.NewReader(""), &out, &errw); code != 0 {
			t.Fatalf("%s: exit %d stderr %q", golden, code, errw.String())
		}
		if got := decodeJSON(t, out.Bytes()); !reflect.DeepEqual(got, decodeJSON(t, want)) {
			t.Errorf("%s: snapshot differs from the real transport's\n got %s\nwant %s", golden, out.String(), want)
		}
	}
}

func TestSeatSnapshotOutsideAGitCheckoutFailsOnStderrOnly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	var out, errw bytes.Buffer
	if code := RunSeat([]string{"--snapshot", dir}, strings.NewReader(""), &out, &errw); code != 1 || out.Len() != 0 || !strings.Contains(errw.String(), "not a git checkout") {
		t.Errorf("exit %d stdout %q stderr %q, want exit 1 and a stderr message only", code, out.String(), errw.String())
	}
}

func TestSeatBuildEnvFillsTheNineVariablesWithoutARealCredential(t *testing.T) {
	t.Setenv("SELECTIVE_API_KEY", "real-secret-key")
	t.Setenv("SELECTIVE_TOKEN", "real-secret-token")
	var out, errw bytes.Buffer
	if code := RunSeat([]string{"--build-env"}, strings.NewReader(""), &out, &errw); code != 0 {
		t.Fatalf("exit %d stderr %q", code, errw.String())
	}
	if strings.Contains(out.String(), "real-secret") {
		t.Errorf("build env leaked the environment: %s", out.String())
	}
	got := decodeJSON(t, out.Bytes())
	for _, key := range []string{"api_key", "host", "platform", "branch", "sha", "run_id", "run_attempt", "git_repo_full_name", "git_provider"} {
		if value, _ := got[key].(string); value == "" {
			t.Errorf("%s is empty in %s", key, out.String())
		}
	}
	if got["api_key"] != "sel-local" || !strings.HasPrefix(got["host"].(string), "ws://") || len(got) != 17 {
		t.Errorf("api_key %v host %v with %d keys, want sel-local, a ws:// host and the 17 transport keys", got["api_key"], got["host"], len(got))
	}
}
