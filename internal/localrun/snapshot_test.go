package localrun

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
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

func gitIn(t *testing.T, dir, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func TestSeatSnapshotMatchesGitOnWorkingTreeEdgeCases(t *testing.T) {
	cases := []struct {
		script string
		check  func(t *testing.T, repo string, code int, out map[string]any, errw string)
	}{
		{"symlink.sh", func(t *testing.T, repo string, code int, out map[string]any, _ string) {
			files := out["files"].(map[string]any)
			if want := gitIn(t, repo, "b.txt", "hash-object", "--stdin"); files["link"] != want {
				t.Errorf("link = %v, want the hash of its link text %s", files["link"], want)
			}
			if !slices.Contains(toStrings(out["dirty"]), "link") {
				t.Errorf("dirty = %v, want link", out["dirty"])
			}
		}},
		{"newline.sh", func(t *testing.T, repo string, code int, out map[string]any, _ string) {
			files := out["files"].(map[string]any)
			want := gitIn(t, repo, "changed\n", "hash-object", "--stdin")
			for _, path := range []string{"ok.txt", "a\nb.txt"} {
				if files[path] != want {
					t.Errorf("%q = %v, want %s", path, files[path], want)
				}
			}
		}},
		{"dir-replaces-file.sh", func(t *testing.T, repo string, code int, out map[string]any, _ string) {
			files := out["files"].(map[string]any)
			if _, present := files["d"]; present {
				t.Errorf("files still lists d, which is a directory now: %v", files)
			}
			if dirty := toStrings(out["dirty"]); !slices.Equal(dirty, []string{"d"}) {
				t.Errorf("dirty = %v, want [d]", dirty)
			}
		}},
		{"status-failure.sh", func(t *testing.T, repo string, code int, out map[string]any, errw string) {
			if code != 1 || out != nil || !strings.Contains(errw, "git status") {
				t.Errorf("exit %d stdout %v stderr %q, want exit 1, no snapshot and a git status message", code, out, errw)
			}
		}},
		{"trailing-space-root.sh", func(t *testing.T, repo string, code int, out map[string]any, errw string) {
			if want := gitIn(t, repo, "", "rev-parse", "HEAD"); code != 0 || out["sha"] != want {
				t.Errorf("exit %d sha %v stderr %q, want exit 0 and %s", code, out["sha"], errw, want)
			}
			if dirty, isList := out["dirty"].([]any); !isList || len(dirty) != 0 {
				t.Errorf("dirty = %#v on a clean tree, want an empty list as transport prints", out["dirty"])
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.script, func(t *testing.T) {
			made, err := exec.Command("sh", filepath.Join("testdata", "snapshot-cases", c.script), t.TempDir()).Output()
			if err != nil {
				t.Fatalf("case script: %v", err)
			}
			repo := string(made)
			var out, errw bytes.Buffer
			code := RunSeat([]string{"--snapshot", repo}, strings.NewReader(""), &out, &errw)
			var doc map[string]any
			if out.Len() > 0 {
				doc = decodeJSON(t, out.Bytes())
			}
			c.check(t, repo, code, doc, errw.String())
		})
	}
}

func toStrings(v any) []string {
	var out []string
	for _, item := range v.([]any) {
		out = append(out, item.(string))
	}
	return out
}
