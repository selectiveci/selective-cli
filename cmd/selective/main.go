package main

import (
	"fmt"
	"os"

	"github.com/selectiveci/selective-cli/internal/auth"
	"github.com/selectiveci/selective-cli/internal/tail"
	"github.com/spf13/cobra"
)

var version = "dev"
var commit = "none"

func main() {
	root := &cobra.Command{
		Use:               "selective",
		Short:             "Selective CI — AI-first test intelligence CLI",
		Long:              "Stream live test results from Selective CI builds directly to your terminal or agent.",
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
	}

	root.AddCommand(authCmd(), tailCmd(), versionCmd())

	root.SilenceUsage = true
	root.SilenceErrors = true
	if err := root.Execute(); err != nil {
		if !auth.IsSilent(err) {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		os.Exit(1)
	}
}

func authCmd() *cobra.Command {
	var legacyToken, legacyHost string

	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Log in to Selective, check the login, or log out",
		Long: `Manage the CLI's login.

  selective auth login     Log in through your browser
  selective auth status    Show who you're logged in as
  selective auth logout    Revoke this machine's token and forget it

CI and agents can skip logging in: set SELECTIVE_TOKEN (and SELECTIVE_HOST
for a server other than app.selective.ci).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if legacyToken != "" {
				return auth.LegacyTokenLogin(legacyToken, legacyHost, version, os.Stderr)
			}
			return cmd.Help()
		},
	}
	// `selective auth --token …` predates `auth login`; keep it working, quietly.
	cmd.Flags().StringVar(&legacyToken, "token", "", "")
	cmd.Flags().StringVar(&legacyHost, "hostname", "", "")
	_ = cmd.Flags().MarkHidden("token")
	_ = cmd.Flags().MarkHidden("hostname")

	cmd.AddCommand(authLoginCmd(), authStatusCmd(), authLogoutCmd())
	return cmd
}

func authLoginCmd() *cobra.Command {
	var opts auth.LoginOptions

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in through your browser",
		Long: `Log in to Selective.

Prints a one-time code and opens your browser (or prints the URL when there
isn't one: SSH, containers, CI). Approve the request there and the CLI is
logged in. The token is stored in your system keychain and appears on your
API tokens page, where you can revoke it.

For scripts, pass a token on stdin instead:

  selective auth login --with-token < token.txt`,
		Example: `  selective auth login
  selective auth login --hostname staging.selective.ci
  selective auth login --hostname localhost:4000`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Version = version
			opts.Stdin = os.Stdin
			opts.Stderr = os.Stderr
			return auth.Login(opts)
		},
	}
	cmd.Flags().StringVar(&opts.Hostname, "hostname", "", "Selective server (default app.selective.ci, or SELECTIVE_HOST)")
	cmd.Flags().BoolVar(&opts.WithToken, "with-token", false, "Read an API token from stdin instead of using the browser")
	cmd.Flags().BoolVar(&opts.NoBrowser, "no-browser", false, "Print the URL instead of opening a browser")
	return cmd
}

func authStatusCmd() *cobra.Command {
	var hostname string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show who you're logged in as",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return auth.Status(hostname, os.Stdout)
		},
	}
	cmd.Flags().StringVar(&hostname, "hostname", "", "Selective server")
	return cmd
}

func authLogoutCmd() *cobra.Command {
	var hostname string
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Revoke this machine's token and forget it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return auth.Logout(hostname, os.Stderr)
		},
	}
	cmd.Flags().StringVar(&hostname, "hostname", "", "Selective server")
	return cmd
}

func tailCmd() *cobra.Command {
	var runID string
	var commitSHA string
	var format string
	var hostname string

	cmd := &cobra.Command{
		Use:   "tail",
		Short: "Stream live test results for the current commit",
		Long: `Connect to a live Selective CI build and stream test results as NDJSON.

Run from inside a git repository — selective tail auto-detects the repo and HEAD commit.

Exit codes:
  0  all tests passed
  1  real test failures
  2  passed, but a test failed before passing on retry (safe to proceed)
  3  no build found for this commit
  4  authentication or network error`,
		RunE: func(cmd *cobra.Command, args []string) error {
			code, err := tail.Run(runID, commitSHA, format, hostname)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			}
			os.Exit(code)
			return nil
		},
	}

	cmd.Flags().StringVar(&runID, "run-id", "", "Override: tail a specific build by run ID")
	cmd.Flags().StringVar(&commitSHA, "commit", "", "Override: use this commit SHA instead of HEAD")
	cmd.Flags().StringVar(&hostname, "hostname", "", "Selective server (default: the one you logged in to)")
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
