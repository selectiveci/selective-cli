package main

import (
	"fmt"
	"strings"

	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/spf13/cobra"
)

func explicitJSON(cmd *cobra.Command) bool {
	f := cmd.Flags().Lookup("json")
	return f != nil && f.Changed && f.Value.String() == "true"
}

func helpOnlyGroup(root *cobra.Command, args []string) *cobra.Command {
	if !jsonFlagGiven(args) {
		return nil
	}
	cmd, rest, err := root.Find(args)
	if err != nil || cmd.Runnable() || !cmd.HasAvailableSubCommands() || cmd.ParseFlags(rest) != nil {
		return nil
	}
	if cmd.Flags().Changed("help") || cmd.Flags().NArg() > 0 || !explicitJSON(cmd) {
		return nil
	}
	return cmd
}

func showGroup(cmd *cobra.Command, args []string) error {
	if len(args) == 0 && explicitJSON(cmd) {
		return groupUsage(cmd)
	}
	return cmd.Help()
}

func groupUsage(cmd *cobra.Command) error {
	names := []string{}
	for _, sub := range cmd.Commands() {
		if sub.IsAvailableCommand() || sub.Name() == "help" {
			names = append(names, sub.Name())
		}
	}
	return &contract.Error{
		Code:    contract.UsageError,
		Message: fmt.Sprintf("%s needs a subcommand: %s", cmd.CommandPath(), strings.Join(names, ", ")),
		Fields:  map[string]any{"subcommands": names},
	}
}
