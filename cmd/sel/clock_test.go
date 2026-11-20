package main

import (
	"bytes"
	"io"
	"os"
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
	c := &clock{stderr: &errOut, exit: func(code int) { exited <- code }, cleanup: func() {}}
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
	(&clock{start: time.Now(), stderr: &errOut, exit: func(code int) { exited <- code }, cleanup: func() {}}).attach(root, args)
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

func TestExpireOnMergedStreamsWritesTheErrorTextOnlyAtALineBoundary(t *testing.T) {
	object := "{\"v\":1,\"error\":\"timeout\",\"message\":\"--timeout 1s expired\"}\n"
	cases := []struct {
		name  string
		setup func(t *testing.T, g *guard)
		want  string
	}{
		{"after a complete line", func(t *testing.T, g *guard) { _, _ = g.Write([]byte("{\"v\":1}\n")) }, "{\"v\":1}\n" + object + "Error: --timeout 1s expired\n"},
		{"after a partial line", func(t *testing.T, g *guard) { _, _ = g.Write([]byte("{\"v\":1,\"pa")) }, "{\"v\":1,\"pa"},
		{"while the commands own write is in flight", func(t *testing.T, g *guard) {
			g.mu.Lock()
			t.Cleanup(g.mu.Unlock)
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			g := &guard{w: w}
			tc.setup(t, g)
			exited := make(chan int, 1)
			c := &clock{stderr: w, exit: func(code int) { exited <- code }, cleanup: func() {}}

			c.expire(g, true, contract.Errorf(contract.Timeout, "--timeout 1s expired"))
			_ = w.Close()
			got, _ := io.ReadAll(r)

			if string(got) != tc.want || <-exited != 8 {
				t.Errorf("the shared pipe got %q, want %q and exit 8", got, tc.want)
			}
		})
	}
}

func TestExpireCleansUpBeforeAnyWriteAndStillExitsWhenStderrNeverDrains(t *testing.T) {
	cleaned := make(chan struct{})
	stderr := &stuckWriter{entered: make(chan struct{}, 4), release: make(chan struct{})}
	t.Cleanup(func() { close(stderr.release) })
	exited := make(chan int, 1)
	var out bytes.Buffer
	var stdoutSawCleanup bool
	g := &guard{w: writerFunc(func(p []byte) (int, error) {
		select {
		case <-cleaned:
			stdoutSawCleanup = true
		default:
		}
		return out.Write(p)
	})}
	c := &clock{stderr: stderr, exit: func(code int) { exited <- code }, cleanup: func() { close(cleaned) }}

	go c.expire(g, true, contract.Errorf(contract.Timeout, "--timeout 1s expired"))

	select {
	case code := <-exited:
		if code != 8 || !stdoutSawCleanup {
			t.Errorf("exit %d, cleanup before the stdout write %t; want exit 8 after cleanup", code, stdoutSawCleanup)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("expire never exited: a stderr that does not drain held it up")
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestFinishNeverReturnsOnceTheFallbackHasClaimedTheTerminalObject(t *testing.T) {
	c := &clock{timer: time.NewTimer(time.Hour)}
	c.terminal.Store(true)
	returned := make(chan struct{})

	go func() {
		c.finish()
		close(returned)
	}()

	select {
	case <-returned:
		t.Error("finish returned after the fallback claimed the terminal object, so the command's own exit would race it")
	case <-time.After(200 * time.Millisecond):
	}
}
