package main

import (
	"io"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

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

func TestNoTimeoutFlagMeansNoDeadlineOnTheCommandContext(t *testing.T) {
	const explicit = 5 * time.Minute
	cases := []struct {
		name     string
		args     []string
		wantWait time.Duration
	}{
		{name: "sel test", args: []string{"test"}},
		{name: "sel doctor", args: []string{"doctor"}},
		{name: "sel test --watch", args: []string{"test", "--watch"}},
		{name: "sel tail", args: []string{"tail"}},
		{name: "sel test --timeout", args: []string{"test", "--timeout", explicit.String()}, wantWait: explicit},
		{name: "sel doctor --timeout", args: []string{"doctor", "--timeout", explicit.String()}, wantWait: explicit},
		{name: "sel tail --timeout", args: []string{"tail", "--timeout", explicit.String()}, wantWait: explicit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := newRoot()
			target, _, err := root.Find(tc.args[:1])
			if err != nil {
				t.Fatal(err)
			}
			var deadline time.Time
			var hasDeadline, ran bool
			target.RunE = func(cmd *cobra.Command, _ []string) error {
				ran = true
				deadline, hasDeadline = cmd.Context().Deadline()
				return nil
			}
			target.PreRunE, target.PreRun = nil, nil
			target.Args = cobra.ArbitraryArgs
			began := time.Now()
			if _, code := runCommand(root, tc.args, io.Discard, io.Discard); code != 0 {
				t.Fatalf("%v exited %d", tc.args, code)
			}
			if !ran {
				t.Fatalf("%v never reached RunE", tc.args)
			}
			if tc.wantWait == 0 {
				if hasDeadline {
					t.Fatalf("%v: cmd.Context() has a deadline %s after start, but --timeout was not given", tc.args, deadline.Sub(began).Round(time.Second))
				}
				return
			}
			if !hasDeadline {
				t.Fatalf("%v: cmd.Context() has no deadline although --timeout was given", tc.args)
			}
			if got := deadline.Sub(began); got < tc.wantWait-time.Second || got > tc.wantWait+time.Second {
				t.Fatalf("%v: deadline %s after start, want about %s", tc.args, got, tc.wantWait)
			}
		})
	}
}
