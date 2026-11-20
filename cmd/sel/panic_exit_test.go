package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

const panicChildEnv = "SEL_TEST_PANIC_CHILD_ARGS"

func TestPanicChildProcess(t *testing.T) {
	args, ok := os.LookupEnv(panicChildEnv)
	if !ok {
		t.Skip("runs only as the child of the panic exit tests")
	}
	boom := &cobra.Command{
		Use: "boom",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if partial, _ := cmd.Flags().GetBool("partial"); partial {
				fmt.Fprintln(cmd.OutOrStdout(), `{"v":1,"event":"first"}`)
				fmt.Fprintln(cmd.OutOrStdout(), `{"v":1,"event":"second"}`)
			}
			var none []int
			_ = none[len(args)]
			return nil
		},
	}
	boom.Flags().String("format", "", "")
	boom.Flags().Bool("partial", false, "")
	root := newRoot()
	root.AddCommand(boom)
	os.Exit(run(root, strings.Fields(args), os.Stdout, os.Stderr))
}

func runPanicChild(t *testing.T, args string) (int, []string, string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPanicChildProcess$")
	cmd.Env = append(os.Environ(), panicChildEnv+"="+args)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	var exit *exec.ExitError
	if err := cmd.Run(); err != nil && !errors.As(err, &exit) {
		t.Fatal(err)
	}
	code := cmd.ProcessState.ExitCode()
	if code != 9 {
		t.Fatalf("exit %d, want 9\nstdout: %q\nstderr: %q", code, stdout.String(), stderr.String())
	}
	var lines []string
	if out := strings.TrimSpace(stdout.String()); out != "" {
		lines = strings.Split(out, "\n")
	}
	return code, lines, stderr.String()
}

func requireStack(t *testing.T, stderr string) {
	t.Helper()
	if !strings.Contains(stderr, "index out of range") || !strings.Contains(stderr, "goroutine") {
		t.Errorf("stderr lacks the panic value and stack: %q", stderr)
	}
}

func TestPanicInJSONModeEndsStdoutWithAnInternalErrorObject(t *testing.T) {
	for name, args := range map[string]string{"nothing printed first": "boom --json", "after partial NDJSON": "boom --json --partial"} {
		t.Run(name, func(t *testing.T) {
			_, lines, stderr := runPanicChild(t, args)
			if len(lines) == 0 {
				t.Fatal("stdout is empty, want the error object")
			}
			for _, line := range lines {
				var object map[string]any
				if err := json.Unmarshal([]byte(line), &object); err != nil || object["v"] != float64(1) {
					t.Errorf("stdout line %q is not a v1 JSON object: %v", line, err)
				}
			}
			var last map[string]any
			_ = json.Unmarshal([]byte(lines[len(lines)-1]), &last)
			if last["error"] != "internal_error" {
				t.Errorf("last stdout line %q, want error internal_error", lines[len(lines)-1])
			}
			if joined := strings.Join(lines, "\n"); strings.Contains(joined, "index out of range") || strings.Contains(joined, "goroutine") {
				t.Errorf("stdout carries the panic value or stack: %q", joined)
			}
			requireStack(t, stderr)
		})
	}
}

func TestPanicInPrettyModeLeavesStdoutEmptyAndReportsOnStderr(t *testing.T) {
	_, lines, stderr := runPanicChild(t, "boom --format pretty")
	if len(lines) != 0 {
		t.Errorf("stdout %q, want empty", lines)
	}
	if !strings.Contains(stderr, "Error: unexpected internal error") {
		t.Errorf("stderr lacks the Error: line: %q", stderr)
	}
	requireStack(t, stderr)
}
