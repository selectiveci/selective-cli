package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/approvals"
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/selectiveci/selective-cli/internal/setup"
	"github.com/selectiveci/selective-cli/internal/wait"
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
	var opts showOptions
	cmd := &cobra.Command{
		Use:   "show ID",
		Short: "Show one approval",
		Long: `Show the approval with this ID: its status, the action and subject it is for,
the token that asked, and the dashboard URL where a person decides it. The
status is pending, approved, denied or expired; an approved approval carries
consumed_at once its one retry has used it.

Without --wait, any status exits 0: this only reads. Deciding happens in the
dashboard.

--wait[=DURATION] polls until a person decides it (10m when given without a
value; write --wait=5s, not --wait 5s), printing a waiting line per poll. It
ends with one line: an approved event (exit 0), or an error object that
carries the approval: approval_denied (6), approval_expired (7), or
pending_approval (7) when the wait ran out. --timeout DURATION caps the whole
command, plain read included, and exits 8.

An agent token sees only the approvals it asked for. An approval that does not
exist, or that this token may not see, exits 3 (not_found) with the same body.`,
		Example: "  sel approvals show 0193b6a0-7c1e-7d3a-9f2b-4a5c6d7e8f90\n  sel approvals show ID --wait=30m --json",
		Args:    cobra.ExactArgs(1),
		Annotations: map[string]string{
			"exit_codes":   "0,3,4,5,6,7,8",
			"events":       "waiting,approved",
			"wait_default": defaultApprovalWait,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.started = time.Now()
			if !tokenID.MatchString(args[0]) {
				return contract.Errorf(contract.UsageError, "%q is not an approval id (a UUID, shown in the approval's URL or in a pending_approval result)", args[0])
			}
			for _, name := range []string{"wait", "timeout"} {
				d, _ := cmd.Flags().GetDuration(name)
				if cmd.Flags().Changed(name) && d <= 0 {
					return contract.Errorf(contract.UsageError, "--%s must be a positive duration", name)
				}
			}
			env, err := tokensEnv(cmd, &f)
			if err != nil {
				return err
			}
			return approvalsShow(env, args[0], opts)
		},
	}
	f.bind(cmd, false)
	cmd.Flags().DurationVar(&opts.wait, "wait", 0, "Wait for a person to decide it, at most this long (default "+defaultApprovalWait+" when given without a value)")
	cmd.Flags().Lookup("wait").NoOptDefVal = defaultApprovalWait
	cmd.Flags().DurationVar(&opts.timeout, "timeout", 0, "Stop after this long in total and exit 8")
	return cmd
}

const defaultApprovalWait = "10m"

var approvalsClock wait.Clock

type showOptions struct {
	wait, timeout time.Duration
	started       time.Time
}

func approvalsShow(env *setup.Env, id string, opts showOptions) error {
	ctx := context.Background()
	if opts.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadlineCause(ctx, opts.started.Add(opts.timeout), contract.Errorf(contract.Timeout, "--timeout %s expired", opts.timeout))
		defer cancel()
	}
	approval, err := fetchApproval(ctx, env, id, opts)
	if err != nil {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		return err
	}
	switch {
	case env.JSON && opts.wait > 0:
		return format.EventLine(env.Stdout, "approved", map[string]any{"approval": approval})
	case env.JSON:
		return format.Resource(env.Stdout, "approval", approval)
	}
	renderApproval(env.Stdout, approval)
	return nil
}

func fetchApproval(ctx context.Context, env *setup.Env, id string, opts showOptions) (*api.Approval, error) {
	if opts.wait <= 0 {
		return env.Client.WithContext(ctx).GetApproval(id)
	}
	waiting := wait.Options{Limit: opts.wait, Origin: opts.started, Out: env.Stdout, Pretty: !env.JSON, Clock: approvalsClock}
	if !env.JSON {
		waiting.Out = env.Stderr
	}
	return approvals.Await(ctx, env.Client, id, waiting)
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
