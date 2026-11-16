package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
)

type watchRun struct {
	t       *testing.T
	cmd     *exec.Cmd
	capture string
	dir     string
}

func startWatch(t *testing.T, api *fakeAPI, args ...string) *watchRun {
	t.Helper()
	w := &watchRun{t: t, dir: pytestProject(t), capture: filepath.Join(t.TempDir(), "stdout")}
	w.cmd = exec.Command(os.Args[0], append([]string{"test", "--org", "acme", "--watch", "--json"}, args...)...)
	w.cmd.Dir = w.dir
	w.cmd.Env = append(os.Environ(), "SELECTIVE_TEST_RUN_MAIN=1", "SELECTIVE_HOST="+api.srv.URL, "SELECTIVE_TOKEN=tok", "SELECTIVE_CONFIG_DIR="+t.TempDir())
	out, err := os.Create(w.capture)
	if err != nil {
		t.Fatal(err)
	}
	w.cmd.Stdout = out
	if err := w.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.cmd.Process.Kill() })
	return w
}

func (w *watchRun) stdout() string { return string(mustRead(w.t, w.capture)) }

func (w *watchRun) await(event string, count int) {
	w.t.Helper()
	waitUntil(w.t, event, func() bool { return strings.Count(w.stdout(), `"event":"`+event+`"`) >= count })
}

func (w *watchRun) edit(name, body string) {
	w.t.Helper()
	if err := os.WriteFile(filepath.Join(w.dir, name), []byte(body), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *watchRun) interrupt() ([]map[string]any, int) {
	w.t.Helper()
	_ = w.cmd.Process.Signal(syscall.SIGINT)
	_ = w.cmd.Wait()
	return stdoutEvents(w.t, w.stdout()), w.cmd.ProcessState.ExitCode()
}

func TestSelTestWatchStreamsAnIterationEndPerRunAndOneTerminalSummaryAtSIGINT(t *testing.T) {
	api := newFakeAPI(t, selected)
	runnerEnv(t)
	w := startWatch(t, api)
	w.await("iteration_end", 1)
	w.edit("tests/test_calc.py", "v2\n")
	w.await("iteration_end", 2)

	events, code := w.interrupt()

	names := eventNames(events)
	var ends []map[string]any
	for _, e := range events {
		if e["event"] == "iteration_end" {
			ends = append(ends, e)
		}
	}
	if code != 130 || len(ends) < 2 || names[len(names)-1] != "summary" || slices.Index(names, "summary") != len(names)-1 {
		t.Fatalf("exit %d events %v, want 130, at least two iteration_end and exactly one summary, last", code, names)
	}
	for i, e := range ends {
		if e["iteration"] != float64(i+1) || e["passed"] != 1.0 || e["failed"] != 0.0 || e["selected"] != 1.0 || e["skipped"] != 2.0 {
			t.Errorf("iteration_end %d = %v, want iteration %d with the run's counts", i, e, i+1)
		}
	}
	if sum := events[len(events)-1]; sum["interrupted"] != true || sum["passed"] != 1.0 || sum["selected"] != 1.0 {
		t.Errorf("summary = %v, want interrupted:true with the last iteration's counts", sum)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.selections) < 2 || !slices.Contains(anyStrings(api.selections[1]["changed"]), "tests/test_calc.py") {
		t.Errorf("selection requests %v, want the second to carry the file edited since the first", api.selections)
	}
}

func TestSelTestWatchInterruptedMidRunReportsTheCountsSoFarAndNoIterationEnd(t *testing.T) {
	api := newFakeAPI(t, selected)
	runnerEnv(t)
	t.Setenv("FAKE_HANG", "1")
	w := startWatch(t, api)
	w.await("selection", 1)

	events, code := w.interrupt()

	names := eventNames(events)
	if sum := events[len(events)-1]; code != 130 || names[len(names)-1] != "summary" || sum["interrupted"] != true || sum["selected"] != 1.0 || slices.Contains(names, "iteration_end") {
		t.Errorf("exit %d events %v, want 130 and a last summary of the run in progress, with no iteration_end", code, names)
	}
}

func TestSelTestWatchWithListIsAUsageError(t *testing.T) {
	api := newFakeAPI(t, selected)
	runnerEnv(t)

	res := selTest(t, pytestProject(t), api, "--watch", "--list")

	events := stdoutEvents(t, res.stdout)
	last := events[len(events)-1]
	if message := fmt.Sprint(last["message"]); res.exit != 5 || last["error"] != "usage_error" || !strings.Contains(message, "--watch") || !strings.Contains(message, "--list") {
		t.Errorf("exit %d last line %v, want exit 5, usage_error, and a message naming both flags", res.exit, last)
	}
}

func TestHelpJSONDeclaresIterationEndAndKeepsWatchOutOfMCP(t *testing.T) {
	_, stdout, _ := execute("help", "--json")

	var doc struct {
		Help struct {
			Events   []struct{ Name string } `json:"events"`
			Commands []struct {
				Path     string   `json:"path"`
				Events   []string `json:"events"`
				SkipFlag []string `json:"mcp_skip_flags"`
			} `json:"commands"`
		} `json:"help"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	registered := slices.ContainsFunc(doc.Help.Events, func(e struct{ Name string }) bool { return e.Name == "iteration_end" })
	for _, c := range doc.Help.Commands {
		if c.Path == "test" {
			if !registered || !slices.Contains(c.Events, "iteration_end") || !slices.Contains(c.SkipFlag, "watch") {
				t.Errorf("registered=%v events=%v mcp_skip_flags=%v, want iteration_end registered and declared, and watch skipped for MCP", registered, c.Events, c.SkipFlag)
			}
			return
		}
	}
	t.Error("help --json lists no test command")
}

func TestSelTestWatchPrettyShowsAWatchingLineAndABlockPerIterationThenASummaryOnCtrlC(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs util-linux script to give the binary a terminal")
	}
	script, err := exec.LookPath("script")
	if err != nil {
		t.Skip("needs util-linux script to give the binary a terminal")
	}
	api := newFakeAPI(t, selected)
	runnerEnv(t)
	dir := pytestProject(t)
	capture := filepath.Join(t.TempDir(), "pty")
	out, err := os.Create(capture)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(script, "-qec", os.Args[0]+" test --org acme --watch", "/dev/null")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "SELECTIVE_TEST_RUN_MAIN=1", "SELECTIVE_HOST="+api.srv.URL, "SELECTIVE_TOKEN=tok", "SELECTIVE_CONFIG_DIR="+t.TempDir(), "NO_COLOR=1", "TERM=dumb")
	keys, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	seen := func() string { return strings.ReplaceAll(string(mustRead(t, capture)), "\r\n", "\n") }
	waitUntil(t, "iteration 1", func() bool { return strings.Contains(seen(), "iteration 1") })
	if err := os.WriteFile(filepath.Join(dir, "tests/test_calc.py"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the second watching line", func() bool { return strings.Count(seen(), "Watching for changes") >= 2 })
	_, _ = keys.Write([]byte{3})
	_ = cmd.Wait()

	got := seen()
	watching, finalSummary := strings.Count(got, "Watching for changes"), "●  1 passed  0 failed"
	lastLine := got[strings.LastIndex(got, finalSummary):]
	if watching < 2 || strings.Count(got, finalSummary) != 3 || strings.Contains(strings.SplitN(lastLine, "\n", 2)[0], "iteration") {
		t.Errorf("pretty output:\n%s\nwant a watching line per iteration, two iteration blocks and one final summary without an iteration number", got)
	}
}
