package main

import "github.com/spf13/cobra"

func mcpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "mcp",
		Short:       "Serve the CLI as an MCP server over stdio",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"exit_codes": "0,5,9", "mcp": "skip"},
		RunE:        func(cmd *cobra.Command, args []string) error { return nil },
	}
	cmd.Flags().Bool("read-only", false, "List and run only read tools")
	cmd.Flags().Bool("allow-destructive", false, "Also expose the destructive tools")
	return cmd
}
