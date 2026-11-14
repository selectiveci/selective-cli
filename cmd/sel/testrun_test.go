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
	"syscall"
	"testing"
	"time"
)

const (
	suitesBody = `{"suites":[{"id":"suite-1","name":"widget-pytest","type":"pytest","repo":"acme/widget"}]}`
	twoSuites  = `{"suites":[{"id":"suite-1","name":"a","type":"pytest","repo":"acme/widget"},{"id":"suite-2","name":"b","type":"pytest","repo":"Acme/Widget"}]}`
	selected   = `{"selection":{"cause":"map","map":{"id":"m1"},"selected":["tests/test_calc.py::test_sub"],"outcome":"select","counts":{"total":3,"selected":1,"skipped":2,"changed_files":1}}}`
	nothing    = `{"selection":{"cause":"map","map":{"id":"m1"},"selected":[],"outcome":"select","counts":{"total":3,"selected":0,"skipped":3,"changed_files":0}}}`
	everything = `{"selection":{"cause":"untraced_change","map":{"id":"m1"},"selected":["tests/test_calc.py::test_add","tests/test_calc.py::test_sub","tests/test_calc.py::test_mul"],"outcome":"full","counts":{"total":3,"selected":3,"skipped":0,"changed_files":1}}}`
)

type fakeAPI struct {
	srv        *httptest.Server
	mu         sync.Mutex
	suites     string
	listed     int
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
	api := &fakeAPI{suites: suitesBody, status: 200, reply: reply}
	api.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/orgs/acme/suites":
			api.listed++
			_, _ = io.WriteString(w, api.suites)
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
	return runBinary(t, dir, api.srv.URL, append([]string{"test", "--org", "acme"}, args...)...)
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
			if sum["interrupted"] != nil {
				t.Errorf("summary = %v, want no interrupted field on a finished run", sum)
			}
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

func TestSelTestErrorsExitByCodeAndEndTheStreamWithAnErrorObject(t *testing.T) {
	cases := []struct {
		name   string
		status int
		reply  string
		suites string
		exit   int
		code   string
		hint   string
	}{
		{"unknown suite", 404, `{"error":"suite_not_found","message":"No such suite in this organization."}`, suitesBody, 3, "suite_not_found", ""},
		{"bad token", 401, `{"error":"unauthorized","message":"Missing, invalid or revoked API token."}`, suitesBody, 4, "unauthorized", ""},
		{"two suites for one repo", 200, selected, twoSuites, 5, "usage_error", "suite-1, suite-2"},
		{"no suite for the repo", 200, selected, `{"suites":[]}`, 3, "suite_not_found", "acme/widget"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeAPI(t, tc.reply)
			api.status, api.suites = tc.status, tc.suites
			runnerEnv(t)

			res := selTest(t, pytestProject(t), api)

			events := stdoutEvents(t, res.stdout)
			last := events[len(events)-1]
			if res.exit != tc.exit || last["error"] != tc.code || !strings.Contains(fmt.Sprint(last["message"]), tc.hint) {
				t.Errorf("exit %d last line %v, want exit %d with error %q last and %q in its message", res.exit, last, tc.exit, tc.code, tc.hint)
			}
		})
	}
}

func TestSelTestSuiteFlagSkipsTheListing(t *testing.T) {
	api := newFakeAPI(t, nothing)
	api.suites = twoSuites
	runnerEnv(t)

	res := selTest(t, pytestProject(t), api, "--suite", "suite-2")

	if res.exit != 0 || api.firstSelection(t)["suite_id"] != "suite-2" || api.listed != 0 {
		t.Errorf("exit %d request %v listings %d, want the given suite and no listing", res.exit, api.firstSelection(t), api.listed)
	}
}

func TestSelTestDeadlineWhileTheClientNeverConnectsIsATimeoutNotAnInterrupt(t *testing.T) {
	api := newFakeAPI(t, selected)
	runnerEnv(t)
	t.Setenv("FAKE_NEVER_CONNECT", "1")
	started := time.Now()

	res := selTest(t, pytestProject(t), api, "--timeout", "1s")

	events := stdoutEvents(t, res.stdout)
	if last := events[len(events)-1]; res.exit != 8 || last["error"] != "timeout" {
		t.Errorf("exit %d last line %v, want exit 8 and a timeout error", res.exit, last)
	}
	if took := time.Since(started); took > 10*time.Second {
		t.Errorf("took %s, want the 1s deadline to bound the wait", took)
	}
}

func waitUntil(t *testing.T, what string, done func() bool) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); !done(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func interruptedRun(t *testing.T, ready func(stdout func() string)) ([]map[string]any, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "test", "--org", "acme")
	cmd.Dir = pytestProject(t)
	cmd.Env = append(os.Environ(), "SELECTIVE_TEST_RUN_MAIN=1", "SELECTIVE_HOST="+newFakeAPI(t, selected).srv.URL, "SELECTIVE_TOKEN=tok", "SELECTIVE_CONFIG_DIR="+t.TempDir())
	var stdout syncBuffer
	cmd.Stdout = &stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	ready(stdout.String)
	_ = cmd.Process.Signal(syscall.SIGINT)
	_ = cmd.Wait()
	return stdoutEvents(t, stdout.String()), cmd.ProcessState.ExitCode()
}

