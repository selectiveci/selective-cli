package main

import (
	"bytes"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/spf13/cobra"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type stuckWriter struct{}

func (stuckWriter) Write([]byte) (int, error) { select {} }

type wedgedWriter struct{ w io.Writer }

func (w wedgedWriter) Write(p []byte) (int, error) {
	_, _ = w.w.Write(p)
	select {}
}

func expiredClock(stdout, stderr io.Writer, asJSON bool) (*clock, *guard, chan int) {
	exited := make(chan int, 1)
	c := &clock{start: time.Now(), stdout: stdout, stderr: stderr, exit: func(code int) { exited <- code }}
	g := &guard{w: stdout}
	c.arm(time.Now(), contract.Errorf(contract.Timeout, "--timeout 0s expired"), g, asJSON)
	return c, g, exited
}

func awaitExit(t *testing.T, exited chan int) (code int, took time.Duration) {
	t.Helper()
	began := time.Now()
	select {
	case code = <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the backstop never exited")
	}
	return code, time.Since(began)
}

func TestBackstopExitsEightWithOneTimeoutObjectAndAbandonsACleanupThatNeverReturns(t *testing.T) {
	var stdout, stderr lockedBuffer
	c, _, exited := expiredClock(&stdout, &stderr, true)
	cleaned := make(chan struct{})
	c.extend(0, []func(){func() {
		close(cleaned)
		select {}
	}})

	code, took := awaitExit(t, exited)

	select {
	case <-cleaned:
	default:
		t.Error("the registered cleanup never ran")
	}
	if lines := strings.Split(strings.TrimSpace(stdout.String()), "\n"); code != 8 || len(lines) != 1 || !strings.Contains(lines[0], `"error":"timeout"`) {
		t.Errorf("exit %d, stdout %q; want exit 8 and exactly one timeout object", code, stdout.String())
	}
	if !strings.HasPrefix(stderr.String(), "Error: --timeout") || took > backstopGrace+backstopExit+250*time.Millisecond {
		t.Errorf("stderr %q, exit after %s; want an Error: line and exit by the bound", stderr.String(), took)
	}
}

func TestBackstopWritesNoObjectWhenTheOwnPathOrAnotherWriteHoldsStdout(t *testing.T) {
	cases := map[string]func(c *clock, g *guard, stdout *lockedBuffer){
		"the own path claimed the terminal object": func(c *clock, _ *guard, _ *lockedBuffer) { c.claim() },
		"a line is half written": func(_ *clock, g *guard, _ *lockedBuffer) {
			_, _ = g.Write([]byte(`{"v":1,"event":"pro`))
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr lockedBuffer
			c := &clock{start: time.Now(), stdout: &stdout, stderr: &stderr}
			exited := make(chan int, 1)
			c.exit = func(code int) { exited <- code }
			g := &guard{w: &stdout}
			setup(c, g, &stdout)
			before := stdout.String()
			c.arm(time.Now(), contract.Errorf(contract.Timeout, "--timeout 0s expired"), g, true)

			code, _ := awaitExit(t, exited)

			if code != 8 || stdout.String() != before || !strings.HasPrefix(stderr.String(), "Error: ") {
				t.Errorf("exit %d, stdout %q (was %q), stderr %q; want exit 8, no new stdout bytes and an Error: line", code, stdout.String(), before, stderr.String())
			}
		})
	}
}

func TestBackstopNeverBlocksOnABackedUpStdoutAndStderr(t *testing.T) {
	c, g, exited := expiredClock(stuckWriter{}, stuckWriter{}, true)
	go func() { _, _ = g.Write([]byte("{}\n")) }()
	time.Sleep(50 * time.Millisecond)
	c.extend(0, nil)

	code, took := awaitExit(t, exited)

	if code != 8 || took > backstopGrace+backstopExit+250*time.Millisecond {
		t.Errorf("exit %d after %s; want exit 8 by the bound with both streams stuck", code, took)
	}
}

func TestBackstopLeavesTheCommandsOwnErrorAndExitCodeAloneOnceItClaimedThem(t *testing.T) {
	for i := range 20 {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			t.Parallel()
			var stdout, stderr lockedBuffer
			c, g, exited := expiredClock(&stdout, &stderr, true)
			c.claim()
			fired := make(chan struct{})
			c.extend(0, []func(){func() { close(fired) }})
			returned := make(chan struct{})
			go func() {
				<-fired
				_, _ = g.Write([]byte(`{"error":"not_found"}` + "\n"))
				c.finish()
				close(returned)
			}()

			select {
			case <-returned:
			case code := <-exited:
				t.Fatalf("hard exit %d, stdout %q: it took the command's own error and exit code", code, stdout.String())
			case <-time.After(5 * time.Second):
				t.Fatal("neither the command nor the backstop finished")
			}

			if stdout.String() != `{"error":"not_found"}`+"\n" || stderr.String() != "" {
				t.Errorf("stdout %q, stderr %q; want the command's object alone", stdout.String(), stderr.String())
			}
		})
	}
}

