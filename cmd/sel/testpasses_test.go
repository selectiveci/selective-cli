package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func passIDs(events []map[string]any) []string {
	var ids []string
	for _, e := range events {
		if e["event"] == "test_pass" {
			ids = append(ids, fmt.Sprint(e["id"]))
		}
	}
	return ids
}

func TestSelTestPassesEmitsATestPassPerPassingTestAndKeepsSummaryLast(t *testing.T) {
	api := newFakeAPI(t, everything)
	runnerEnv(t)
	t.Setenv("FAKE_FAIL", "tests/test_calc.py::test_sub")

	res := selTest(t, pytestProject(t), api, "--json", "--passes")

	events := stdoutEvents(t, res.stdout)
	wantIDs := []string{"tests/test_calc.py::test_add", "tests/test_calc.py::test_mul"}
	got := passIDs(events)
	slices.Sort(got)
	if res.exit != 1 || !slices.Equal(got, wantIDs) {
		t.Fatalf("exit %d test_pass ids %v, want %v\nevents: %v", res.exit, got, wantIDs, eventNames(events))
	}
	for _, e := range events {
		if e["event"] != "test_pass" {
			continue
		}
		if e["node"] != "local" || e["file"] != "tests/test_calc.py" || e["line"] != 1.0 || e["ms"] != 10.0 {
			t.Errorf("test_pass = %v, want node local, file tests/test_calc.py, line 1, ms 10", e)
		}
	}
	if last := events[len(events)-1]; last["event"] != "summary" || last["passed"] != 2.0 {
		t.Errorf("last event = %v, want the summary", last)
	}
}

func TestSelTestWithoutPassesEmitsNoTestPass(t *testing.T) {
	api := newFakeAPI(t, everything)
	runnerEnv(t)
	t.Setenv("FAKE_FAIL", "tests/test_calc.py::test_sub")

	res := selTest(t, pytestProject(t), api, "--json")

	events := stdoutEvents(t, res.stdout)
	if got := passIDs(events); len(got) != 0 || events[len(events)-1]["passed"] != 2.0 {
		t.Errorf("test_pass ids %v in %v, want none on a run that passed 2 tests", got, eventNames(events))
	}
}

func prettyRun(t *testing.T, args ...string) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("needs util-linux script to give the binary a terminal")
	}
	script, err := exec.LookPath("script")
	if err != nil {
		t.Skip("needs util-linux script to give the binary a terminal")
	}
	api := newFakeAPI(t, selected)
	runnerEnv(t)
	cmdline := strings.Join(append([]string{os.Args[0], "test", "--org", "acme"}, args...), " ")
	cmd := exec.Command(script, "-qec", cmdline, "/dev/null")
	cmd.Dir = pytestProject(t)
	cmd.Env = append(os.Environ(), "SELECTIVE_TEST_RUN_MAIN=1", "SELECTIVE_HOST="+api.srv.URL, "SELECTIVE_TOKEN=tok", "SELECTIVE_CONFIG_DIR="+t.TempDir(), "NO_COLOR=1", "TERM=dumb")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return strings.ReplaceAll(string(out), "\r\n", "\n")
}

func TestSelTestPrettyPrintsOneLinePerPassingTestOnlyWithPasses(t *testing.T) {
	const passLine = "  ✓  local  tests/test_calc.py:1  10ms\n"
	without := prettyRun(t)
	with := prettyRun(t, "--passes")

	if strings.Contains(without, "✓") || !strings.Contains(without, "●  1 passed  0 failed") {
		t.Errorf("without --passes:\n%s\nwant the summary and no passing line", without)
	}
	if got := strings.Count(with, passLine); got != 1 || !strings.Contains(with, "●  1 passed  0 failed") {
		t.Errorf("with --passes (%d passing lines):\n%s\nwant exactly one passing line and the summary", got, with)
	}
}

func TestHelpJSONListsThePassesFlagAsABoolean(t *testing.T) {
	_, stdout, _ := execute("help", "--json")

	var doc struct {
		Help struct {
			Commands []struct {
				Path  string `json:"path"`
				Flags []struct {
					Name string `json:"name"`
					Type string `json:"type"`
				} `json:"flags"`
			} `json:"commands"`
		} `json:"help"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	for _, c := range doc.Help.Commands {
		if c.Path != "test" {
			continue
		}
		for _, f := range c.Flags {
			if f.Name == "passes" {
				if f.Type != "bool" {
					t.Errorf("passes type = %q, want bool", f.Type)
				}
				return
			}
		}
	}
	t.Error("help --json lists no passes flag on test")
}
