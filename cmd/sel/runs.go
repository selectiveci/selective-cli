package main

import (
	"errors"
	"os"

	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/runs"
	"github.com/selectiveci/selective-cli/internal/setup"
	"github.com/selectiveci/selective-cli/internal/tail"
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

func tailCmd() *cobra.Command {
	var opts tail.Options
	var hostname string

	cmd := &cobra.Command{
		Use:   "tail",
		Short: "Stream live test results for the current commit",
		Long: `Connect to a live Selective CI build and stream test results as NDJSON.

Run from inside a git repository: sel tail auto-detects the repo and HEAD commit.

Exit codes:
  0  all tests passed
  1  real test failures
  2  passed, but a test failed before passing on retry (safe to proceed)
  3  no build found for this commit, or not inside a git repository
  4  authentication or network error
  5  usage error`,
		Example: `  sel tail
  sel tail --json
  sel tail --run-id 8f3c9a`,
		Args: cobra.NoArgs,
		Annotations: map[string]string{
			"exit_codes": "0,1,2,3,4,5",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			asJSON, _ := cmd.Flags().GetBool("json")
			var err error
			if opts.Format, err = tailFormat(asJSON, opts.Format); err != nil {
				return err
			}
			env, err := setup.Connect(hostname, asJSON)
			if err != nil {
				return tailConnectError(err)
			}
			code, err := tail.Run(env.Client, opts)
			if err != nil {
				return err
			}
			if code != contract.ExitOK {
				os.Exit(code)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&opts.RunID, "run-id", "", "Override: tail a specific build by run ID")
	cmd.Flags().StringVar(&opts.Commit, "commit", "", "Override: use this commit SHA instead of HEAD")
	cmd.Flags().StringVar(&hostname, "hostname", "", "Selective server (default: the one you logged in to)")
	cmd.Flags().StringVar(&opts.Format, "format", "", `Output format: "ndjson" (default when piped) or "pretty" (default on TTY)`)
	return cmd
}

func tailFormat(asJSON bool, requested string) (string, error) {
	switch {
	case asJSON && requested == "pretty":
		return "", contract.Errorf(contract.UsageError, "--json and --format pretty contradict each other")
	case asJSON:
		return "ndjson", nil
	}
	return requested, nil
}

func tailConnectError(err error) error {
	var coded contract.Coded
	if !errors.As(err, &coded) {
		return contract.Errorf(contract.UsageError, "%s", err.Error())
	}
	if coded.ErrorCode() == contract.Unauthorized {
		return contract.Errorf(contract.Unauthorized, "not logged in; run: sel auth login")
	}
	return err
}
