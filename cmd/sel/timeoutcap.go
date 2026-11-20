package main

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/selectiveci/selective-cli/internal/gitctx"
	"github.com/spf13/cobra"
)

var timeoutCapGrace = 500 * time.Millisecond

var abandonChildren = gitctx.Abandon

func capTimeout(c *cobra.Command, argv []string, stdout, stderr io.Writer) {
	if run := c.RunE; run != nil && c.Flags().Lookup("timeout") != nil {
		c.RunE = func(cmd *cobra.Command, args []string) error {
			limit, _ := cmd.Flags().GetDuration("timeout")
			if !cmd.Flags().Changed("timeout") || limit <= 0 || cmd.Annotations["timeout_cap"] == "own" {
				return abandonOnTimeout(run(cmd, args))
			}
			asJSON := errorObjectOnStdout(cmd, argv, stdout)
			finished, err := runWithin(limit+timeoutCapGrace, func() error { return run(cmd, args) })
			if finished {
				return abandonOnTimeout(err)
			}
			abandonChildren()
			expired := contract.Errorf(contract.Timeout, "--timeout %s expired", limit)
			stdout, stderr = silence(stdout), silence(stderr)
			if asJSON {
				_ = format.Error(stdout, expired)
			}
			fmt.Fprintf(stderr, "Error: %s\n", expired.Error())
			return exitCode(expired.ExitCode())
		}
	}
	for _, sub := range c.Commands() {
		capTimeout(sub, argv, stdout, stderr)
	}
}

func abandonOnTimeout(err error) error {
	var coded contract.Coded
	var exit exitCode
	if errors.As(err, &coded) && coded.ErrorCode() == contract.Timeout || errors.As(err, &exit) && int(exit) == contract.ExitTimeout {
		abandonChildren()
	}
	return err
}

type capped struct {
	err      error
	panicked any
	didPanic bool
}

func runWithin(limit time.Duration, run func() error) (bool, error) {
	done := make(chan capped, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- capped{panicked: r, didPanic: true}
			}
		}()
		done <- capped{err: run()}
	}()
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case r := <-done:
		if r.didPanic {
			panic(r.panicked)
		}
		return true, r.err
	case <-timer.C:
		return false, nil
	}
}
