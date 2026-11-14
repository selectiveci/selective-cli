package main

import (
	"os"

	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/runs"
	"github.com/selectiveci/selective-cli/internal/setup"
	"github.com/spf13/cobra"
)

func failuresCmd() *cobra.Command {
	var opts runs.FailuresOptions
	var hostname string
	cmd := &cobra.Command{
		Use:   "failures BUILD",
		Short: "List the failing tests of a build",
		Long: `List the tests that failed in a build, with their error, location and
whether a retry passed. Exits 0 when the read succeeds, even when there are
failures; the build's outcome is sel tail's job.

--outcome picks which results: failed (the default), flaky (failed, then
passed on retry) or all.

Without --limit, pages are fetched until next_cursor is null. The walk stops
early after 20000 results or 2000 requests, whichever comes first:
next_cursor is then set and notices carries pagination_stopped_early. With
--limit N, one page of at most N (max 200) is returned, and next_cursor
(when not null) goes to --cursor for the next page.

--suite keeps only the results of the suite with that name or ID. It filters
what the server returned, so with --limit a page can come back short while
next_cursor is still set.

A build ID that exists in several of your organizations needs --org.`,
		Example: `  sel failures 8f3c9a
  sel failures 8f3c9a --suite web-rspec --outcome all
  sel failures 8f3c9a --limit 20 --json`,
		Args: cobra.ExactArgs(1),
		Annotations: map[string]string{
			"exit_codes": "0,3,4,5,6",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("limit") && opts.Limit < 1 {
				return contract.Errorf(contract.UsageError, "--limit must be at least 1")
			}
			opts.Build = args[0]
			opts.Hostname = hostname
			if opts.Org == "" {
				opts.Org = os.Getenv("SELECTIVE_ORG")
			}
			asJSON, _ := cmd.Flags().GetBool("json")
			env, err := setup.Connect(hostname, asJSON)
			if err != nil {
				return err
			}
			env.Stdout, env.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
			return runs.Failures(env, opts)
		},
	}
	cmd.Flags().StringVar(&hostname, "hostname", "", "Selective server (default: the one you logged in to)")
	cmd.Flags().StringVar(&opts.Org, "org", "", "Organization slug, for a build ID found in several (default: SELECTIVE_ORG)")
	cmd.Flags().StringVar(&opts.Suite, "suite", "", "Only this suite, by name or ID")
	cmd.Flags().StringVar(&opts.Outcome, "outcome", "", "failed (default), flaky or all")
	cmd.Flags().IntVar(&opts.Limit, "limit", 0, "Return one page of at most this many results (max 200)")
	cmd.Flags().StringVar(&opts.Cursor, "cursor", "", "Continue from a next_cursor")
	return cmd
}
