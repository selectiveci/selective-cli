package main

import (
	"os"

	"github.com/selectiveci/selective-cli/internal/auth"
	"github.com/spf13/cobra"
)

func authCmd() *cobra.Command {
	var legacyToken, legacyHost string

	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Log in to Selective, check the login, or log out",
		Long: `Manage the CLI's login.

  sel auth login     Log in through your browser
  sel auth status    Show who you're logged in as
  sel auth logout    Revoke this machine's token and forget it

CI and agents can skip logging in: set SELECTIVE_TOKEN (and SELECTIVE_HOST
for a server other than app.selective.ci).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if legacyToken != "" {
				return auth.LegacyTokenLogin(legacyToken, legacyHost, version, os.Stderr)
			}
			return cmd.Help()
		},
	}
	// `sel auth --token …` predates `auth login`; keep it working, quietly.
	cmd.Flags().StringVar(&legacyToken, "token", "", "")
	cmd.Flags().StringVar(&legacyHost, "hostname", "", "")
	_ = cmd.Flags().MarkHidden("token")
	_ = cmd.Flags().MarkHidden("hostname")

	cmd.AddCommand(authLoginCmd(), authSignupCmd(), authStatusCmd(), authLogoutCmd())
	return cmd
}

func authLoginCmd() *cobra.Command {
	var opts auth.LoginOptions

	cmd := &cobra.Command{
		Use: "login",
		Annotations: map[string]string{
			"exit_codes": "0,3,4,5,6,7,9,130",
			"events":     "authorize,logged_in",
			"mcp":        "skip",
		},
		Short: "Log in through your browser",
		Long: `Log in to Selective.

Prints a one-time code and opens your browser (or prints the URL when there
isn't one: SSH, containers, CI). Without an account, the page signs you up
with GitHub first. Approve the request there and the CLI is logged in. The
token is stored in your system keychain and appears on your API tokens
page, where you can revoke it.

--setup also lets the CLI create suites and read their API keys, for
setting a repository up. --write also lets it make changes: revoke tokens,
and the write commands as they ship. --json writes events to stdout as they happen
(first the URL to open, then the login), for an agent to read while the
command waits.

For scripts, pass a token on stdin instead:

  sel auth login --with-token < token.txt`,
		Example: `  sel auth login
  sel auth login --setup --json
  sel auth login --write
  sel auth login --hostname staging.selective.ci
  sel auth login --hostname localhost:4000`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Version = version
			opts.Stdin = os.Stdin
			opts.Stdout = os.Stdout
			opts.Stderr = os.Stderr
			return auth.Login(opts)
		},
	}
	cmd.Flags().StringVar(&opts.Hostname, "hostname", "", "Selective server (default app.selective.ci, or SELECTIVE_HOST)")
	cmd.Flags().BoolVar(&opts.WithToken, "with-token", false, "Read an API token from stdin instead of using the browser")
	cmd.Flags().BoolVar(&opts.NoBrowser, "no-browser", false, "Print the URL instead of opening a browser")
	cmd.Flags().BoolVar(&opts.Setup, "setup", false, "Also allow creating suites and reading their API keys")
	cmd.Flags().BoolVar(&opts.Write, "write", false, "Also allow changes (revoke tokens, and the write commands as they ship)")
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "Write login events to stdout as JSON lines")
	return cmd
}

func authSignupCmd() *cobra.Command {
	opts := auth.LoginOptions{Signup: true}
	cmd := &cobra.Command{
		Use: "signup",
		Annotations: map[string]string{
			"exit_codes": "0,3,4,5,6,7,9,130",
			"events":     "authorize,logged_in",
			"mcp":        "skip",
		},
		Short: "Create a Selective account and log in",
		Long: `Create a Selective account with GitHub and log the CLI in to it.

Like auth login --setup, but the page greets the person with signup. Someone
who already has an account can still sign in from there.`,
		Example: `  sel auth signup
  sel auth signup --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Version = version
			opts.Stdin = os.Stdin
			opts.Stdout = os.Stdout
			opts.Stderr = os.Stderr
			return auth.Login(opts)
		},
	}
	cmd.Flags().StringVar(&opts.Hostname, "hostname", "", "Selective server (default app.selective.ci, or SELECTIVE_HOST)")
	cmd.Flags().BoolVar(&opts.NoBrowser, "no-browser", false, "Print the URL instead of opening a browser")
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "Write login events to stdout as JSON lines")
	return cmd
}

func authStatusCmd() *cobra.Command {
	var hostname string
	cmd := &cobra.Command{
		Use:         "status",
		Annotations: map[string]string{"exit_codes": "0,3,4,5,6,9"},
		Short:       "Show who you're logged in as",
		Args:        cobra.NoArgs,
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
		Use:         "logout",
		Annotations: map[string]string{"exit_codes": "0,5,9"},
		Short:       "Revoke this machine's token and forget it",
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return auth.Logout(hostname, os.Stderr)
		},
	}
	cmd.Flags().StringVar(&hostname, "hostname", "", "Selective server")
	return cmd
}