type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestSelTestInterruptExitsOneThirty(t *testing.T) {
	runnerEnv(t)
	t.Setenv("FAKE_HANG", "1")

	events, code := interruptedRun(t, func(stdout func() string) {
		waitUntil(t, "the selection event", func() bool { return strings.Contains(stdout(), `"event":"selection"`) })
	})

	if code != 130 {
		t.Errorf("exit = %d, want 130", code)
	}
	last := events[len(events)-1]
	if last["event"] != "summary" || last["interrupted"] != true || last["selected"] != 1.0 || last["passed"] != 0.0 {
		t.Errorf("last line = %v, want a summary with interrupted:true and the counts so far", last)
	}
}

func TestSelTestInterruptBeforeTheRunnerConnectsStillEndsWithASummary(t *testing.T) {
	runnerEnv(t)
	t.Setenv("FAKE_NEVER_CONNECT", "1")
	started := filepath.Join(t.TempDir(), "started")
	t.Setenv("FAKE_ARGS_FILE", started)

	events, code := interruptedRun(t, func(func() string) {
		waitUntil(t, "the runner to start", func() bool { _, err := os.Stat(started); return err == nil })
	})

	if code != 130 {
		t.Errorf("exit = %d, want 130", code)
	}
	if len(events) != 1 || events[0]["event"] != "summary" || events[0]["interrupted"] != true || events[0]["selected"] != 0.0 {
		t.Errorf("events = %v, want one summary with interrupted:true and nothing selected", events)
	}
}

func TestSelTestInterruptAfterTheSummaryDoesNotPrintASecondOne(t *testing.T) {
	runnerEnv(t)
	t.Setenv("FAKE_LINGER", "1")

	events, code := interruptedRun(t, func(stdout func() string) {
		waitUntil(t, "the summary", func() bool { return strings.Contains(stdout(), `"event":"summary"`) })
	})

	summaries := 0
	for _, name := range eventNames(events) {
		if name == "summary" {
			summaries++
		}
	}
	if code != 130 || summaries != 1 || events[len(events)-1]["interrupted"] != nil {
		t.Errorf("exit %d with %d summaries, last %v, want 130 and the finished run's single summary", code, summaries, events[len(events)-1])
	}
}

func TestSelTestListPrintsTheSelectionWithIdsAndRunsNothing(t *testing.T) {
	api := newFakeAPI(t, selected)
	ran := runnerEnv(t)

	res := selTest(t, pytestProject(t), api, "--list")

	events := stdoutEvents(t, res.stdout)
	sel := firstNamed(events, "selection")
	if res.exit != 0 || !slices.Equal(anyStrings(sel["ids"]), []string{"tests/test_calc.py::test_sub"}) || events[len(events)-1]["event"] != "summary" {
		t.Errorf("exit %d events %v, want the selection with its ids, ending in summary", res.exit, eventNames(events))
	}
	if got := ranIDs(t, ran); len(got) != 0 {
		t.Errorf("--list ran %v", got)
	}
}

func TestSelTestPathsStandInForTheGitDiffAndConflictWithAgainst(t *testing.T) {
	api := newFakeAPI(t, nothing)
	runnerEnv(t)
	dir := pytestProject(t)

	res := selTest(t, dir, api, "src/other.py", "src/calc.py")

	if got := anyStrings(api.firstSelection(t)["changed"]); res.exit != 0 || !slices.Equal(got, []string{"src/other.py", "src/calc.py"}) {
		t.Errorf("exit %d changed %v, want the given paths", res.exit, got)
	}
	if conflict := selTest(t, dir, api, "--against", "main", "src/calc.py"); conflict.exit != 5 {
		t.Errorf("paths with --against: exit %d, want 5", conflict.exit)
	}
}

func TestHelpJSONShowsTestWithItsAnnotationsAndNoTransport(t *testing.T) {
	code, stdout, _ := execute("help", "--json")

	var doc struct {
		Help struct {
			Commands []map[string]any `json:"commands"`
		} `json:"help"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil || code != 0 {
		t.Fatalf("help --json exit %d: %v", code, err)
	}
	var found map[string]any
	for _, c := range doc.Help.Commands {
		if c["path"] == "__transport" {
			t.Error("help --json lists __transport")
		}
		if c["path"] == "test" {
			found = c
		}
	}
	if found == nil {
		t.Fatal("help --json has no test command")
	}
	if codes := fmt.Sprint(found["exit_codes"]); codes != "[0 1 3 4 5 6 8 130]" {
		t.Errorf("exit_codes = %s", codes)
	}
	if events := fmt.Sprint(found["events"]); events != "[build_start selection test_fail progress heartbeat summary]" {
		t.Errorf("events = %s", events)
	}
	if found["sends_paths"] != true || found["mutates"] != nil {
		t.Errorf("sends_paths = %v mutates = %v, want true and absent", found["sends_paths"], found["mutates"])
	}
}
