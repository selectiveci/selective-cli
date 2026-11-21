package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/spf13/cobra"
)

const (
	backstopGrace = 250 * time.Millisecond
	backstopExit  = 250 * time.Millisecond
)

var hardExit = os.Exit

type backstopKey struct{}

type guard struct {
	mu   sync.Mutex
	w    io.Writer
	last byte
}

func (g *guard) Write(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	n, err := g.w.Write(p)
	if n > 0 {
		g.last = p[n-1]
	}
	return n, err
}

func (g *guard) Unwrap() io.Writer { return g.w }

type clock struct {
	start  time.Time
	root   *cobra.Command
	args   []string
	stdout io.Writer
	stderr io.Writer
	exit   func(int)

	mu       sync.Mutex
	timer    *time.Timer
	deadline time.Time
	windDown time.Duration
	cause    contract.Coded
	guard    *guard
	asJSON   bool
	cleanups []func()
	claimed  bool
	fired    bool
	done     bool
}

func newClock(root *cobra.Command, args []string, stdout, stderr io.Writer) *clock {
	return &clock{start: time.Now(), root: root, args: args, stdout: stdout, stderr: stderr, exit: hardExit}
}

func (c *clock) attach(cmd *cobra.Command) {
	if run := cmd.RunE; run != nil {
		cmd.RunE = func(cmd *cobra.Command, positional []string) error {
			if d, ok := cappedTimeout(cmd); ok {
				cause := contract.Errorf(contract.Timeout, "--timeout %s expired", d)
				deadline := c.start.Add(d)
				ctx, cancel := context.WithDeadlineCause(cmd.Context(), deadline, cause)
				defer cancel()
				g := &guard{w: cmd.OutOrStdout()}
				cmd.SetOut(g)
				asJSON := errorObjectOnStdout(cmd, c.args, c.stdout) && !secretFlagGivenFor(c.root, cmd, c.args)
				c.arm(deadline, cause, g, asJSON)
				cmd.SetContext(context.WithValue(ctx, backstopKey{}, c))
			}
			return run(cmd, positional)
		}
	}
	for _, sub := range cmd.Commands() {
		c.attach(sub)
	}
}

func (c *clock) arm(deadline time.Time, cause contract.Coded, g *guard, asJSON bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deadline, c.cause, c.guard, c.asJSON = deadline, cause, g, asJSON
	c.timer = time.AfterFunc(c.untilFire(), c.fire)
}

func (c *clock) untilFire() time.Duration {
	return time.Until(c.deadline.Add(c.windDown + backstopGrace))
}

func holdBackstop(ctx context.Context, windDown time.Duration, cleanups ...func()) {
	if c, ok := ctx.Value(backstopKey{}).(*clock); ok {
		c.extend(windDown, cleanups)
	}
}

func (c *clock) extend(windDown time.Duration, cleanups []func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fired || c.done {
		return
	}
	c.windDown = max(c.windDown, windDown)
	c.cleanups = append(c.cleanups, cleanups...)
	c.timer.Reset(c.untilFire())
}

func (c *clock) output(stdout io.Writer) io.Writer {
	if c.guard != nil {
		return c.guard
	}
	return stdout
}

func (c *clock) claim() {
	c.mu.Lock()
	if c.fired {
		c.mu.Unlock()
		select {}
	}
	c.claimed = true
	c.mu.Unlock()
}

func (c *clock) finish() {
	c.mu.Lock()
	c.done = true
	fired := c.fired
	if c.timer != nil {
		c.timer.Stop()
	}
	c.mu.Unlock()
	if fired {
		select {}
	}
}

func (c *clock) fire() {
	c.mu.Lock()
	if c.fired || c.done {
		c.mu.Unlock()
		return
	}
	if wait := c.untilFire(); wait > 0 {
		c.timer.Reset(wait)
		c.mu.Unlock()
		return
	}
	c.fired = true
	claimed, cleanups := c.claimed, c.cleanups
	c.mu.Unlock()

	clean := c.guard.mu.TryLock() && (c.guard.last == 0 || c.guard.last == '\n')
	var wg sync.WaitGroup
	run := func(f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f()
		}()
	}
	if clean && c.asJSON && !claimed {
		run(func() { _ = format.Error(c.guard.w, c.cause) })
	}
	if clean || !sameFile(c.stdout, c.stderr) {
		run(func() { fmt.Fprintf(c.stderr, "Error: %s\n", c.cause.Error()) })
	}
	for _, cleanup := range cleanups {
		run(cleanup)
	}
	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(backstopExit):
	}
	c.exit(contract.ExitTimeout)
}

func sameFile(a, b io.Writer) bool {
	fa, okA := format.Unwrapped(a).(*os.File)
	fb, okB := format.Unwrapped(b).(*os.File)
	if !okA || !okB {
		return false
	}
	ia, errA := fa.Stat()
	ib, errB := fb.Stat()
	return errA == nil && errB == nil && os.SameFile(ia, ib)
}

func cappedTimeout(cmd *cobra.Command) (time.Duration, bool) {
	if watch, _ := cmd.Flags().GetBool("watch"); watch {
		return 0, false
	}
	d, err := cmd.Flags().GetDuration("timeout")
	return d, err == nil && d > 0 && cmd.Flags().Changed("timeout")
}

func expiredOr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	return err
}
