package main

import (
	"os"
	"time"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/runs"
	"github.com/selectiveci/selective-cli/internal/setup"
	"github.com/selectiveci/selective-cli/internal/tail"
	"github.com/spf13/cobra"
)

func rerunCmd() *cobra.Command {
	var opts runs.RerunOptions
	var hostname string
	var failed bool
	var wait, timeout time.Duration
	cmd := &cobra.Command{
		Use:   "rerun BUILD",
		Short: "Rerun the failed tests of a live build",
		Long: `Queue the failed tests of a build that is still running for another try.
A finished build exits 5 with run_not_running: only a live build can be rerun.
--failed is required; it says which tests to rerun.

--dry-run reports how many tests would be queued and queues nothing.

--wait[=DURATION] follows the build to its outcome (10m when given without a
value; write --wait=5s, not --wait 5s). Line 1 on stdout is the write response,
then the build's NDJSON events from when the rerun was queued, with no replay of
earlier failures, ending with summary. The summary and the exit code are the
whole build's, as for sel tail: 0 every test passed, 1 failures remain, 2 every
failure passed on retry, 8 --wait or --timeout expired. --timeout caps the whole
command. --dry-run with --wait queues and waits for nothing.

Each invocation sends one random Idempotency-Key and reuses it when the CLI
retries, so a lost response never queues the rerun twice. Running the command
again later sends a new key and reruns again. --idempotency-key K sets the key
yourself, so a retry from another process is recognised too.

A build ID that exists in several of your organizations needs --org.`,
		Example: `  sel rerun --failed 8f3c9a
  sel rerun --failed 8f3c9a --wait --json
  sel rerun --failed 8f3c9a --dry-run --json
  sel rerun --failed 8f3c9a --idempotency-key retry-8f3c9a-1`,
		Args: cobra.ExactArgs(1),
		Annotations: map[string]string{
			"exit_codes":   "0,1,2,3,4,5,6,7,8",
			"events":       "build_start,selection,progress,test_fail,summary,heartbeat",
			"mutates":      "true",
			"wait_default": defaultTailWait,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if !failed {
				return contract.Errorf(contract.UsageError, "--failed is required: it says which tests to rerun")
			}
			if err := requirePositiveDurations(cmd, "wait", "timeout"); err != nil {
				return err
			}
			if timeout > 0 && wait <= 0 {
				return contract.Errorf(contract.UsageError, "--timeout needs --wait: without it the command does not wait")
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
			started := time.Now()
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
			if err := runs.Rerun(env, opts); err != nil || wait <= 0 || opts.DryRun {
				return err
			}
			return followRerun(env.Client, opts.Build, asJSON, started, wait, timeout)
		},
	}
	cmd.Flags().StringVar(&hostname, "hostname", "", "Selective server (default: the one you logged in to)")
	cmd.Flags().StringVar(&opts.Org, "org", "", "Organization slug, for a build ID found in several (default: SELECTIVE_ORG)")
	cmd.Flags().BoolVar(&failed, "failed", false, "Rerun the failed tests (required)")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Report what would be queued without queuing it")
	cmd.Flags().StringVar(&opts.Key, "idempotency-key", "", "Use this Idempotency-Key instead of a random one, to span invocations")
	cmd.Flags().DurationVar(&wait, "wait", 0, "Follow the rerun to its outcome, at most this long (default "+defaultTailWait+" when given without a value)")
	cmd.Flags().Lookup("wait").NoOptDefVal = defaultTailWait
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "Stop after this long in total and exit 8")
	return cmd
}

func followRerun(client *api.Client, build string, asJSON bool, started time.Time, wait, timeout time.Duration) error {
	limit, limitFlag := wait, "--wait"
	if timeout > 0 && timeout < wait {
		limit, limitFlag = timeout, "--timeout"
	}
	format, err := tailFormat(asJSON, "")
	if err != nil {
		return err
	}
	code, err := tail.Run(client, tail.Options{
		RunID:     build,
		Format:    format,
		Timeout:   limit,
		Started:   started,
		NoReplay:  true,
		LimitFlag: limitFlag,
	})
	if err != nil {
		return err
	}
	if code != contract.ExitOK {
		return exitCode(code)
	}
	return nil
}
