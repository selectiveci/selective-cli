package main

import (
	"os"
	"strconv"

	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/runs"
	"github.com/selectiveci/selective-cli/internal/tail"
	"github.com/spf13/cobra"
)

const maxTailRetries = 10

func tailRetries(cmd *cobra.Command, opts *tail.Options) error {
	flags := cmd.Flags()
	if !flags.Changed("retries") {
		for _, name := range []string{"idempotency-key", "dry-run", "org"} {
			if flags.Changed(name) {
				return contract.Errorf(contract.UsageError, "--%s needs --retries", name)
			}
		}
		return nil
	}
	raw, _ := flags.GetString("retries")
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > maxTailRetries {
		return contract.Errorf(contract.UsageError, "--retries must be between 1 and %d (got %s)", maxTailRetries, raw)
	}
	opts.Retries = n
	if opts.Org == "" {
		opts.Org = os.Getenv("SELECTIVE_ORG")
	}
	if opts.IdempotencyKey != "" {
		return nil
	}
	if flags.Changed("idempotency-key") {
		return contract.Errorf(contract.UsageError, "--idempotency-key must not be empty")
	}
	opts.IdempotencyKey, err = runs.NewIdempotencyKey()
	return err
}
