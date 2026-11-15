package main

import (
	"slices"
	"strings"

	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/selectiveci/selective-cli/internal/setup"
	"github.com/spf13/cobra"
)

var approvalStatuses = []string{"pending", "approved", "denied", "expired"}

func approvalsListCmd() *cobra.Command {
	var f setupFlags
	var status string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the organization's approvals",
		Long: `List the approvals of an organization, newest first, with each one's status,
action, subject, requester and expiry. Pages are fetched until next_cursor is
null; the walk stops early after 20000 approvals or 2000 requests, and notices
then carries pagination_stopped_early.

--status pending|approved|denied|expired narrows the list. With no --status
every approval is listed, so people usually pass --status pending. An approved
approval that was used still counts as approved, and an approval nobody used
in time counts as expired.

The organization is --org, then SELECTIVE_ORG, then your only one.

This is a command for people: the server answers 403 (exit 6) to agent tokens,
to tokens limited to repositories, and to anyone who is not an owner or admin
of the organization. An agent finds its own approval with sel approvals show,
using the id from its pending_approval result.`,
		Example:     "  sel approvals list --status pending\n  sel approvals list --org acme --json",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"exit_codes": "0,3,4,5,6"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if status != "" && !slices.Contains(approvalStatuses, status) {
				return contract.Errorf(contract.UsageError, "%q is not a status; use one of: %s", status, strings.Join(approvalStatuses, ", "))
			}
			env, org, err := f.env(true)
			if err != nil {
				return err
			}
			env.Stdout, env.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
			return approvalsList(env, org, status)
		},
	}
	f.bind(cmd, true)
	cmd.Flags().StringVar(&status, "status", "", "Only approvals with this status: "+strings.Join(approvalStatuses, ", "))
	return cmd
}

func approvalsList(env *setup.Env, org, status string) error {
	page, err := env.Client.Approvals(org, status)
	if err != nil {
		return err
	}
	return format.Document(env.Stdout, page)
}
