package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/selectiveci/selective-cli/internal/auth"
	"github.com/selectiveci/selective-cli/internal/tail"
)

var version = "dev"
var commit = "none"

func main() {
	root := &cobra.Command{
		Use:   "selective",
		Short: "Selective CI — AI-first test intelligence CLI",
		Long:  "Stream live test results from Selective CI builds directly to your terminal or agent.",
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
	}

	root.AddCommand(authCmd(), tailCmd(), versionCmd())

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func authCmd() *cobra.Command {
	var token string

	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Authenticate with Selective CI",
		Long: `Save an API token for use by the CLI.

Create a token at https://app.selective.ci/users/settings/tokens, then run:

  selective auth --token sk_live_...

Or run without --token to be prompted.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return auth.Run(token)
		},
	}

	cmd.Flags().StringVar(&token, "token", "", "API token (sk_live_...)")
	return cmd
}

func tailCmd() *cobra.Command {
	var runID string
	var commitSHA string
	var format string

	cmd := &cobra.Command{
		Use:   "tail",
		Short: "Stream live test results for the current commit",
		Long: `Connect to a live Selective CI build and stream test results as NDJSON.

Run from inside a git repository — selective tail auto-detects the repo and HEAD commit.

Exit codes:
  0  all tests passed
  1  real test failures
  2  only flaky failures (safe to proceed)
  3  no build found for this commit
  4  authentication or network error`,
		RunE: func(cmd *cobra.Command, args []string) error {
			code, err := tail.Run(runID, commitSHA, format)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			}
			os.Exit(code)
			return nil
		},
	}

	cmd.Flags().StringVar(&runID, "run-id", "", "Override: tail a specific build by run ID")
	cmd.Flags().StringVar(&commitSHA, "commit", "", "Override: use this commit SHA instead of HEAD")
	cmd.Flags().StringVar(&format, "format", "", `Output format: "ndjson" (default when piped) or "pretty" (default on TTY)`)

	return cmd
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("selective %s (%s)\n", version, commit)
		},
	}
}
