package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/doctor"
	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/spf13/cobra"
)

var checklistIcons = map[doctor.Status]string{
	doctor.OK:   "✓",
	doctor.Warn: "⚠",
	doctor.Fail: "✗",
	doctor.Skip: "–",
}

func doctorCmd() *cobra.Command {
	var f setupFlags
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check that setup works: config, server, login, organization and suite",
		Long: `Run the setup checks in order and report each one:

  config   the config file and the server to talk to
  server   the server answers (and how fast)
  auth     the login works; shows who, never the token
  org      the organization resolves and is one of yours
  suite    a suite exists for this checkout's origin remote

A check that can't run because an earlier one failed is skipped. The exit
code is the first failing check's. --json writes one document with every
check; when a check fails it is the error object, carrying the same
document as its doctor field.`,
		Example: `  sel doctor
  sel doctor --org acme --json
  sel doctor --timeout 10s`,
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"exit_codes": "0,3,4,5,6,8"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if timeout <= 0 {
				return contract.Errorf(contract.UsageError, "--timeout must be longer than zero")
			}
			report := doctor.Run(doctor.Options{Hostname: f.hostname, Org: f.org, Timeout: timeout})
			err := report.Err()
			out := cmd.OutOrStdout()
			if format.JSONMode(cmd.Flags(), out) {
				if err != nil {
					return err
				}
				return format.Resource(out, "doctor", report)
			}
			if werr := writeChecklist(out, report); werr != nil {
				return werr
			}
			return err
		},
	}
	f.bind(cmd, true)
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "Give up after this long (exit 8)")
	return cmd
}

func writeChecklist(w io.Writer, report *doctor.Report) error {
	var b strings.Builder
	for _, c := range report.Checks {
		fmt.Fprintf(&b, "%s %-12s %s\n", checklistIcons[c.Status], c.Name, c.Message)
		if c.Fix != "" {
			fmt.Fprintf(&b, "  %-12s fix: %s\n", "", c.Fix)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}
