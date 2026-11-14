package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/auth"
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/selectiveci/selective-cli/internal/setup"
	"github.com/selectiveci/selective-cli/internal/tail"
	"github.com/spf13/cobra"
)

var version = "dev"
var commit = "none"

func main() {
	os.Exit(run(newRoot(), os.Args[1:], os.Stdout, os.Stderr))
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:               "sel",
		Short:             "Selective CI — AI-first test intelligence CLI",
		Long:              "Stream live test results from Selective CI builds directly to your terminal or agent.",
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
	}
	root.PersistentFlags().Bool("json", false, "JSON output (the default when stdout isn't a terminal)")

	root.AddCommand(authCmd(), orgsCmd(), reposCmd(), suitesCmd(), tailCmd(), versionCmd(), doctorCmd())
	installHelp(root)

	root.SilenceUsage = true
	root.SilenceErrors = true
	return root
}

func run(root *cobra.Command, args []string, stdout, stderr io.Writer) int {
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	entered := false
	markEntered(root, &entered)

	cmd, err := root.ExecuteC()
	if err == nil {
		return contract.ExitOK
	}
	if auth.IsSilent(err) {
		return contract.ExitFor(contract.Unauthorized, 0)
	}
	if auth.IsCancelled(err) {
		fmt.Fprintf(stderr, "Error: %s\n", err)
		return contract.ExitInterrupted
	}
	coded := classify(err, entered)
	if format.JSONMode(cmd.Flags(), stdout) || jsonFlagGiven(args) {
		_ = format.Error(stdout, coded)
	}
	fmt.Fprintf(stderr, "Error: %s\n", coded.Error())
	return coded.ExitCode()
}

func jsonFlagGiven(args []string) bool {
	return slices.ContainsFunc(args, func(arg string) bool {
		if arg == "--json" {
			return true
		}
		value, ok := strings.CutPrefix(arg, "--json=")
		if !ok {
			return false
		}
		asked, err := strconv.ParseBool(value)
		return err == nil && asked
	})
}

func isNetworkError(err error) bool {
	var urlErr *url.Error
	var opErr *net.OpError
	var dnsErr *net.DNSError
	return errors.As(err, &urlErr) || errors.As(err, &opErr) || errors.As(err, &dnsErr)
}

func classify(err error, entered bool) contract.Coded {
	var coded contract.Coded
	switch {
	case !entered:
		return contract.Errorf(contract.UsageError, "%s", err.Error())
	case errors.As(err, &coded):
		return coded
	case isNetworkError(err):
		return contract.Errorf(contract.NetworkError, "%s", err.Error())
	default:
		return contract.Errorf(contract.InternalError, "%s", err.Error())
	}
}

func markEntered(c *cobra.Command, entered *bool) {
	if run := c.RunE; run != nil {
		c.RunE = func(cmd *cobra.Command, args []string) error {
			*entered = true
			return run(cmd, args)
		}
	}
	for _, sub := range c.Commands() {
		markEntered(sub, entered)
	}
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

Run from inside a git repository — sel tail auto-detects the repo and HEAD commit.

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
			fmt.Printf("sel %s (%s)\n", version, commit)
		},
	}
}

func suitesCmd() *cobra.Command {
	var f setupFlags
	cmd := &cobra.Command{
		Use:   "suites",
		Short: "List, create and configure test suites",
		Long: `List the organization's test suites, with when each last ran.

  sel suites create   Create a suite for a repository (needs --setup login)
  sel suites key        Hand a suite's API key to a secret store
  sel suites configure  Change a suite's settings, such as test selection`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			env, org, err := f.env(true)
			if err != nil {
				return err
			}
			return env.Suites(org)
		},
	}
	f.bind(cmd, true)
	cmd.AddCommand(suitesCreateCmd(), suitesKeyCmd(), suitesConfigureCmd())
	return cmd
}

