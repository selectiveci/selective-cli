package main

import (
	"os"

	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/runs"
	"github.com/selectiveci/selective-cli/internal/setup"
	"github.com/spf13/cobra"
)

func buildsCmd() *cobra.Command {
	var opts runs.BuildsOptions
	var hostname string
	var flaky bool
	cmd := &cobra.Command{
		Use:   "builds",
		Short: "List the builds of an organization",
		Long: `List an organization's builds, newest first. Exits 0 when the read succeeds,
whatever the builds' outcomes.

--status keeps builds with exactly that status: failed, running, incomplete,
cancelled or success. A build that is still running counts as running, even
when a test in it has already failed. --flaky keeps builds with flaky tests,
and combines with --status. --branch keeps one branch.

Without --all, one page is returned: --limit (default 50, max 200) results and
a next_cursor, which goes to --cursor for the next page. With --all, pages are
fetched until next_cursor is null, --limit being the page size (default 200).
The walk stops early after 20000 builds or 2000 requests, whichever comes
first: next_cursor is then set and notices carries pagination_stopped_early.

The organization is --org, then SELECTIVE_ORG, then your only one.`,
		Example: `  sel builds
  sel builds --status failed --branch main
  sel builds --flaky --all --json`,
		Args: cobra.NoArgs,
		Annotations: map[string]string{
			"exit_codes": "0,3,4,5,6",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("limit") && opts.Limit < 1 {
				return contract.Errorf(contract.UsageError, "--limit must be at least 1")
			}
			if cmd.Flags().Changed("flaky") {
				opts.Flaky = &flaky
			}
			asJSON, _ := cmd.Flags().GetBool("json")
			env, err := setup.Connect(hostname, asJSON)
			if err != nil {
				return err
			}
			env.Stdout, env.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
			return runs.Builds(env, opts)
		},
	}
	cmd.Flags().StringVar(&hostname, "hostname", "", "Selective server (default: the one you logged in to)")
	cmd.Flags().StringVar(&opts.Org, "org", "", "Organization slug (default: SELECTIVE_ORG, then your only organization)")
	cmd.Flags().StringVar(&opts.Status, "status", "", "failed, running, incomplete, cancelled or success")
	cmd.Flags().BoolVar(&flaky, "flaky", false, "Only builds with flaky tests")
	cmd.Flags().StringVar(&opts.Branch, "branch", "", "Only this branch")
	cmd.Flags().IntVar(&opts.Limit, "limit", 0, "Results per page (default 50, max 200)")
	cmd.Flags().StringVar(&opts.Cursor, "cursor", "", "Continue from a next_cursor")
	cmd.Flags().BoolVar(&opts.All, "all", false, "Fetch every page")
	return cmd
}

func buildCmd() *cobra.Command {
	var opts runs.BuildOptions
	var hostname string
	cmd := &cobra.Command{
		Use:   "build BUILD",
		Short: "Show one build and its runs",
		Long: `Show a build: its status, counts and timings, and one entry per suite run.
Exits 0 when the read succeeds, even when the build failed; the build's
outcome is sel tail's job.

On a terminal the output is a summary and a table of runs. - means the value is
empty and unknown means the server did not report it; neither is ever shown as
0. Piped, or with --json, the output is one JSON document.

A build ID that exists in several of your organizations needs --org.`,
		Example: `  sel build 8f3c9a
  sel build 8f3c9a --org acme --json`,
		Args: cobra.ExactArgs(1),
		Annotations: map[string]string{
			"exit_codes": "0,3,4,5,6",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.ID = args[0]
			if opts.Org == "" {
				opts.Org = os.Getenv("SELECTIVE_ORG")
			}
			asJSON, _ := cmd.Flags().GetBool("json")
			env, err := setup.Connect(hostname, asJSON)
			if err != nil {
				return err
			}
			env.Stdout, env.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
			return runs.Build(env, opts)
		},
	}
	cmd.Flags().StringVar(&hostname, "hostname", "", "Selective server (default: the one you logged in to)")
	cmd.Flags().StringVar(&opts.Org, "org", "", "Organization slug, for a build ID found in several (default: SELECTIVE_ORG)")
	return cmd
}
