package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/spf13/cobra"
)

const (
	fallbackGrace     = 250 * time.Millisecond
	fallbackWriteWait = 100 * time.Millisecond
)

type guard struct {
	mu     sync.Mutex
	w      io.Writer
	last   byte
	closed bool
}

func (g *guard) Write(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return len(p), nil
	}
	n, err := g.w.Write(p)
	if n > 0 {
		g.last = p[n-1]
	}
	return n, err
}

func (g *guard) Unwrap() io.Writer { return g.w }

type clock struct {
	start    time.Time
	stderr   io.Writer
	exit     func(int)
	timer    *time.Timer
	terminal atomic.Bool
}

func (c *clock) attach(cmd *cobra.Command, args []string) {
	if run := cmd.RunE; run != nil {
		cmd.RunE = func(cmd *cobra.Command, positional []string) error {
			if d, ok := cappedTimeout(cmd); ok {
				cause := contract.Errorf(contract.Timeout, "--timeout %s expired", d)
				deadline := c.start.Add(d)
				ctx, cancel := context.WithDeadlineCause(cmd.Context(), deadline, cause)
				defer cancel()
				cmd.SetContext(ctx)
				g := &guard{w: cmd.OutOrStdout()}
				cmd.SetOut(g)
				asJSON := errorObjectOnStdout(cmd, args, g)
				c.timer = time.AfterFunc(time.Until(deadline)+fallbackGrace, func() { c.expire(g, asJSON, cause) })
			}
			return run(cmd, positional)
		}
	}
	for _, sub := range cmd.Commands() {
		c.attach(sub, args)
	}
}

func cappedTimeout(cmd *cobra.Command) (time.Duration, bool) {
	if watch, _ := cmd.Flags().GetBool("watch"); watch {
		return 0, false
	}
	d, err := cmd.Flags().GetDuration("timeout")
	return d, err == nil && d > 0
}

func (c *clock) finish() {
	if c.timer == nil {
		return
	}
	c.timer.Stop()
	if c.terminal.Swap(true) {
		select {}
	}
}

func (c *clock) expire(g *guard, asJSON bool, cause contract.Coded) {
	if c.terminal.Swap(true) {
		return
	}
	if asJSON && g.mu.TryLock() {
		if g.last == 0 || g.last == '\n' {
			written := make(chan struct{})
			go func() {
				_ = format.Error(g.w, cause)
				close(written)
			}()
			select {
			case <-written:
			case <-time.After(fallbackWriteWait):
			}
		}
		g.closed = true
		g.mu.Unlock()
	}
	fmt.Fprintf(c.stderr, "Error: %s\n", cause.Error())
	c.exit(contract.ExitTimeout)
}

func newClock(stderr io.Writer) *clock {
	return &clock{start: time.Now(), stderr: stderr, exit: os.Exit}
}

func expiredOr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	return err
}
