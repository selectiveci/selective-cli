package main

import (
	"testing"

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