func suitesCreateCmd() *cobra.Command {
	var f setupFlags
	var name, repo, framework string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a suite for a repository",
		Long: `Create a test suite for a repository, or find the one already there:
running it again with the same name returns the same suite. Names are
unique within an organization, so with several repositories include the
repository in the name (web-rspec).

The repository defaults to this checkout's origin remote, and must be one
the organization's Selective GitHub App can see (sel repos). A
repository with several test commands (say, RSpec and Minitest) gets one
suite per command.`,
		Example: `  sel suites create --name web-rspec --framework rspec
  sel suites create --name web-minitest --framework minitest --repo acme/web`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			env, org, err := f.env(true)
			if err != nil {
				return err
			}
			return env.CreateSuite(org, name, repo, framework)
		},
	}
	f.bind(cmd, true)
	cmd.Flags().StringVar(&name, "name", "", "Suite name, e.g. the test command it runs (required)")
	cmd.Flags().StringVar(&repo, "repo", "", "GitHub repository owner/name (default: this checkout's origin)")
	cmd.Flags().StringVar(&framework, "framework", "", "Test framework: rspec, minitest, exunit, pytest, junit, phpunit or vitest (default: learned from the first run)")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func suitesKeyCmd() *cobra.Command {
	var f setupFlags
	var opts setup.KeyOptions
	cmd := &cobra.Command{
		Use:   "key SUITE",
		Short: "Hand a suite's API key to a secret store without showing it",
		Long: `Send a suite's API key (by name or ID) where CI can read it, without it
appearing on screen.

  --github-secret NAME   set a GitHub Actions secret on the suite's repository
                         (uses the GitHub CLI, gh)
  --stdout               write the key to stdout, piped straight into a
                         secret store; never to a terminal

One of the two is required.`,
		Example: `  sel suites key rspec --github-secret SELECTIVE_API_KEY
  sel suites key rspec --stdout | rwx vaults secrets set SELECTIVE_API_KEY`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env, org, err := f.env(true)
			if err != nil {
				return err
			}
			return env.Key(org, args[0], opts)
		},
	}
	f.bind(cmd, true)
	cmd.Flags().StringVar(&opts.GithubSecret, "github-secret", "", "Set this GitHub Actions secret on the repository")
	cmd.Flags().BoolVar(&opts.Stdout, "stdout", false, "Write the key to stdout, which must be a pipe into a secret store")
	cmd.Flags().StringVar(&opts.Repo, "repo", "", "Repository for --github-secret (default: the suite's)")
	return cmd
}

func suitesConfigureCmd() *cobra.Command {
	var f setupFlags
	var settings api.SuiteSettings
	var defaultBranch string
	cmd := &cobra.Command{
		Use:   "configure SUITE",
		Short: "Change a suite's settings, such as test selection",
		Long: `Change a suite's settings (by name or ID). Needs a --setup login, as an
owner or admin of the organization.

  --selection off      every test runs
  --selection shadow   Selective decides which tests to run but runs them all,
                       so you can see what it would have skipped
  --selection live     only the selected tests run

Runs on the repository's default branch run the full suite, so anything
selection missed on a pull request is caught when it merges:

  --default-branch full     run every test there (the default)
  --default-branch select   select there too`,
		Example: `  sel suites configure web-rspec --selection shadow
  sel suites configure web-rspec --selection live
  sel suites configure web-rspec --default-branch select`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch defaultBranch {
			case "":
			case "full", "select":
				selects := defaultBranch == "select"
				settings.SelectionOnDefaultBranch = &selects
			default:
				return contract.Errorf(contract.UsageError, "--default-branch must be one of: %s", strings.Join(setup.DefaultBranchModes, ", "))
			}
			env, org, err := f.env(true)
			if err != nil {
				return err
			}
			return env.Configure(org, args[0], settings)
		},
	}
	f.bind(cmd, true)
	cmd.Flags().StringVar(&settings.SelectionMode, "selection", "", "Test selection: off, shadow or live")
	cmd.Flags().StringVar(&defaultBranch, "default-branch", "", "Default-branch runs: full or select")
	return cmd
}
