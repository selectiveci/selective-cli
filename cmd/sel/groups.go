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

func helpOnlyGroup(root *cobra.Command, args []string) (*cobra.Command, error) {
	if !jsonFlagGiven(args) {
		return nil, nil
	}
	cmd, rest, err := root.Find(args)
	if err != nil {
		return unknownTopLevel(root, args, err)
	}
	if cmd.Runnable() || !cmd.HasAvailableSubCommands() || cmd.ParseFlags(rest) != nil {
		return nil, nil
	}
	if cmd.Flags().Changed("help") || !explicitJSON(cmd) {
		return nil, nil
	}
	return cmd, groupUsage(cmd, cmd.Flags().Args())
}

func unknownTopLevel(root *cobra.Command, args []string, findErr error) (*cobra.Command, error) {
	if root.ParseFlags(args) != nil || !explicitJSON(root) || root.Flags().NArg() == 0 {
		return nil, nil
	}
	return root, &contract.Error{
		Code:    contract.UsageError,
		Message: findErr.Error(),
		Fields:  map[string]any{"suggestions": suggestionsFor(root, root.Flags().Arg(0))},
	}
}

func suggestionsFor(cmd *cobra.Command, name string) []string {
	if cmd.SuggestionsMinimumDistance <= 0 {
		cmd.SuggestionsMinimumDistance = 2
	}
	return cmd.SuggestionsFor(name)
}

func showGroup(cmd *cobra.Command, args []string) error {
	if explicitJSON(cmd) {
		return groupUsage(cmd, args)
	}
	return cmd.Help()
}

func groupNoArgs(cmd *cobra.Command, args []string) error {
	if len(args) > 0 && explicitJSON(cmd) {
		return groupUsage(cmd, args)
	}
	return cobra.NoArgs(cmd, args)
}

func groupUsage(cmd *cobra.Command, args []string) error {
	names := []string{}
	for _, sub := range cmd.Commands() {
		if sub.IsAvailableCommand() || sub.Name() == "help" {
			names = append(names, sub.Name())
		}
	}
	fields := map[string]any{"subcommands": names}
	message := fmt.Sprintf("%s needs a subcommand: %s", cmd.CommandPath(), strings.Join(names, ", "))
	if len(args) > 0 {
		suggestions := suggestionsFor(cmd, args[0])
		fields["suggestions"] = suggestions
		message = fmt.Sprintf("unknown command %q for %q", args[0], cmd.CommandPath())
		if len(suggestions) > 0 {
			message += "\n\nDid you mean this?\n\t" + strings.Join(suggestions, "\n\t") + "\n"
		}
	}
	return &contract.Error{Code: contract.UsageError, Message: message, Fields: fields}
}