func TestBackstopExitsByTheBoundWhenTheClaimedCommandIsWedgedMidLineOnStdout(t *testing.T) {
	const partial = `{"error":"not_fo`
	cases := map[string]bool{"stderr apart from stdout": false, "stderr on stdout's file": true}
	for name, shared := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stdout, stderr := new(lockedBuffer), new(lockedBuffer)
			var out, errOut io.Writer = stdout, stderr
			seen := stdout.String
			if shared {
				f, err := os.CreateTemp(t.TempDir(), "stdout")
				if err != nil {
					t.Fatal(err)
				}
				out, errOut = f, f
				seen = func() string { body, _ := os.ReadFile(f.Name()); return string(body) }
			}
			exited := make(chan int, 1)
			c := &clock{start: time.Now(), stdout: out, stderr: errOut, exit: func(code int) { exited <- code }}
			g := &guard{w: wedgedWriter{out}}
			c.arm(time.Now(), contract.Errorf(contract.Timeout, "--timeout 0s expired"), g, true)
			c.claim()
			go func() { _, _ = g.Write([]byte(partial)) }()

			code, took := awaitExit(t, exited)

			if gotError := strings.HasPrefix(stderr.String(), "Error: "); code != 8 || took > backstopGrace+backstopExit+250*time.Millisecond || seen() != partial || gotError == shared {
				t.Errorf("exit %d after %s, stdout %q, stderr %q; want exit 8 by the bound, the command's bytes alone on stdout, and an Error: line only when stderr is not stdout's file", code, took, seen(), stderr.String())
			}
		})
	}
}

func TestAfterTheBackstopFiresTheCommandPathParksInsteadOfWriting(t *testing.T) {
	for name, call := range map[string]func(*clock){"claim": (*clock).claim, "finish": (*clock).finish} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c, _, exited := expiredClock(io.Discard, io.Discard, false)
			awaitExit(t, exited)
			returned := make(chan struct{})
			go func() {
				call(c)
				close(returned)
			}()

			select {
			case <-returned:
				t.Errorf("%s returned after the hard exit had fired: the command could write after it", name)
			case <-time.After(200 * time.Millisecond):
			}
		})
	}
}

func TestBackstopIsArmedOnlyForAnExplicitTimeoutOutsideWatch(t *testing.T) {
	cases := map[string]struct {
		args  []string
		armed bool
	}{
		"no --timeout":               {[]string{"doctor"}, false},
		"sel test without --timeout": {[]string{"test"}, false},
		"sel test --watch":           {[]string{"test", "--watch", "--timeout", "5m"}, false},
		"sel doctor --timeout":       {[]string{"doctor", "--timeout", "5m"}, true},
		"sel test --timeout":         {[]string{"test", "--timeout", "5m"}, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := newRoot()
			target, _, err := root.Find(tc.args[:1])
			if err != nil {
				t.Fatal(err)
			}
			var armed bool
			target.RunE = func(cmd *cobra.Command, _ []string) error {
				_, armed = cmd.Context().Value(backstopKey{}).(*clock)
				return nil
			}
			target.PreRunE, target.PreRun, target.Args = nil, nil, cobra.ArbitraryArgs

			if _, code := runCommand(root, tc.args, io.Discard, io.Discard); code != 0 || armed != tc.armed {
				t.Errorf("%v: exit %d, armed=%t; want armed=%t", tc.args, code, armed, tc.armed)
			}
		})
	}
}

func TestOnlyCommandsThatWriteThroughTheGuardCanBeArmedByATimeoutFlag(t *testing.T) {
	var got []string
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Flags().Lookup("timeout") != nil {
			got = append(got, c.CommandPath())
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(newRoot())
	slices.Sort(got)

	if want := []string{"sel approvals show", "sel doctor", "sel suites delete", "sel tail", "sel test"}; !slices.Equal(got, want) {
		t.Errorf("commands with --timeout: %v; want %v, each audited to write only through cmd.OutOrStdout() (setup.Env.Stdout and auth's os.Stdout bypass the guard)", got, want)
	}
}
