package main

import (
	"encoding/json"
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
	"github.com/selectiveci/selective-cli/internal/update"
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

	root.AddCommand(authCmd(), orgsCmd(), reposCmd(), suitesCmd(), tailCmd(), failuresCmd(), buildsCmd(), buildCmd(), trendsCmd(), versionCmd(), doctorCmd(), flakesCmd(), transportCmd(), testCmd(), tokensCmd(), approvalsCmd())
	installHelp(root)

	root.SilenceUsage = true
	root.SilenceErrors = true
	return root
}

func run(root *cobra.Command, args []string, stdout, stderr io.Writer) int {
	update.Version = version
	var checker *update.Checker
	if !isVersionCommand(root, args) {
		checker = update.StartCheck(version, os.Getenv)
	}
	cmd, code := runCommand(root, args, stdout, stderr)
	checker.Stop()
	printUpdateNotice(cmd, stdout, stderr)
	return code
}

type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit %d", int(e)) }

func runCommand(root *cobra.Command, args []string, stdout, stderr io.Writer) (*cobra.Command, int) {
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	if secretFlagGiven(root, args) {
		helpToStderr(root, args, stderr)
	}
	entered := false
	markEntered(root, &entered)

	cmd, err := root.ExecuteC()
	if err == nil {
		return cmd, contract.ExitOK
	}
	var exit exitCode
	if errors.As(err, &exit) {
		return cmd, int(exit)
	}
	if auth.IsSilent(err) {
		return cmd, contract.ExitFor(contract.Unauthorized, 0)
	}
	if auth.IsCancelled(err) {
		fmt.Fprintf(stderr, "Error: %s\n", err)
		return cmd, contract.ExitInterrupted
	}
	var pending *api.PendingApproval
	if errors.As(err, &pending) {
		reportPending(errorObjectOnStdout(cmd, args, stdout), secretFlagGivenFor(root, cmd, args), stdout, stderr, pending)
		return cmd, pending.ExitCode()
	}
	coded := classify(err, entered)
	if errorObjectOnStdout(cmd, args, stdout) {
		sink := stdout
		if secretFlagGivenFor(root, cmd, args) {
			sink = stderr
		}
		_ = format.Error(sink, coded)
	}
	fmt.Fprintf(stderr, "Error: %s\n", coded.Error())
	return cmd, coded.ExitCode()
}

func pendingSentence(p *api.PendingApproval) string {
	if p.DryRun {
		return "Dry run: this needs a person's approval; nothing was requested."
	}
	var approval struct {
		URL       string `json:"url"`
		ExpiresAt string `json:"expires_at"`
	}
	_ = json.Unmarshal(p.Approval, &approval)
	url, expires := approval.URL, approval.ExpiresAt
	if url == "" {
		return "Approval needed. Run the same command again once it is approved."
	}
	return fmt.Sprintf("Approval needed: %s (expires %s UTC). Run the same command again once it is approved.", url, utcMinute(expires))
}

func reportPending(asJSON, secretOnStderr bool, stdout, stderr io.Writer, p *api.PendingApproval) {
	if !asJSON {
		fmt.Fprintln(stderr, pendingSentence(p))
		return
	}
	sink := stdout
	if secretOnStderr {
		sink = stderr
	}
	_ = format.Document(sink, struct {
		Result   string          `json:"result"`
		DryRun   bool            `json:"dry_run"`
		Approval json.RawMessage `json:"approval,omitempty"`
	}{"pending_approval", p.DryRun, p.Approval})
}

func errorObjectOnStdout(cmd *cobra.Command, args []string, stdout io.Writer) bool {
	if jsonFlagGiven(args) {
		return true
	}
	if f := cmd.Flags().Lookup("format"); f != nil && f.Changed && f.Value.String() != "" {
		return f.Value.String() == "ndjson"
	}
	return format.JSONMode(cmd.Flags(), stdout)
}

func helpToStderr(root *cobra.Command, args []string, stderr io.Writer) {
	help, usage := root.HelpFunc(), root.UsageFunc()
	root.SetHelpFunc(func(c *cobra.Command, helpArgs []string) {
		if secretFlagGivenFor(root, c, args) {
			c.SetOut(stderr)
		}
		help(c, helpArgs)
	})
	root.SetUsageFunc(func(c *cobra.Command) error {
		if secretFlagGivenFor(root, c, args) {
			c.SetOut(stderr)
		}
		return usage(c)
	})
}

func secretFlagGiven(root *cobra.Command, args []string) bool {
	return anyFlagGiven(args, treeSecretFlags(root))
}

func secretFlagGivenFor(root, resolved *cobra.Command, args []string) bool {
	if resolved != nil && resolved.Runnable() && resolved.Name() != "help" {
		return anyFlagGiven(args, ownSecretFlags(resolved))
	}
	return secretFlagGiven(root, args)
}

func anyFlagGiven(args, names []string) bool {
	if end := slices.Index(args, "--"); end >= 0 {
		args = args[:end]
	}
	return slices.ContainsFunc(names, func(name string) bool { return flagGiven(args, name) })
}

func ownSecretFlags(c *cobra.Command) []string {
	var names []string
	for _, name := range strings.Split(c.Annotations["secret_flags"], ",") {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func treeSecretFlags(c *cobra.Command) []string {
	names := ownSecretFlags(c)
	for _, sub := range c.Commands() {
		names = append(names, treeSecretFlags(sub)...)
	}
	return names
}

func flagGiven(args []string, name string) bool {
	return slices.ContainsFunc(args, func(arg string) bool {
		if arg == "--"+name {
			return true
		}
		value, ok := strings.CutPrefix(arg, "--"+name+"=")
		if !ok {
			return false
		}
		asked, err := strconv.ParseBool(value)
		return err == nil && asked
	})
}

func jsonFlagGiven(args []string) bool {
	return flagGiven(args, "json")
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

func suitesCmd() *cobra.Command {
	var f setupFlags
	cmd := &cobra.Command{
		Use:   "suites",
		Short: "List, create and configure test suites",
		Long: `List the organization's test suites, with when each last ran.

  sel suites create   Create a suite for a repository (needs --setup login)
  sel suites key        Hand a suite's API key to a secret store
  sel suites configure  Change a suite's settings, such as test selection
  sel suites archive    Archive a suite
  sel suites restore    Bring an archived suite back
  sel suites delete     Delete an archived suite for good
  sel suites show       Show a suite and its settings
  sel suites settings   Show or change a suite's settings`,
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
	cmd.AddCommand(suitesCreateCmd(), suitesKeyCmd(), suitesConfigureCmd(), suitesArchiveCmd(), suitesRestoreCmd(), suitesDeleteCmd(), suitesShowCmd(), suitesSettingsCmd())
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
