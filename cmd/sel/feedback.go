package main

import (
	"os"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/feedback"
	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/selectiveci/selective-cli/internal/runs"
	"github.com/selectiveci/selective-cli/internal/setup"
	"github.com/spf13/cobra"
)

func feedbackCmd() *cobra.Command {
	var opts feedback.Options
	var hostname string
	cmd := &cobra.Command{
		Use:   "feedback [TEXT]",
		Short: "Send product feedback to the Selective team",
		Long: `Send feedback about Selective to the team, for the current organization.
Give the text as an argument, or on stdin when there is no argument; an
argument wins, and stdin is not read then. Any token may send feedback, a
read-only one included.

The text is limited to 8192 bytes. Empty or longer text exits 5 before
anything is sent. Feedback is limited per token: past the limit the command
exits 4 with rate_limited and retry_after_s.

--dry-run checks the text and shows what would be sent, without contacting
the server or needing a login.

Each invocation sends one random Idempotency-Key and reuses it when the CLI
retries, so a lost response never sends the feedback twice. --idempotency-key K
sets the key yourself.`,
		Example: `  sel feedback "tail should show the build URL"
  echo "the flaky list is hard to scan" | sel feedback`,
		Args:        cobra.MaximumNArgs(1),
		Annotations: map[string]string{"exit_codes": "0,3,4,5,6", "mutates": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			stdin := cmd.InOrStdin()
			if f, ok := stdin.(*os.File); ok && isatty.IsTerminal(f.Fd()) {
				stdin = strings.NewReader("")
			}
			text, err := feedback.Read(strings.Join(args, ""), len(args) == 1, stdin)
			if err != nil {
				return err
			}
			if opts.Key == "" {
				if cmd.Flags().Changed("idempotency-key") {
					return contract.Errorf(contract.UsageError, "--idempotency-key must not be empty")
				}
				if opts.Key, err = runs.NewIdempotencyKey(); err != nil {
					return err
				}
			}
			opts.Version = version
			out := cmd.OutOrStdout()
			env := &setup.Env{Stdout: out, JSON: format.JSONMode(cmd.Flags(), out)}
			if !opts.DryRun {
				if env, err = setup.Connect(hostname, env.JSON); err != nil {
					return err
				}
				env.Stdout = out
			}
			return feedback.Send(env, text, opts)
		},
	}
	cmd.Flags().StringVar(&hostname, "hostname", "", "Selective server (default: the one you logged in to)")
	cmd.Flags().StringVar(&opts.Org, "org", "", "Organization slug (default: SELECTIVE_ORG, then your only organization)")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Check the text and show what would be sent, without contacting the server")
	cmd.Flags().StringVar(&opts.Key, "idempotency-key", "", "Use this Idempotency-Key instead of a random one, to span invocations")
	return cmd
}
