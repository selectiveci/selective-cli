package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/spf13/cobra"
)

func cappedRoot(annotations map[string]string, run func(cmd *cobra.Command) error) *cobra.Command {
	root := &cobra.Command{Use: "sel"}
	root.PersistentFlags().Bool("json", false, "")
	sub := &cobra.Command{Use: "slow", Annotations: annotations, RunE: func(cmd *cobra.Command, _ []string) error { return run(cmd) }}
	sub.Flags().Duration("timeout", 0, "")
	root.AddCommand(sub)
	root.SilenceErrors, root.SilenceUsage = true, true
	return root
}

func fastCap(t *testing.T) {
	t.Helper()
	was := timeoutCapGrace
	timeoutCapGrace = 50 * time.Millisecond
	abandonWas := abandonChildren
	abandonChildren = func() {}
	t.Cleanup(func() { timeoutCapGrace, abandonChildren = was, abandonWas })
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func runWithinFiveSeconds(t *testing.T, root *cobra.Command, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut lockedBuffer
	finished := make(chan int, 1)
	go func() {
		_, code := runCommand(root, args, &out, &errOut)
		finished <- code
	}()
	select {
	case code = <-finished:
		return code, out.String(), errOut.String()
	case <-time.After(5 * time.Second):
		t.Fatal("command still running after 5s: --timeout does not cap it")
		return 0, "", ""
	}
}

func blockForever(*cobra.Command) error {
	select {}
}

func TestTimeoutCapEndsACommandStuckInSetupWithTheTimeoutErrorObject(t *testing.T) {
	fastCap(t)

	code, stdout, stderr := runWithinFiveSeconds(t, cappedRoot(nil, blockForever), "slow", "--timeout=100ms", "--json")

	want := `{"v":1,"error":"timeout","message":"--timeout 100ms expired"}` + "\n"
	if code != contract.ExitTimeout || stdout != want || !strings.HasPrefix(stderr, "Error: ") {
		t.Errorf("exit %d stdout %q stderr %q, want exit 8, stdout %q and an Error: line on stderr", code, stdout, stderr, want)
	}
}

func TestTimeoutCapInPrettyModeLeavesStdoutEmptyAndPutsTheErrorOnStderr(t *testing.T) {
	fastCap(t)
	root := cappedRoot(nil, blockForever)
	root.Commands()[0].Flags().String("format", "", "")

	code, stdout, stderr := runWithinFiveSeconds(t, root, "slow", "--timeout=100ms", "--format=pretty")

	if code != contract.ExitTimeout || stdout != "" || stderr != "Error: --timeout 100ms expired\n" {
		t.Errorf("exit %d stdout %q stderr %q, want exit 8, empty stdout and one Error: line", code, stdout, stderr)
	}
}

func TestTimeoutCapKeepsEveryStreamedLineValidWithTheTimeoutObjectLast(t *testing.T) {
	fastCap(t)
	stream := func(cmd *cobra.Command) error {
		for i := 0; i < 3; i++ {
			fmt.Fprintf(cmd.OutOrStdout(), "{\"v\":1,\"event\":\"waiting\",\"elapsed_s\":%d}\n", i)
		}
		return blockForever(cmd)
	}

	code, stdout, _ := runWithinFiveSeconds(t, cappedRoot(nil, stream), "slow", "--timeout=100ms", "--json")

	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	for _, line := range lines {
		var doc map[string]any
		if err := json.Unmarshal([]byte(line), &doc); err != nil {
			t.Fatalf("line %q is not JSON: %v", line, err)
		}
	}
	if last := lines[len(lines)-1]; code != 8 || len(lines) != 4 || !strings.Contains(last, `"error":"timeout"`) {
		t.Errorf("exit %d, %d lines, last %q; want exit 8 and 3 events then the timeout object", code, len(lines), last)
	}
}

func TestTimeoutCapLeavesCommandsThatFinishInTimeOrSetNoTimeoutAlone(t *testing.T) {
	fastCap(t)
	slowButNoCap := func(*cobra.Command) error {
		time.Sleep(300 * time.Millisecond)
		return nil
	}
	finishesEarly := func(*cobra.Command) error { return contract.Errorf(contract.NotFound, "no such thing") }
	cases := map[string]struct {
		annotations map[string]string
		run         func(*cobra.Command) error
		args        []string
		code        int
	}{
		"no --timeout given":        {nil, slowButNoCap, []string{"slow"}, 0},
		"finished before the limit": {nil, finishesEarly, []string{"slow", "--timeout=5s"}, 3},
		"owns its own timeout":      {map[string]string{"timeout_cap": "own"}, slowButNoCap, []string{"slow", "--timeout=50ms"}, 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			code, _, _ := runWithinFiveSeconds(t, cappedRoot(tc.annotations, tc.run), tc.args...)
			if code != tc.code {
				t.Errorf("exit %d, want %d", code, tc.code)
			}
		})
	}
}

func TestTimeoutCapRepanicsOnTheCallerSoTheRecoveryInRunSeesIt(t *testing.T) {
	defer func() {
		if r := recover(); r != "boom" {
			t.Errorf("recovered %v, want the command's panic", r)
		}
	}()
	runCommand(cappedRoot(nil, func(*cobra.Command) error { panic("boom") }), []string{"slow", "--timeout=5s"}, &bytes.Buffer{}, &bytes.Buffer{})
}
