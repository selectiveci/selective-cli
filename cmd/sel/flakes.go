package main

import (
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/flakes"
	"github.com/spf13/cobra"
)

var flakeReadAnnotations = map[string]string{"exit_codes": "0,3,4,5,6"}

func flakesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "flakes",
		Short: "List flaky tests",
		Long:  "Read the organization's flaky tests.",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(flakesListCmd())
	return cmd
}

func flakesListCmd() *cobra.Command {
	var f setupFlags
	var opts flakes.ListOptions
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List flaky tests",
		Long: `List the organization's flaky tests, most impactful first.

Without --limit, pages are fetched until next_cursor is null. The walk stops
early after 20000 flakes or 2000 requests, whichever comes first: next_cursor
is then set and notices carries pagination_stopped_early. With --limit N, one page of at
most N (max 200) is returned, and next_cursor (when not null) goes to
--cursor for the next page. Pass the same filters with the cursor.

--status is flaking, quarantined or all (the default). --sort is impactful
(the default), rate, recent or count. --age is under_7d, under_30d or
under_90d. --suite takes a suite name or ID. The server applies them all.

For a large organization the server ranks approximately and may return
short pages. Metrics it did not compute in time are absent from a flake
(never 0) and the notices say so: run the command again to fill them.`,
		Example: `  sel flakes list
  sel flakes list --suite web-rspec --status quarantined --sort rate
  sel flakes list --limit 20 --json`,
		Args:        cobra.NoArgs,
		Annotations: flakeReadAnnotations,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("limit") && opts.Limit < 1 {
				return contract.Errorf(contract.UsageError, "--limit must be at least 1")
			}
			env, org, err := f.env(true)
			if err != nil {
				return err
			}
			env.Stdout, env.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
			opts.Hostname, opts.Org = f.hostname, org
			return flakes.List(env, opts)
		},
	}
	f.bind(cmd, true)
	cmd.Flags().StringVar(&opts.Suite, "suite", "", "Only this suite, by name or ID")
	cmd.Flags().StringVar(&opts.Status, "status", "", "flaking, quarantined or all (default)")
	cmd.Flags().StringVar(&opts.Sort, "sort", "", "impactful (default), rate, recent or count")
	cmd.Flags().StringVar(&opts.Age, "age", "", "under_7d, under_30d or under_90d")
	cmd.Flags().StringVar(&opts.Search, "search", "", "Only tests whose name or path matches")
	cmd.Flags().IntVar(&opts.Limit, "limit", 0, "Return one page of at most this many flakes (max 200)")
	cmd.Flags().StringVar(&opts.Cursor, "cursor", "", "Continue from a next_cursor")
	return cmd
}
