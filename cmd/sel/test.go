package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/selectiveci/selective-cli/internal/localrun"
	"github.com/spf13/cobra"
)

func transportCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "__transport",
		Hidden:             true,
		DisableFlagParsing: true,
		Annotations:        map[string]string{"mcp": "skip"},
		Run: func(_ *cobra.Command, args []string) {
			if len(args) == 1 && args[0] == "--stdio" {
				signal.Ignore(syscall.SIGINT, syscall.SIGTERM)
			}
			os.Exit(localrun.RunSeat(args, os.Stdin, os.Stdout, os.Stderr))
		},
	}
}
