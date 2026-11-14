package main

import (
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/runs"
	"github.com/selectiveci/selective-cli/internal/setup"
	"github.com/spf13/cobra"
)

func trendsCmd() *cobra.Command {
	var opts runs.TrendsOptions
	var hostname string
	cmd := &cobra.Command{
		Use:   "trends",
		Short: "Show an organization's test and build trends",
		Long: `Show how an organization's builds and tests are doing over a window: the
summary tiles with the previous window beside them, outcomes, failures and
build duration over time, and the top failing, most flaky and slowest tests.
Exits 0 when the read succeeds.

--range is day, week or month (default month). --section picks one of summary,
outcomes, failures, duration, top_failing, most_flaky or slowest; without it
every section is returned. --limit sets the length of the three ranked lists
(default 10, max 50).

A value the server doesn't have is left out, or null when the window is known
to be empty: an empty window has a null pass_rate, not 0.

--suite takes a suite ID (sel suites lists them). A token limited to specific
repositories must pass --suite.

The organization is --org, then SELECTIVE_ORG, then your only one.`,
		Example: `  sel trends
  sel trends --range week --section top_failing --limit 5
  sel trends --suite SUITE_ID --json`,
		Args: cobra.NoArgs,
		Annotations: map[string]string{
			"exit_codes": "0,3,4,5,6",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("limit") && opts.Limit < 1 {
				return contract.Errorf(contract.UsageError, "--limit must be at least 1")
			}
			asJSON, _ := cmd.Flags().GetBool("json")
			env, err := setup.Connect(hostname, asJSON)
			if err != nil {
				return err
			}
			env.Stdout, env.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
			return runs.Trends(env, opts)
		},
	}
	cmd.Flags().StringVar(&hostname, "hostname", "", "Selective server (default: the one you logged in to)")
	cmd.Flags().StringVar(&opts.Org, "org", "", "Organization slug (default: SELECTIVE_ORG, then your only organization)")
	cmd.Flags().StringVar(&opts.Range, "range", "", "day, week or month (default month)")
	cmd.Flags().StringVar(&opts.Suite, "suite", "", "Only this suite, by ID (see sel suites)")
	cmd.Flags().StringVar(&opts.Section, "section", "", "One section: summary, outcomes, failures, duration, top_failing, most_flaky or slowest")
	cmd.Flags().IntVar(&opts.Limit, "limit", 0, "Length of the ranked lists (default 10, max 50)")
	return cmd
}
