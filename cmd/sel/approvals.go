package main

import (
	"fmt"
	"io"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/selectiveci/selective-cli/internal/setup"
	"github.com/spf13/cobra"
)

func approvalsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "approvals",
		Short: "Inspect approval requests",
		Long: `Inspect approval requests.

  sel approvals show  One approval: its status, who asked and what for

A person decides an approval in the dashboard, never here.`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(approvalsShowCmd())
	return cmd
}

func approvalsShowCmd() *cobra.Command {
	var f setupFlags
	cmd := &cobra.Command{
		Use:   "show ID",
		Short: "Show one approval",
		Long: `Show the approval with this ID: its status, the action and subject it is for,
the token that asked, and the dashboard URL where a person decides it. The
status is pending, approved, denied or expired; an approved approval carries
consumed_at once its one retry has used it.

Any status exits 0: this only reads. Deciding happens in the dashboard.

An agent token sees only the approvals it asked for. An approval that does not
exist, or that this token may not see, exits 3 (not_found) with the same body.`,
		Example:     "  sel approvals show 0193b6a0-7c1e-7d3a-9f2b-4a5c6d7e8f90\n  sel approvals show ID --json",
		Args:        cobra.ExactArgs(1),
		Annotations: map[string]string{"exit_codes": "0,3,4,5,6"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if !tokenID.MatchString(args[0]) {
				return contract.Errorf(contract.UsageError, "%q is not an approval id (a UUID, shown in the approval's URL or in a pending_approval result)", args[0])
			}
			env, err := tokensEnv(cmd, &f)
			if err != nil {
				return err
			}
			return approvalsShow(env, args[0])
		},
	}
	f.bind(cmd, false)
	return cmd
}

func approvalsShow(env *setup.Env, id string) error {
	approval, err := env.Client.GetApproval(id)
	if err != nil {
		return err
	}
	if env.JSON {
		return format.Resource(env.Stdout, "approval", approval)
	}
	renderApproval(env.Stdout, approval)
	return nil
}

func renderApproval(w io.Writer, a *api.Approval) {
	subject := a.Subject.Type + " " + a.Subject.ID
	if a.Subject.Label != nil {
		subject = *a.Subject.Label
	}
	requester := "unknown"
	if a.RequestedBy.TokenName != nil {
		requester = *a.RequestedBy.TokenName
	}
	fmt.Fprintf(w, "%s %s\n\n", a.Action, subject)
	fmt.Fprintf(w, "Status:     %s\n", approvalStatusLine(a))
	if a.Status == "denied" && a.DecisionNote != nil {
		fmt.Fprintf(w, "Note:       %s\n", *a.DecisionNote)
	}
	fmt.Fprintf(w, "Requested:  by %s, %s UTC\nURL:        %s\n", requester, utcMinute(a.InsertedAt), a.URL)
}

func approvalStatusLine(a *api.Approval) string {
	switch a.Status {
	case "pending":
		return "pending, expires " + optionalUTC(a.ExpiresAt)
	case "approved":
		used := "not used yet"
		if a.ConsumedAt != nil {
			used = "used " + optionalUTC(a.ConsumedAt)
		}
		return fmt.Sprintf("approved %s, valid until %s, %s", optionalUTC(a.DecidedAt), optionalUTC(a.ExpiresAt), used)
	case "denied":
		return "denied " + optionalUTC(a.DecidedAt)
	case "expired":
		return "expired " + optionalUTC(a.ExpiresAt)
	}
	return a.Status
}

func optionalUTC(at *string) string {
	if at == nil {
		return "unknown"
	}
	return utcMinute(*at) + " UTC"
}
