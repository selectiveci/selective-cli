package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/mcpserver"
	"github.com/selectiveci/selective-cli/internal/schema"
	"github.com/spf13/cobra"
)

type unclosable struct{ io.Writer }

func (unclosable) Close() error { return nil }

func mcpCmd() *cobra.Command {
	var exposure mcpserver.Exposure
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the CLI's commands as MCP tools over stdio",
		Long: `Serve the CLI's commands as MCP tools over stdio until stdin closes. Each
tool call runs this executable with the caller's credentials, from the
environment and config, and returns the command's result.

By default the destructive tools are hidden. --allow-destructive exposes them;
--read-only hides every tool that changes anything or runs local code. The two
flags cannot be combined.`,
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"exit_codes": "0,5,9", "mcp": "skip"},
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := schema.Build(cmd.Root(), version)
			if err != nil {
				return contract.Errorf(contract.InternalError, "%s", err.Error())
			}
			exe, err := os.Executable()
			if err != nil {
				return contract.Errorf(contract.InternalError, "cannot find this executable: %v", err)
			}
			server := mcpserver.New(doc, exposure, mcpserver.NewRunner(doc, exe, exposure).Handler())
			transport := &mcp.IOTransport{Reader: io.NopCloser(cmd.InOrStdin()), Writer: unclosable{cmd.OutOrStdout()}}
			if err := server.Run(cmd.Context(), transport); err != nil && !errors.Is(err, io.EOF) {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: mcp server stopped: %v\n", err)
				return exitCode(contract.ExitInternal)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&exposure.ReadOnly, "read-only", false, "Expose only the tools that change nothing and run no local code")
	cmd.Flags().BoolVar(&exposure.AllowDestructive, "allow-destructive", false, "Also expose the destructive tools")
	cmd.MarkFlagsMutuallyExclusive("read-only", "allow-destructive")
	return cmd
}
