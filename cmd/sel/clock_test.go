package main

import (
	"bytes"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/spf13/cobra"
)

type stuckWriter struct {
	writes  atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (w *stuckWriter) Write(p []byte) (int, error) {
	w.writes.Add(1)
	w.entered <- struct{}{}
	<-w.release
	return len(p), nil
}

func expireWith(t *testing.T, g *guard) (stderr string, code int) {
	t.Helper()
	var errOut bytes.Buffer
	exited := make(chan int, 2)
	c := &clock{stderr: &errOut, exit: func(code int) { exited <- code }}
	cause := contract.Errorf(contract.Timeout, "--timeout 1s expired")
	c.expire(g, true, cause)
	c.expire(g, true, cause)
	if len(exited) != 1 {
		t.Fatalf("exit called %d times, want exactly once", len(exited))
	}
	return errOut.String(), <-exited
}

func TestExpireEndsStdoutWithOneTimeoutObjectAfterACompleteLine(t *testing.T) {
	var out bytes.Buffer
	g := &guard{w: &out}
	_, _ = g.Write([]byte("{\"v\":1,\"event\":\"progress\"}\n"))

	stderr, code := expireWith(t, g)
	_, _ = g.Write([]byte("late\n"))

	want := "{\"v\":1,\"event\":\"progress\"}\n{\"v\":1,\"error\":\"timeout\",\"message\":\"--timeout 1s expired\"}\n"
	if out.String() != want || code != 8 || stderr != "Error: --timeout 1s expired\n" {
		t.Errorf("stdout %q, exit %d, stderr %q; want one timeout object last, later writes dropped, exit 8", out.String(), code, stderr)
	}
}

func TestExpireLeavesStdoutAloneAfterAPartialLine(t *testing.T) {
	var out bytes.Buffer
	g := &guard{w: &out}
	_, _ = g.Write([]byte(`{"v":1,"event":"prog`))

	stderr, code := expireWith(t, g)

	if out.String() != `{"v":1,"event":"prog` || code != 8 || !strings.HasPrefix(stderr, "Error: --timeout 1s expired") {
		t.Errorf("stdout %q, exit %d, stderr %q; want no object glued to the partial line", out.String(), code, stderr)
	}
}

func TestFallbackWritesNothingToStdoutWhileTheCommandsOwnWriteIsInFlight(t *testing.T) {
	w := &stuckWriter{entered: make(chan struct{}, 4), release: make(chan struct{})}
	t.Cleanup(func() { close(w.release) })
	var errOut bytes.Buffer
	exited := make(chan int, 1)
	args := []string{"x", "--json", "--timeout=50ms"}
	root := &cobra.Command{Use: "sel"}
	root.AddCommand(&cobra.Command{Use: "x", RunE: func(cmd *cobra.Command, _ []string) error {
		_, err := cmd.OutOrStdout().Write([]byte(`{"v":1,"pad":"` + strings.Repeat("x", 20000)))
		return err
	}})
	root.PersistentFlags().Bool("json", false, "")
	root.Commands()[0].Flags().Duration("timeout", 0, "")
	root.SetOut(w)
	root.SetArgs(args)
	(&clock{start: time.Now(), stderr: &errOut, exit: func(code int) { exited <- code }}).attach(root, args)
	go func() { _ = root.Execute() }()

	code := <-exited

	if w.writes.Load() != 1 || code != 8 || !strings.HasPrefix(errOut.String(), "Error: --timeout 50ms expired") {
		t.Errorf("%d writes reached stdout, exit %d, stderr %q; want only the command's in-flight write, exit 8 and the message on stderr", w.writes.Load(), code, errOut.String())
	}
}

func TestTailWritesThroughTheCommandsOutputWhenItHasATimeout(t *testing.T) {
	srv := tailServer{frames: []string{frameStart, summaryPass}}.start(t)
	t.Setenv("SELECTIVE_HOST", srv.URL)
	t.Setenv("SELECTIVE_TOKEN", "tok")
	t.Setenv("SELECTIVE_CONFIG_DIR", t.TempDir())
	var out, errOut bytes.Buffer

	_, code := runCommand(newRoot(), []string{"tail", "--run-id", "r1", "--json", "--timeout", "30s"}, &out, &errOut)

	if code != 0 || !strings.Contains(out.String(), `"event":"summary"`) {
		t.Errorf("exit %d, stdout %q, stderr %q; want the stream on the command's own output", code, out.String(), errOut.String())
	}
}

func TestEveryCommandWithATimeoutFlagHasARunEForTheClockToWrap(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.LocalFlags().Lookup("timeout") != nil && c.RunE == nil {
			t.Errorf("%q has a --timeout flag but no RunE, so the process clock never reaches it", c.CommandPath())
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(newRoot())
}
