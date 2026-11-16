package approvals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/selectiveci/selective-cli/internal/wait"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const defaultWait = "10m"

type Outcome struct {
	Key      string
	Resource any
	Result   string
	DryRun   bool
}

type Gate struct {
	Client  *api.Client
	Out     io.Writer
	Err     io.Writer
	JSON    bool
	Wait    time.Duration
	Timeout time.Duration
	Started time.Time
	Clock   wait.Clock
	flags   *pflag.FlagSet
}

func BindWait(cmd *cobra.Command, g *Gate) {
	cmd.Flags().DurationVar(&g.Wait, "wait", 0, "Wait for a person to decide the approval, then finish the action, at most this long (default "+defaultWait+" when given without a value)")
	cmd.Flags().Lookup("wait").NoOptDefVal = defaultWait
	g.flags = cmd.Flags()
	cmd.Flags().DurationVar(&g.Timeout, "timeout", 0, "Stop after this long in total and exit 8")
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations["wait_default"] = defaultWait
	cmd.Annotations["events"] = "waiting,done"
}

func (g *Gate) Do(ctx context.Context, send func(context.Context) (*Outcome, error), sentence func(*Outcome) string) error {
	if err := g.validate(); err != nil {
		return err
	}
	if g.Timeout > 0 {
		start := g.Started
		if start.IsZero() {
			start = time.Now()
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadlineCause(ctx, start.Add(g.Timeout), contract.Errorf(contract.Timeout, "--timeout %s expired", g.Timeout))
		defer cancel()
	}
	err := g.run(ctx, send, sentence)
	if err != nil && ctx.Err() != nil {
		return context.Cause(ctx)
	}
	return err
}

func (g *Gate) validate() error {
	for _, name := range []string{"wait", "timeout"} {
		if g.flags == nil || !g.flags.Changed(name) {
			continue
		}
		if d, _ := g.flags.GetDuration(name); d <= 0 {
			return contract.Errorf(contract.UsageError, "--%s must be a positive duration", name)
		}
	}
	return nil
}

func (g *Gate) run(ctx context.Context, send func(context.Context) (*Outcome, error), sentence func(*Outcome) string) error {
	outcome, err := send(ctx)
	var pending *api.PendingApproval
	if !errors.As(err, &pending) {
		if err != nil {
			return err
		}
		if err := g.report(outcome, sentence); err != nil {
			return err
		}
		if g.Wait > 0 && g.JSON && !outcome.DryRun {
			return format.EventLine(g.Out, "done", doneFields(outcome))
		}
		return nil
	}
	id, url := identify(pending)
	if g.Wait <= 0 || pending.DryRun || id == "" {
		return err
	}
	if err := g.announce(pending, url); err != nil {
		return err
	}
	if _, err := Await(ctx, g.Client, id, g.waitOptions()); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	outcome, err = send(ctx)
	if errors.As(err, &pending) {
		_, url := identify(pending)
		again := contract.Errorf(contract.PendingApproval, "the re-sent request needs a new approval at %s", url)
		again.Fields = map[string]any{"approval": pending.Approval}
		return again
	}
	if err != nil {
		return err
	}
	if g.JSON {
		return format.EventLine(g.Out, "done", doneFields(outcome))
	}
	_, err = fmt.Fprintln(g.Out, sentence(outcome))
	return err
}

func (g *Gate) report(outcome *Outcome, sentence func(*Outcome) string) error {
	if g.JSON {
		return format.Write(g.Out, outcome.Key, outcome.Resource, outcome.Result, outcome.DryRun)
	}
	_, err := fmt.Fprintln(g.Out, sentence(outcome))
	return err
}

func (g *Gate) announce(pending *api.PendingApproval, url string) error {
	if g.JSON {
		return format.Document(g.Out, struct {
			Result   string          `json:"result"`
			DryRun   bool            `json:"dry_run"`
			Approval json.RawMessage `json:"approval"`
		}{"pending_approval", false, pending.Approval})
	}
	_, err := fmt.Fprintf(g.Err, "Approval needed: %s. Waiting for a person to decide it.\n", url)
	return err
}

func (g *Gate) waitOptions() wait.Options {
	opts := wait.Options{Limit: g.Wait, Origin: g.Started, Out: g.Out, Pretty: !g.JSON, Clock: g.Clock}
	if !g.JSON {
		opts.Out = g.Err
	}
	return opts
}

func doneFields(outcome *Outcome) map[string]any {
	return map[string]any{"result": outcome.Result, outcome.Key: outcome.Resource}
}

func identify(p *api.PendingApproval) (id, url string) {
	var approval struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	_ = json.Unmarshal(p.Approval, &approval)
	return approval.ID, approval.URL
}
