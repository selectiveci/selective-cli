package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func deadlineSeenBy(t *testing.T, args ...string) (before, after, deadline time.Time, has bool) {
	t.Helper()
	root := newRoot()
	target, _, err := root.Find(args[:1])
	if err != nil {
		t.Fatal(err)
	}
	ran := false
	target.RunE = func(cmd *cobra.Command, _ []string) error {
		ran = true
		deadline, has = cmd.Context().Deadline()
		return nil
	}
	target.PreRunE, target.PreRun = nil, nil
	target.Args = cobra.ArbitraryArgs
	before = time.Now()
	_, code := runCommand(root, args, new(bytes.Buffer), new(bytes.Buffer))
	after = time.Now()
	if code != 0 || !ran {
		t.Fatalf("%v: exit %d, reached RunE: %v", args, code, ran)
	}
	return before, after, deadline, has
}

func TestTimeoutExclusionsOnTheCommandContext(t *testing.T) {
	cases := []struct {
		args []string
		want time.Duration
	}{
		{[]string{"test", "--watch", "--timeout", "5m"}, 0},
		{[]string{"test", "--timeout=30m"}, 30 * time.Minute},
		{[]string{"doctor", "--timeout=30s"}, 30 * time.Second},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			before, after, deadline, has := deadlineSeenBy(t, tc.args...)
			switch {
			case tc.want == 0 && has:
				t.Errorf("cmd.Context() has a deadline %s after start, want none under --watch", deadline.Sub(before))
			case tc.want > 0 && !has:
				t.Errorf("cmd.Context() has no deadline although --timeout=%s was given explicitly", tc.want)
			case tc.want > 0 && (deadline.Before(before.Add(tc.want)) || deadline.After(after.Add(tc.want))):
				t.Errorf("deadline %s after start, want %s", deadline.Sub(before), tc.want)
			}
		})
	}
}

func neverConnectingProject(t *testing.T) {
	t.Helper()
	api := newFakeAPI(t, selected)
	runnerEnv(t)
	t.Setenv("FAKE_NEVER_CONNECT", "1")
	t.Setenv("SELECTIVE_HOST", api.srv.URL)
	t.Setenv("SELECTIVE_TOKEN", "tok")
	t.Setenv("SELECTIVE_CONFIG_DIR", t.TempDir())
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(pytestProject(t)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

func TestSelTestWithoutTheFlagCapsEachRunButNotTheWholeCommand(t *testing.T) {
	neverConnectingProject(t)

	root := newRoot()
	target, _, _ := root.Find([]string{"test"})
	flag := target.Flags().Lookup("timeout")
	if err := flag.Value.Set("1s"); err != nil || flag.Changed {
		t.Fatalf("shortening the default: err %v, Changed %v; want the flag still unchanged", err, flag.Changed)
	}
	run := target.RunE
	var wholeCommand time.Time
	var hasWholeCommand bool
	target.RunE = func(cmd *cobra.Command, args []string) error {
		wholeCommand, hasWholeCommand = cmd.Context().Deadline()
		return run(cmd, args)
	}
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		_, code := runCommand(root, []string{"test", "--org", "acme", "--json"}, &stdout, &stderr)
		done <- code
	}()
	select {
	case code := <-done:
		var last map[string]any
		lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
		_ = json.Unmarshal([]byte(lines[len(lines)-1]), &last)
		message, _ := last["message"].(string)
		if code != 8 || !strings.Contains(message, "the test run did not finish within 1s") {
			t.Errorf("exit %d, last line %v; want exit 8 from the per-run cap", code, last)
		}
		if hasWholeCommand {
			t.Errorf("cmd.Context() has the deadline %s without --timeout", wholeCommand)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("no per-run cap: the run was still waiting 20s after its 1s cap")
	}
}

func TestSelTestWatchWithATimeoutCapsEachRun(t *testing.T) {
	neverConnectingProject(t)
	done := make(chan int, 1)
	var stdout, stderr bytes.Buffer
	go func() {
		_, code := runCommand(newRoot(), []string{"test", "--org", "acme", "--json", "--watch", "--timeout", "1s"}, &stdout, &stderr)
		done <- code
	}()
	select {
	case code := <-done:
		if code != 8 {
			t.Errorf("exit %d, want 8 from the per-run cap\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no per-run cap under --watch: still running 10s after a 1s --timeout")
	}
}

func TestSelTestTimeoutDefaultIsThirtyMinutes(t *testing.T) {
	target, _, _ := newRoot().Find([]string{"test"})
	if got := target.Flags().Lookup("timeout").DefValue; got != "30m0s" {
		t.Errorf("--timeout default = %s, want 30m0s", got)
	}
}

func TestSelTestTimeoutHelpSaysWhichClockItIs(t *testing.T) {
	const wantEach, wantWhole, wantWatch = "each run is capped", "caps the whole command", "the watch itself has no limit"
	_, helpOut, _ := execute("test", "--help")
	_, jsonOut, _ := execute("help", "--json")
	var doc struct {
		Help struct {
			Commands []struct {
				Path  string `json:"path"`
				Flags []struct{ Name, Usage string }
			}
		}
	}
	if err := json.Unmarshal([]byte(jsonOut), &doc); err != nil {
		t.Fatal(err)
	}
	usage := ""
	for _, c := range doc.Help.Commands {
		for _, f := range c.Flags {
			if c.Path == "test" && f.Name == "timeout" {
				usage = f.Usage
			}
		}
	}
	for name, text := range map[string]string{"sel test --help": helpOut, "sel help --json": usage} {
		if !strings.Contains(text, wantEach) || !strings.Contains(text, wantWhole) || !strings.Contains(text, wantWatch) {
			t.Errorf("%s does not say %q, %q and %q about --timeout\n%s", name, wantEach, wantWhole, wantWatch, text)
		}
	}
}
