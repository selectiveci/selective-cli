package main

import (
	"strings"

	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/selectiveci/selective-cli/internal/schema"
	"github.com/spf13/cobra"
)

func helpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "help [command...]",
		Short: "Help about any command; with --json, the machine-readable description of the CLI",
		Annotations: map[string]string{
			"exit_codes": "0,5",
			"mcp":        "skip",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			root := cmd.Root()
			out := cmd.OutOrStdout()
			target, err := find(root, args)
			if err != nil {
				return err
			}
			if !format.JSONMode(cmd.Flags(), out) {
				return target.Help()
			}
			doc, err := schema.Build(root, version)
			if err != nil {
				return contract.Errorf(contract.InternalError, "%s", err.Error())
			}
			if len(args) == 0 {
				return format.Resource(out, "help", doc)
			}
			node, ok := schema.Lookup(doc, args)
			if !ok {
				return unknownCommand(args)
			}
			return format.Resource(out, "command", node)
		},
	}
}

func find(root *cobra.Command, args []string) (*cobra.Command, error) {
	cur := root
	for _, name := range args {
		var next *cobra.Command
		for _, sub := range cur.Commands() {
			if sub.Name() == name {
				next = sub
			}
		}
		if next == nil {
			return nil, unknownCommand(args)
		}
		cur = next
	}
	return cur, nil
}

func unknownCommand(args []string) error {
	return contract.Errorf(contract.UsageError, "unknown command %q", strings.Join(args, " "))
}

func installHelp(root *cobra.Command) {
	root.SetHelpCommand(helpCmd())
	root.InitDefaultHelpCmd()
}
