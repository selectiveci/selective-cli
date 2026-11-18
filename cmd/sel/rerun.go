package main

import (
	"os"

	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/runs"
	"github.com/selectiveci/selective-cli/internal/setup"
	"github.com/spf13/cobra"
)

func rerunCmd() *cobra.Command {
	var opts runs.RerunOptions
	var hostname string
	var failed bool
	cmd := &cobra.Command{
		Use:   "rerun BUILD",
		Short: "Rerun the failed tests of a live build",
		Long: `Queue the failed tests of a build that is still running for another try.
A finished build exits 5 with run_not_running: only a live build can be rerun.
--failed is required; it says which tests to rerun.

--dry-run reports how many tests would be queued and queues nothing.

Each invocation sends one random Idempotency-Key and reuses it when the CLI
retries, so a lost response never queues the rerun twice. Running the command
again later sends a new key and reruns again. --idempotency-key K sets the key
yourself, so a retry from another process is recognised too.

A build ID that exists in several of your organizations needs --org.`,
		Example: `  sel rerun --failed 8f3c9a
  sel rerun --failed 8f3c9a --dry-run --json
  sel rerun --failed 8f3c9a --idempotency-key retry-8f3c9a-1`,
		Args: cobra.ExactArgs(1),
		Annotations: map[string]string{
			"exit_codes": "0,3,4,5,6,7",
			"mutates":    "true",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if !failed {
				return contract.Errorf(contract.UsageError, "--failed is required: it says which tests to rerun")
			}
			if opts.Key == "" {
				if cmd.Flags().Changed("idempotency-key") {
					return contract.Errorf(contract.UsageError, "--idempotency-key must not be empty")
				}
				var err error
				if opts.Key, err = runs.NewIdempotencyKey(); err != nil {
					return err
				}
			}
			opts.Build = args[0]
			if opts.Org == "" {
				opts.Org = os.Getenv("SELECTIVE_ORG")
			}
			asJSON, _ := cmd.Flags().GetBool("json")
			env, err := setup.Connect(hostname, asJSON)
			if err != nil {
				return err
			}
			env.Stdout, env.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
			return runs.Rerun(env, opts)
		},
	}
	cmd.Flags().StringVar(&hostname, "hostname", "", "Selective server (default: the one you logged in to)")
	cmd.Flags().StringVar(&opts.Org, "org", "", "Organization slug, for a build ID found in several (default: SELECTIVE_ORG)")
	cmd.Flags().BoolVar(&failed, "failed", false, "Rerun the failed tests (required)")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Report what would be queued without queuing it")
	cmd.Flags().StringVar(&opts.Key, "idempotency-key", "", "Use this Idempotency-Key instead of a random one, to span invocations")
	return cmd
}
