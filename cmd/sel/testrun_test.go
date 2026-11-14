package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

const (
	selected   = `{"selection":{"cause":"map","map":{"id":"m1"},"selected":["tests/test_calc.py::test_sub"],"outcome":"select","counts":{"total":3,"selected":1,"skipped":2,"changed_files":1}}}`
	nothing    = `{"selection":{"cause":"map","map":{"id":"m1"},"selected":[],"outcome":"select","counts":{"total":3,"selected":0,"skipped":3,"changed_files":0}}}`
	everything = `{"selection":{"cause":"untraced_change","map":{"id":"m1"},"selected":["tests/test_calc.py::test_add","tests/test_calc.py::test_sub","tests/test_calc.py::test_mul"],"outcome":"full","counts":{"total":3,"selected":3,"skipped":0,"changed_files":1}}}`
)

type fakeAPI struct {
	srv        *httptest.Server
	mu         sync.Mutex
	status     int
	reply      string
	selections []map[string]any
}

func (a *fakeAPI) firstSelection(t *testing.T) map[string]any {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.selections) == 0 {
		t.Error("the server got no selection request")
		return map[string]any{}
	}
	return a.selections[0]
}

func newFakeAPI(t *testing.T, reply string) *fakeAPI {
	t.Helper()
	api := &fakeAPI{status: 200, reply: reply}
	api.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/v1/orgs/acme/selections":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			api.selections = append(api.selections, body)
			w.WriteHeader(api.status)
			_, _ = io.WriteString(w, api.reply)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.srv.Close)
	return api
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func pytestProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	client := "#!/bin/sh\necho selective_pytest >/dev/null\nexec env FAKE_RUNNER=1 " + os.Args[0] + " \"$@\"\n"
	for name, body := range map[string]string{
		"pyproject.toml":      "[tool.pytest.ini_options]\n",
		"src/calc.py":         "v1\n",
		"tests/test_calc.py":  "v1\n",
		".venv/bin/selective": client,
	} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "remote", "add", "origin", "https://github.com/acme/widget.git")
	git(t, dir, "add", "-f", ".")
	git(t, dir, "commit", "-q", "-m", "base")
	git(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	git(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	git(t, dir, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(dir, "src/calc.py"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "commit", "-q", "-am", "change calc")
	return dir
}

func runnerEnv(t *testing.T) (ranFile string) {
	t.Helper()
	ranFile = filepath.Join(t.TempDir(), "ran")
	t.Setenv("FAKE_IDS", "tests/test_calc.py::test_add,tests/test_calc.py::test_sub,tests/test_calc.py::test_mul")
	t.Setenv("FAKE_RAN_FILE", ranFile)
	return ranFile
}

func ranIDs(t *testing.T, file string) []string {
	t.Helper()
	body, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	return strings.Fields(string(body))
}

func stdoutEvents(t *testing.T, stdout string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		var doc map[string]any
		if err := json.Unmarshal([]byte(line), &doc); err != nil || doc["v"] != float64(1) {
			t.Fatalf("stdout line %q is not a v:1 JSON document (%v)\nfull stdout:\n%s", line, err, stdout)
		}
		events = append(events, doc)
	}
	return events
}

func eventNames(events []map[string]any) []string {
	var names []string
	for _, e := range events {
		if name, ok := e["event"].(string); ok {
			names = append(names, name)
		} else {
			names = append(names, "error:"+fmt.Sprint(e["error"]))
		}
	}
	return names
}

func firstNamed(events []map[string]any, name string) map[string]any {
	for _, e := range events {
		if e["event"] == name {
			return e
		}
	}
	return nil
}

func selTest(t *testing.T, dir string, api *fakeAPI, args ...string) cliResult {
	t.Helper()
	return runBinary(t, dir, api.srv.URL, append([]string{"test", "--org", "acme", "--suite", "suite-1"}, args...)...)
}

func TestSelTestSendsTheChangeAndShowsTheServersDecisionOnAnNDJSONOnlyStdout(t *testing.T) {
	api := newFakeAPI(t, selected)
	runnerEnv(t)
	args := filepath.Join(t.TempDir(), "args")
	t.Setenv("FAKE_ARGS_FILE", args)

	res := selTest(t, pytestProject(t), api, "--", "-k", "sub")

	events := stdoutEvents(t, res.stdout)
	want := []string{"build_start", "selection", "progress", "summary"}
	if got := eventNames(events); res.exit != 0 || !slices.Equal(got, want) {
		t.Errorf("exit %d events = %v, want %v", res.exit, got, want)
	}
	sel := firstNamed(events, "selection")
	if sel["cause"] != "map" || sel["source"] != "map" || sel["selected"] != 1.0 || sel["skipped"] != 2.0 || sel["total"] != 3.0 || sel["changed_files"] != 1.0 {
		t.Errorf("selection = %v", sel)
	}
	if strings.Contains(res.stdout, "fake runner") || !strings.Contains(res.stderr, "fake runner stdout noise") {
		t.Errorf("runner output must reach stderr only\nstdout: %s\nstderr: %s", res.stdout, res.stderr)
	}
	if got := strings.Fields(string(mustRead(t, args))); !slices.Equal(got, []string{"pytest", "-k", "sub"}) {
		t.Errorf("runner args = %v, want the framework args after --", got)
	}
	body := api.firstSelection(t)
	if body["suite_id"] != "suite-1" || body["framework"] != "pytest" || body["language"] != "python" || !slices.Equal(anyStrings(body["changed"]), []string{"src/calc.py"}) || len(anyStrings(body["test_cases"])) != 3 {
		t.Errorf("selection request = %v", body)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func anyStrings(v any) []string {
	var out []string
	items, _ := v.([]any)
	for _, item := range items {
		out = append(out, fmt.Sprint(item))
	}
	return out
}

func TestSelTestOutcomes(t *testing.T) {
	sub := "tests/test_calc.py::test_sub"
	cases := []struct {
		name    string
		reply   string
		fail    string
		exit    int
		ran     int
		outcome string
		summary map[string]any
	}{
		{"everything selected passes", selected, "", 0, 1, "select", map[string]any{"passed": 1.0, "failed": 0.0, "selected": 1.0}},
		{"zero selected exits zero and runs nothing", nothing, "", 0, 0, "select", map[string]any{"selected": 0.0, "skipped": 3.0}},
		{"a full outcome runs everything and says so", everything, "", 0, 3, "full", map[string]any{"passed": 3.0, "selected": 3.0}},
		{"a failing test exits one", everything, sub, 1, 3, "full", map[string]any{"passed": 2.0, "failed": 1.0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeAPI(t, tc.reply)
			ran := runnerEnv(t)
			t.Setenv("FAKE_FAIL", tc.fail)

			res := selTest(t, pytestProject(t), api)

			events := stdoutEvents(t, res.stdout)
			if res.exit != tc.exit {
				t.Errorf("exit = %d, want %d\nstderr: %s", res.exit, tc.exit, res.stderr)
			}
			if sel := firstNamed(events, "selection"); sel["outcome"] != tc.outcome {
				t.Errorf("selection = %v, want outcome %q", sel, tc.outcome)
			}
			sum := events[len(events)-1]
			for key, want := range tc.summary {
				if sum["event"] != "summary" || sum[key] != want {
					t.Errorf("last event %v, want a summary with %s = %v", sum, key, want)
				}
			}
			if got := ranIDs(t, ran); len(got) != tc.ran {
				t.Errorf("ran %v, want %d tests", got, tc.ran)
			}
			if fail := firstNamed(events, "test_fail"); (fail != nil) != (tc.fail != "") || (fail != nil && (fail["id"] != tc.fail || fail["ms"] == nil)) {
				t.Errorf("test_fail = %v, want it only for %q, with id and ms", fail, tc.fail)
			}
		})
	}
}

func TestSelTestRefusesAnArgumentThatIsNotAfterTheDash(t *testing.T) {
	res := selTest(t, pytestProject(t), newFakeAPI(t, selected), "tests/")

	if events := stdoutEvents(t, res.stdout); res.exit != 5 || events[len(events)-1]["error"] != "usage_error" {
		t.Errorf("exit %d stdout %s, want exit 5 with a usage_error", res.exit, res.stdout)
	}
}
