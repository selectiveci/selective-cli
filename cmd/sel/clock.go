package main

import (
	"context"
	"time"

	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/spf13/cobra"
)

type clock struct {
	start time.Time
}

func (c *clock) attach(cmd *cobra.Command) {
	if run := cmd.RunE; run != nil {
		cmd.RunE = func(cmd *cobra.Command, positional []string) error {
			if d, ok := cappedTimeout(cmd); ok {
				cause := contract.Errorf(contract.Timeout, "--timeout %s expired", d)
				ctx, cancel := context.WithDeadlineCause(cmd.Context(), c.start.Add(d), cause)
				defer cancel()
				cmd.SetContext(ctx)
			}
			return run(cmd, positional)
		}
	}
	for _, sub := range cmd.Commands() {
		c.attach(sub)
	}
}

func cappedTimeout(cmd *cobra.Command) (time.Duration, bool) {
	if watch, _ := cmd.Flags().GetBool("watch"); watch {
		return 0, false
	}
	d, err := cmd.Flags().GetDuration("timeout")
	return d, err == nil && d > 0 && cmd.Flags().Changed("timeout")
}

func newClock() *clock {
	return &clock{start: time.Now()}
}

func expiredOr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	return err
}
